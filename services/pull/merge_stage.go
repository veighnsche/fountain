// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"forgejo.org/models"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
)

// prepareMergeCommits is the one ordinary merge-method engine. Computing local
// objects is separate from LFS transfer, ref push and native completion.
func prepareMergeCommits(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User, mergeStyle repo_model.MergeStyle, expectedHeadCommitID, expectedBaseOID, message string) (*mergeContext, context.CancelFunc, error) {
	// Clone base repo.
	mergeCtx, cancel, err := createTemporaryRepoForMerge(ctx, pr, doer, expectedHeadCommitID)
	if err != nil {
		return nil, nil, err
	}
	keep := false
	defer func() {
		if !keep {
			cancel()
		}
	}()
	if expectedBaseOID != "" {
		base, err := git.GetFullCommitID(ctx, mergeCtx.tmpBasePath, "original_"+baseBranch)
		if err != nil {
			return nil, nil, err
		}
		if base != expectedBaseOID {
			return nil, nil, ErrExactMergeRefused{Reason: ExactMergeRefusedStaleBase}
		}
	}

	// Merge commits.
	switch mergeStyle {
	case repo_model.MergeStyleMerge:
		if err := doMergeStyleMerge(mergeCtx, message); err != nil {
			return nil, nil, err
		}
	case repo_model.MergeStyleRebase, repo_model.MergeStyleRebaseMerge:
		if err := doMergeStyleRebase(mergeCtx, mergeStyle, message); err != nil {
			return nil, nil, err
		}
	case repo_model.MergeStyleSquash:
		if err := doMergeStyleSquash(mergeCtx, message); err != nil {
			return nil, nil, err
		}
	case repo_model.MergeStyleFastForwardOnly:
		if err := doMergeStyleFastForwardOnly(mergeCtx); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, models.ErrInvalidMergeStyle{ID: pr.BaseRepo.ID, Style: mergeStyle}
	}

	keep = true
	return mergeCtx, cancel, nil
}

// PreparedMergeResult describes computed local objects, never a published ref or
// operation receipt. Its parent/tree evidence binds the actual method result.
type PreparedMergeResult struct {
	CommitID, TreeID, HeadOID, BaseOID string
	Parents                            []string
	Method                             repo_model.MergeStyle
}

// PreparedExactMerge owns a private local worktree and the native PR working
// pool hold. Close is mandatory; this object exposes no publish operation.
type PreparedExactMerge struct {
	merge   *mergeContext
	result  PreparedMergeResult
	close   func()
	runMu   sync.Mutex
	mu      sync.RWMutex
	closed  bool
	started bool
	target  preparedMergeTarget
}

// Private input facts survive later native model reloads and caller mutation.
type preparedMergeTarget struct {
	owner                         string
	generation, actor, repository int64
	pull, issue, index            int64
	headRepository, poster        int64
	originalAuthor                int64
	baseBranch, headBranch        string
	flow                          issues_model.PullRequestFlow
	message                       [32]byte
}

func (p *PreparedExactMerge) Result() PreparedMergeResult {
	r := p.result
	r.Parents = append([]string(nil), r.Parents...)
	return r
}

func (p *PreparedExactMerge) Close() {
	if p == nil {
		return
	}
	// A Git push calls back into WithExactTarget through its native hook. Wait
	// here before queuing the RW writer so that callback can still take RLock.
	p.runMu.Lock()
	defer p.runMu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		if p.close != nil {
			p.close()
		}
	}
}

// WithExactTarget runs a native gate against this open stage's original inputs.
// Close waits for that gate. No private path, mutation or publish operation is
// exposed, and a result copied from another stage cannot substitute for it.
func (p *PreparedExactMerge) WithExactTarget(ctx context.Context, pr *issues_model.PullRequest, actor *user_model.User, method repo_model.MergeStyle, head, base, message string, gate func(PreparedMergeResult) error) error {
	return p.withExactTarget(ctx, pr, actor, method, head, base, message, false, false, gate)
}

// WithQuiescentExactTarget proves the original native stage attempted its single
// synchronous push and Close finished waiting for the Git child/hooks and local
// resource cleanup. A result copy, an open/unused stage or a foreign generation
// cannot substitute. Closed stages cannot acquire another push or gate.
func (p *PreparedExactMerge) WithQuiescentExactTarget(ctx context.Context, pr *issues_model.PullRequest, actor *user_model.User, method repo_model.MergeStyle, head, base, message string, gate func(PreparedMergeResult) error) error {
	return p.withExactTarget(ctx, pr, actor, method, head, base, message, true, true, gate)
}

// WithClosedExactTarget also accepts a closed stage that never entered the push
// engine. Refusal cleanup separately proves non-admission and no ref effect;
// committed completion must use WithQuiescentExactTarget's attempted-push check.
func (p *PreparedExactMerge) WithClosedExactTarget(ctx context.Context, pr *issues_model.PullRequest, actor *user_model.User, method repo_model.MergeStyle, head, base, message string, gate func(PreparedMergeResult) error) error {
	return p.withExactTarget(ctx, pr, actor, method, head, base, message, true, false, gate)
}

func (p *PreparedExactMerge) withExactTarget(ctx context.Context, pr *issues_model.PullRequest, actor *user_model.User, method repo_model.MergeStyle, head, base, message string, closed, requireStarted bool, gate func(PreparedMergeResult) error) error {
	if p == nil || pr == nil || pr.Issue == nil || actor == nil || gate == nil {
		return ErrExactMergeRefused{Reason: ExactMergeRefusedPRMismatch}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	x, t := execcontext.FromContext(ctx), p.target
	if p.closed != closed || requireStarted && !p.started || p.merge == nil || x == nil || x.Owner != t.owner || x.Generation != t.generation || actor.ID != t.actor || pr.BaseRepoID != t.repository || pr.ID != t.pull || pr.IssueID != t.issue || pr.Index != t.index || pr.BaseBranch != t.baseBranch || pr.HeadRepoID != t.headRepository || pr.HeadBranch != t.headBranch || pr.Flow != t.flow || pr.Issue.PosterID != t.poster || pr.Issue.OriginalAuthorID != t.originalAuthor || sha256.Sum256([]byte(message)) != t.message || p.result.Method != method || p.result.HeadOID != head || p.result.BaseOID != base {
		return ErrExactMergeRefused{Reason: ExactMergeRefusedPRMismatch}
	}
	return gate(p.Result())
}

// PrepareExactMerge computes all ordinary immediate methods with the existing
// engine against exact copied head/base tips. The caller owns human/native policy
// and must hold the global execution throughout. No LFS or remote ref is written.
func PrepareExactMerge(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User, method repo_model.MergeStyle, head, base, message string) (*PreparedExactMerge, error) {
	execution := execcontext.FromContext(ctx)
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return nil, err
	}
	if execution == nil || execution.Owner == "" || held.Owner != execution.Owner || held.Generation != execution.Generation {
		return nil, nativeoperation_model.ErrBusy
	}
	_, headErr := git.NewIDFromString(head)
	_, baseErr := git.NewIDFromString(base)
	if pr == nil || pr.Issue == nil || pr.ID <= 0 || pr.BaseRepoID <= 0 || pr.IssueID <= 0 || doer == nil || doer.ID <= 0 || headErr != nil || baseErr != nil {
		return nil, ErrExactMergeRefused{Reason: ExactMergeRefusedPRMismatch}
	}
	pullWorkingPool.CheckIn(fmt.Sprint(pr.ID))
	release := func() { pullWorkingPool.CheckOut(fmt.Sprint(pr.ID)) }
	merge, cancel, err := prepareMergeCommits(ctx, pr, doer, method, head, base, message)
	if err != nil {
		release()
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			cancel()
			release()
		}
	}()
	repo, err := git.OpenRepository(ctx, merge.tmpBasePath)
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	result, err := repo.GetBranchCommit(baseBranch)
	if err != nil {
		return nil, err
	}
	r := PreparedMergeResult{CommitID: result.ID.String(), TreeID: result.Tree.ID.String(), HeadOID: head, BaseOID: base, Method: method}
	for _, parent := range result.Parents {
		r.Parents = append(r.Parents, parent.String())
	}
	held, err = nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return nil, err
	}
	if held.Owner != execution.Owner || held.Generation != execution.Generation {
		return nil, nativeoperation_model.ErrBusy
	}
	keep = true
	target := preparedMergeTarget{owner: execution.Owner, generation: execution.Generation, actor: doer.ID, repository: pr.BaseRepoID, pull: pr.ID, issue: pr.IssueID, index: pr.Index, headRepository: pr.HeadRepoID, headBranch: pr.HeadBranch, baseBranch: pr.BaseBranch, flow: pr.Flow, poster: pr.Issue.PosterID, originalAuthor: pr.Issue.OriginalAuthorID, message: sha256.Sum256([]byte(message))}
	return &PreparedExactMerge{merge: merge, result: r, target: target, close: func() { cancel(); release() }}, nil
}

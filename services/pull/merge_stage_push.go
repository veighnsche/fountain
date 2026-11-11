// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	repo_module "forgejo.org/modules/repository"
)

var (
	ErrPreparedMergeHooks   = errors.New("native prepared merge hooks are unavailable")
	ErrPreparedMergeStarted = errors.New("native prepared merge push was already attempted")
)

// CheckPreparedMergeHooks requires the actual Git-selected hook path and current
// native delegates. A missing/overridden hook must refuse before any push, not
// merely be noticed when a ref changed without an admission.
func CheckPreparedMergeHooks(ctx context.Context, repository *repo_model.Repository) error {
	if repository == nil {
		return ErrPreparedMergeHooks
	}
	selected, _, err := git.NewCommand(ctx, "rev-parse", "--path-format=absolute", "--git-path", "hooks/reference-transaction").RunStdString(&git.RunOpts{Dir: repository.RepoPath()})
	if err != nil || strings.TrimSpace(selected) != filepath.Join(repository.RepoPath(), "hooks", "reference-transaction") {
		return ErrPreparedMergeHooks
	}
	problems, checkErr := repo_module.CheckDelegateHooks(repository.RepoPath())
	if checkErr != nil || len(problems) != 0 {
		return ErrPreparedMergeHooks
	}
	return nil
}

// PushExact uses the one native LFS/push engine for this open stage, at most
// once. The service owns the saved command, live human and prepared admission;
// its guard runs around LFS and immediately before launching Git. The returned
// attempted bit is an execution hint, never a receipt or effect classification.
// Close waits for the child and its hooks to finish. No PR completion occurs here.
func (p *PreparedExactMerge) PushExact(ctx context.Context, pr *issues_model.PullRequest, actor *user_model.User, method repo_model.MergeStyle, head, base, message string, guard func() error) (attempted bool, err error) {
	if p == nil || guard == nil {
		return false, ErrExactMergeRefused{Reason: ExactMergeRefusedPRMismatch}
	}
	p.runMu.Lock()
	defer p.runMu.Unlock()
	if p.started {
		return false, ErrPreparedMergeStarted
	}
	err = p.WithExactTarget(ctx, pr, actor, method, head, base, message, func(result PreparedMergeResult) error {
		x := execcontext.FromContext(ctx)
		held, err := nativeoperation_model.ReadReservation(ctx)
		if err != nil {
			return err
		}
		if x == nil || held.OwnerKind != nativeoperation_model.OwnerConditional || x.Owner != held.Owner || x.Generation != held.Generation {
			return nativeoperation_model.ErrWrongOwner
		}
		proof, err := execcontext.ReadCapabilityFile(x.CapabilityPath)
		if err != nil || !execcontext.VerifyProof(held.Verifier, proof) {
			return nativeoperation_model.ErrWrongOwner
		}
		if err := CheckPreparedMergeHooks(ctx, pr.BaseRepo); err != nil {
			return err
		}
		// Guard failures are pre-launch refusals. Once this attempt reaches the
		// shared native engine it cannot be retried with the same local stage,
		// including when LFS, transport or post-receive reports an error.
		if err := guard(); err != nil {
			return err
		}
		p.started, attempted = true, true
		_, err = pushMergeCommits(ctx, p.merge, pr, actor, repo_module.PushTriggerPRMergeToBase, &result, guard)
		return err
	})
	return attempted, err
}

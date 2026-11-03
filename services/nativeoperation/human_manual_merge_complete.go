// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Persisted native receipt codec.
	"errors"
	"os"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"

	"github.com/google/uuid"
)

const HumanManualMergeFinishStarted = "human-manual-merge-finish-started"
const HumanManualMergeFinishDone = "human-manual-merge-finish-done"

var (
	ErrHumanManualMergeEvidence           = errors.New("native manual merge evidence differs")
	ErrHumanManualMergeFinishClaimed      = errors.New("native manual merge finishing was already claimed")
	ErrHumanManualMergeCompletionNotReady = errors.New("native manual merge completion is not ready")
)

func readHumanManualMergeHeld(ctx context.Context, installation string, q sdk.HumanManualMergeRequest) (*ValidIntent, *nativeoperation_model.Reservation, Scope, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation {
		return nil, nil, Scope{}, ErrInvalidIntent
	}
	intent, err := humanManualMergeIntent(q)
	if err != nil {
		return nil, nil, Scope{}, err
	}
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return nil, nil, Scope{}, err
	}
	x := execcontext.FromContext(ctx)
	if x == nil || x.Owner != held.Owner || x.Generation != held.Generation || held.OwnerKind != nativeoperation_model.OwnerConditional || held.Owner != conditionalOwner(installation, q.OperationID) || held.Revision != intent.ExpectedNativeRevision+1 {
		return nil, nil, Scope{}, nativeoperation_model.ErrWrongOwner
	}
	proof, err := execcontext.ReadCapabilityFile(x.CapabilityPath)
	if err != nil || !execcontext.VerifyProof(held.Verifier, proof) {
		return nil, nil, Scope{}, nativeoperation_model.ErrWrongOwner
	}
	scope, err := decodeScope(held.Scope)
	if err != nil {
		return nil, nil, Scope{}, err
	}
	if err := checkHumanManualMergeScope(q, scope, held.Scope); err != nil {
		return nil, nil, Scope{}, err
	}
	return intent, held, scope, nil
}

// Live completion and offline recovery use one exact scope definition.
func checkHumanManualMergeScope(q sdk.HumanManualMergeRequest, scope Scope, rawScope string) error {
	exact := humanManualMergeScope(q)
	if scope.CompletionPhase != "" && scope.CompletionPhase != HumanManualMergeFinishStarted && scope.CompletionPhase != HumanManualMergeFinishDone {
		return ErrInvalidIntent
	}
	exact.CompletionPhase = scope.CompletionPhase
	raw, err := encodeScope(exact)
	if err != nil || raw != rawScope {
		return ErrInvalidIntent
	}
	return nil
}

// Completion consumes the immutable admitted result, not renewed permission to
// mark a PR. Exact native SQL/history and unchanged target/object evidence must
// still match; contradictions retain primary truth and the mutation fence.
func (s *Service) withHumanManualMergeReceipt(ctx context.Context, installation string, q sdk.HumanManualMergeRequest, consume func(context.Context, *nativeoperation_model.Operation, *issues_model.PullRequest, *user_model.User) error) error {
	intent, held, _, err := readHumanManualMergeHeld(ctx, installation, q)
	if err != nil {
		return err
	}
	op, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return err
	}
	if op == nil {
		return ErrHumanManualMergeEvidence
	}
	return nativeoperation_model.WithHumanManualMergeReceipt(ctx, humanManualMergeOperation(installation, intent), held.Owner, held.Generation, held.Scope, op.Receipt, func(tx context.Context, current *nativeoperation_model.Operation) error {
		pr, actor, err := readHumanManualMergeReceipt(tx, q, current.Receipt)
		if err != nil {
			return err
		}
		return consume(tx, current, pr, actor)
	})
}

func readHumanManualMergeReceipt(ctx context.Context, q sdk.HumanManualMergeRequest, receiptJSON string) (*issues_model.PullRequest, *user_model.User, error) {
	var receipt sdk.HumanManualMergeReceipt
	if json.Unmarshal([]byte(receiptJSON), &receipt) != nil || !sdk.ValidHumanManualMergeReceipt(receipt, q) {
		return nil, nil, ErrHumanManualMergeEvidence
	}
	raw, err := json.Marshal(receipt)
	if err != nil || string(raw) != receiptJSON {
		return nil, nil, ErrHumanManualMergeEvidence
	}
	pr, err := readHumanManualMergeParent(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	if !pr.HasMerged || !pr.Issue.IsClosed || pr.Status != issues_model.PullRequestStatusManuallyMerged || pr.MergedCommitID != receipt.CommitOID || pr.MergerID != receipt.ActorID || int64(pr.MergedUnix) != receipt.MergedUnix || int64(pr.Issue.ClosedUnix) != receipt.ClosedUnix {
		return nil, nil, ErrHumanManualMergeEvidence
	}
	history, err := issues_model.GetCommentByID(ctx, receipt.HistoryID)
	if err != nil {
		return nil, nil, err
	}
	if history.Type != issues_model.CommentTypeMergePull || history.IssueID != receipt.IssueID || history.PosterID != receipt.ActorID {
		return nil, nil, ErrHumanManualMergeEvidence
	}
	actor, err := user_model.GetUserByID(ctx, receipt.ActorID)
	if err != nil {
		return nil, nil, err
	}
	return pr, actor, nil
}

// No current admission or policy is reconstructed here: this only proves the
// exact native parent and unchanged immutable target/selected commit evidence.
func readHumanManualMergeParent(ctx context.Context, q sdk.HumanManualMergeRequest) (*issues_model.PullRequest, error) {
	p := q.Expected
	pr, err := issues_model.GetPullRequestByID(ctx, nativeCommentID(p.PullID))
	if err != nil {
		return nil, err
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return nil, err
	}
	if pr.BaseRepoID != nativeCommentID(p.RepositoryID) || pr.IssueID != nativeCommentID(p.IssueID) || pr.Index != nativeCommentID(p.Index) || pr.BaseBranch != p.BaseBranch || !pr.Issue.IsPull || pr.Issue.RepoID != pr.BaseRepoID || pr.Issue.Index != pr.Index {
		return nil, ErrHumanManualMergeEvidence
	}
	repo, err := repo_model.GetRepositoryByID(ctx, pr.BaseRepoID)
	if err != nil {
		return nil, err
	}
	native, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		return nil, err
	}
	defer native.Close()
	base, err := native.GetRefCommitID(git.BranchPrefix + p.BaseBranch)
	if err != nil || base != p.BaseOID {
		return nil, ErrHumanManualMergeEvidence
	}
	commit, err := pull_service.ReadManualMergeCommit(native, repo.ObjectFormatName, p.BaseBranch, p.CommitOID)
	if err != nil || commit.ID.String() != p.CommitOID || commit.Author.When.Unix() == 0 || pr.HasMerged && commit.Author.When.Unix() != int64(pr.MergedUnix) {
		return nil, ErrHumanManualMergeEvidence
	}
	pr.BaseRepo, pr.Issue.Repo = repo, repo
	return pr, nil
}

// FinishHumanManualMerge durably claims the defining native notifications and
// cross-reference work once, outside any caller SQL transaction. Uncertain or
// failed dispatch stays fenced and is never repeated by this method.
func (s *Service) FinishHumanManualMerge(ctx context.Context, installation string, q sdk.HumanManualMergeRequest) (sdk.OperationRecord, error) {
	if db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, held, scope, err := readHumanManualMergeHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if scope.CompletionPhase != "" {
		return sdk.OperationRecord{}, ErrHumanManualMergeFinishClaimed
	}
	started := scope
	started.CompletionPhase = HumanManualMergeFinishStarted
	startedJSON, err := encodeScope(started)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	done := scope
	done.CompletionPhase = HumanManualMergeFinishDone
	doneJSON, err := encodeScope(done)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	want := humanManualMergeOperation(installation, intent)
	var pull *issues_model.PullRequest
	var actor *user_model.User
	var receipt string
	err = s.withHumanManualMergeReceipt(ctx, installation, q, func(tx context.Context, op *nativeoperation_model.Operation, pr *issues_model.PullRequest, merger *user_model.User) error {
		if err := nativeoperation_model.ClaimHumanManualMergeFinish(tx, want, held.Owner, held.Generation, held.Scope, startedJSON, op.Receipt); err != nil {
			return err
		}
		pull, actor, receipt = pr, merger, op.Receipt
		return nil
	})
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	finishErr := pull_service.FinishManualMerge(ctx, pull, actor)
	recorded, err := nativeoperation_model.RecordHumanManualMergeFinish(context.WithoutCancel(ctx), want, held.Owner, held.Generation, startedJSON, doneJSON, receipt, finishErr == nil)
	if err != nil {
		return sdk.OperationRecord{}, errors.Join(err, finishErr)
	}
	return ToRecord(recorded), finishErr
}

// CompleteHumanManualMerge releases only a successfully finished, exact SQL
// primary. There is no Git writer or push event to wait for in this operation.
// Stopped-writer recovery is separate; retained lookup resolves lost replies.
func (s *Service) CompleteHumanManualMerge(ctx context.Context, installation string, q sdk.HumanManualMergeRequest) (sdk.OperationRecord, error) {
	if db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, held, scope, err := readHumanManualMergeHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if scope.CompletionPhase != HumanManualMergeFinishDone {
		return sdk.OperationRecord{}, ErrHumanManualMergeCompletionNotReady
	}
	var recorded *nativeoperation_model.Operation
	err = s.withHumanManualMergeReceipt(ctx, installation, q, func(tx context.Context, op *nativeoperation_model.Operation, _ *issues_model.PullRequest, _ *user_model.User) error {
		var err error
		recorded, err = nativeoperation_model.CompleteHumanManualMerge(tx, humanManualMergeOperation(installation, intent), held.Owner, held.Generation, held.Scope, op.Receipt)
		return err
	})
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	record := ToRecord(recorded)
	if err := os.Remove(execcontext.FromContext(ctx).CapabilityPath); err != nil && !os.IsNotExist(err) {
		return record, err
	}
	return record, nil
}

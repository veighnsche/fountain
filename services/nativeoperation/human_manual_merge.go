// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Persisted native receipt codec.
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"

	"github.com/google/uuid"
)

var errHumanManualMergeChanged = errors.New("manual merge preparation changed")

func humanManualMergeIntent(q sdk.HumanManualMergeRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanManualMergeRequest(q) {
		return nil, ErrInvalidIntent
	}
	p := q.Expected
	v := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(p.ActorID), RepositoryID: nativeCommentID(p.RepositoryID), Kind: nativeoperation_model.KindHumanManualMerge, AuthRevision: sdk.HumanManualMergeAuthorizationRevision, ExpectedNativeRevision: p.NativeRevision, NotAfter: q.NotAfter}
	var err error
	v.Canonical, v.Digest, err = sdk.CanonicalHumanManualMergeIntent(q)
	return v, err
}

func humanManualMergeScope(q sdk.HumanManualMergeRequest) Scope {
	return Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: nativeCommentID(q.Expected.RepositoryID), IssueID: nativeCommentID(q.Expected.IssueID), PRNumber: nativeCommentID(q.Expected.Index)}
}

func humanManualMergeOperation(installation string, v *ValidIntent) *nativeoperation_model.Operation {
	return &nativeoperation_model.Operation{InstallationID: installation, OperationID: v.OperationID, ActorID: v.ActorID, RepositoryID: v.RepositoryID, Kind: v.Kind, AuthRevision: v.AuthRevision, ExpectedNativeRevision: v.ExpectedNativeRevision, NotAfter: v.NotAfter, Intent: v.Canonical, IntentDigest: v.Digest}
}

func (s *Service) checkHumanManualMergeCapture(ctx context.Context, installation string, v *ValidIntent, scope string) error {
	if ctx.Err() != nil {
		return ErrAuthorityLost
	}
	if s.clock() >= v.NotAfter {
		return nativeoperation_model.ErrOperationExpired
	}
	op, err := nativeoperation_model.LookupOperation(ctx, installation, v.OperationID)
	if err != nil {
		return err
	}
	if !humanMergeCaptureMatches(op, v) || op.Admitted || op.EffectState != nativeoperation_model.EffectPending || op.Receipt != "" || op.Completion != "" {
		return nativeoperation_model.ErrAdmissionLost
	}
	if op.Revoked {
		return nativeoperation_model.ErrAdmissionLost
	}
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return err
	}
	x := execcontext.FromContext(ctx)
	if x == nil || held.Owner != conditionalOwner(installation, v.OperationID) || x.Owner != held.Owner || x.Generation != held.Generation || held.OwnerKind != nativeoperation_model.OwnerConditional || held.Scope != scope {
		return nativeoperation_model.ErrWrongOwner
	}
	proof, err := execcontext.ReadCapabilityFile(x.CapabilityPath)
	if err != nil || !execcontext.VerifyProof(held.Verifier, proof) {
		return nativeoperation_model.ErrWrongOwner
	}
	return requireHumanPrimaryOwner(ctx, v.ExpectedNativeRevision)
}

// This reads identity/current human policy and immutable target evidence. Open
// state is checked separately before SQL, so the same permission/target check
// can run again after our own mark and roll it back on authority withdrawal.
func readHumanManualMergeTarget(ctx context.Context, p sdk.RepositoryManualMergeSnapshot, admit HumanReviewAdmission) (*issues_model.PullRequest, *user_model.User, error) {
	if ctx.Err() != nil || admit(ctx) != nil {
		return nil, nil, ErrAuthorityLost
	}
	if err := requireHumanPrimaryOwner(ctx, p.NativeRevision); err != nil {
		return nil, nil, err
	}
	actor, err := user_model.GetUserByID(ctx, nativeCommentID(p.ActorID))
	if err != nil || !actor.IsAccessAllowed(ctx) {
		return nil, nil, ErrAuthorityLost
	}
	repo, err := repo_model.GetRepositoryByID(ctx, nativeCommentID(p.RepositoryID))
	if err != nil || repo.IsArchived {
		return nil, nil, ErrAuthorityLost
	}
	pr, err := issues_model.GetPullRequestByID(ctx, nativeCommentID(p.PullID))
	if err != nil || pr.BaseRepoID != repo.ID || pr.IssueID != nativeCommentID(p.IssueID) || pr.Index != nativeCommentID(p.Index) || pr.BaseBranch != p.BaseBranch {
		return nil, nil, errHumanManualMergeChanged
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return nil, nil, err
	}
	if !pr.Issue.IsPull || pr.Issue.RepoID != repo.ID || pr.Issue.Index != pr.Index {
		return nil, nil, errHumanManualMergeChanged
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, actor)
	if err != nil {
		return nil, nil, err
	}
	for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
		if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
			return nil, nil, ErrAuthorityLost
		}
	}
	if !repo.AllowsPulls(ctx) {
		return nil, nil, ErrAuthorityLost
	}
	prUnit, err := repo.GetUnit(ctx, unit.TypePullRequests)
	if err != nil {
		return nil, nil, err
	}
	if !prUnit.PullRequestsConfig().AllowManualMerge {
		return nil, nil, pull_service.ErrManualMergeCommit
	}
	allowed, err := pull_service.IsUserAllowedToMerge(ctx, pr, permission, actor)
	if err != nil {
		return nil, nil, err
	}
	if !allowed {
		return nil, nil, ErrAuthorityLost
	}
	native, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		return nil, nil, err
	}
	defer native.Close()
	base, err := native.GetRefCommitID(git.BranchPrefix + p.BaseBranch)
	if err != nil || base != p.BaseOID {
		return nil, nil, errHumanManualMergeChanged
	}
	commit, err := pull_service.ReadManualMergeCommit(native, repo.ObjectFormatName, p.BaseBranch, p.CommitOID)
	if err != nil {
		return nil, nil, err
	}
	if commit.ID.String() != p.CommitOID || commit.Author.When.Unix() == 0 {
		return nil, nil, pull_service.ErrManualMergeCommit
	}
	pr.BaseRepo, pr.Issue.Repo = repo, repo
	return pr, actor, nil
}

// SubmitHumanManualMergePrimary captures one live human command and commits its
// native SQL mark/history/receipt atomically. It never pushes Git or dispatches
// completion. A committed primary retains the owner/capability for that consumer;
// retries return existing truth without resuming any writer.
func (s *Service) SubmitHumanManualMergePrimary(ctx context.Context, installation string, q sdk.HumanManualMergeRequest, admit HumanReviewAdmission) (sdk.OperationRecord, error) {
	return s.submitHumanManualMerge(ctx, installation, q, admit, false)
}

const (
	CrashPointHumanManualMergeBeforePrimary = "human-manual-merge-before-primary"
	CrashPointHumanManualMergeInPrimary     = "human-manual-merge-in-primary"
	CrashPointHumanManualMergeAfterPrimary  = "human-manual-merge-after-primary"
	CrashPointHumanManualMergeAfterFinish   = "human-manual-merge-after-finish"
	CrashPointHumanManualMergeAfterRelease  = "human-manual-merge-after-release"
)

// SubmitHumanManualMerge composes one human capture with its atomic SQL primary,
// native one-shot finishing and exact release. Existing operations return saved
// truth without resuming work. Post-commit bookkeeping survives request loss;
// the caller must still pass current human admission to receive the result.
func (s *Service) SubmitHumanManualMerge(ctx context.Context, installation string, q sdk.HumanManualMergeRequest, admit HumanReviewAdmission) (sdk.OperationRecord, error) {
	return s.submitHumanManualMerge(ctx, installation, q, admit, true)
}

func (s *Service) submitHumanManualMerge(ctx context.Context, installation string, q sdk.HumanManualMergeRequest, admit HumanReviewAdmission, complete bool) (sdk.OperationRecord, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil || db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	v, err := humanManualMergeIntent(q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if ctx.Err() != nil {
		return sdk.OperationRecord{}, ctx.Err()
	}
	if admit(ctx) != nil {
		return sdk.OperationRecord{}, ErrAuthorityLost
	}
	finish := func(record sdk.OperationRecord, err error) (sdk.OperationRecord, error) {
		if err == nil || errors.Is(err, ErrCancelledBeforeSubmit) {
			if ctx.Err() != nil || admit(ctx) != nil {
				return sdk.OperationRecord{}, ErrAuthorityLost
			}
		}
		return record, err
	}
	existing, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if existing != nil {
		r, e := s.reconcileExisting(existing, v)
		return finish(r, e)
	}
	if q.NotAfter <= s.clock() || q.NotAfter > s.clock()+int64(MaxOperationLifetime/time.Second) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := humanManualMergeScope(q)
	rawScope, err := encodeScope(scope)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	record, err := s.claimConditionalSubmit(ctx, extensionauth_model.SubmissionDecision{ActorID: v.ActorID}, installation, v, scope, func(ctx context.Context, op *nativeoperation_model.Operation, _ *ValidIntent, x *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
		owned, settle := execcontext.NewContext(ctx, x), context.WithoutCancel(ctx)
		refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
			r, err := nativeoperation_model.RefuseHumanManualMerge(settle, humanManualMergeOperation(installation, v), x.Owner, x.Generation, rawScope, reason)
			if err != nil {
				return sdk.OperationRecord{}, terminalResult{}, err
			}
			return ToRecord(r), terminalResult{}, nil
		}
		apply := func(tx context.Context) (string, error) {
			if err := s.checkHumanManualMergeCapture(tx, installation, v, rawScope); err != nil {
				return "", err
			}
			pr, actor, err := readHumanManualMergeTarget(tx, q.Expected, admit)
			if err != nil {
				return "", err
			}
			if pr.HasMerged || pr.Issue.IsClosed != q.Expected.IsClosed {
				return "", errHumanManualMergeChanged
			}
			native, err := git.OpenRepository(tx, pr.BaseRepo.RepoPath())
			if err != nil {
				return "", err
			}
			defer native.Close()
			marked, err := pull_service.ApplyManualMergeMetadata(tx, pr.ID, actor, native, q.Expected.CommitOID)
			// No further Git reads need this handle; retire its readers while the
			// SQL transaction is still held, including at the disclosed checkpoint.
			native.Close()
			if err != nil {
				return "", err
			}
			if _, _, err := readHumanManualMergeTarget(tx, q.Expected, admit); err != nil {
				return "", err
			}
			if err := s.checkHumanManualMergeCapture(tx, installation, v, rawScope); err != nil {
				return "", err
			}
			var history issues_model.Comment
			found, err := db.GetEngine(tx).Where("issue_id=? AND type=?", marked.IssueID, issues_model.CommentTypeMergePull).Desc("id").Get(&history)
			if err != nil {
				return "", err
			}
			if !found || history.PosterID != actor.ID {
				return "", errors.New("native manual merge history missing")
			}
			receipt := sdk.HumanManualMergeReceipt{RepositoryID: v.RepositoryID, ActorID: v.ActorID, PullID: marked.ID, IssueID: marked.IssueID, Index: marked.Index, BaseBranch: marked.BaseBranch, BaseOID: q.Expected.BaseOID, CommitOID: marked.MergedCommitID, MergedUnix: int64(marked.MergedUnix), ClosedUnix: int64(marked.Issue.ClosedUnix), HistoryID: history.ID, IntentDigest: v.Digest}
			if !sdk.ValidHumanManualMergeReceipt(receipt, q) {
				return "", errors.New("native manual merge receipt differs")
			}
			raw, err := json.Marshal(receipt)
			if err == nil && complete {
				err = TestCrashBarrier(CrashPointHumanManualMergeInPrimary)
			}
			return string(raw), err
		}
		// Check live admission before entering SQL, allowing ordinary cancellation
		// to win outside the primary transaction; then recheck everything inside.
		if complete {
			if err := TestCrashBarrier(CrashPointHumanManualMergeBeforePrimary); err != nil {
				return sdk.OperationRecord{}, terminalResult{}, err
			}
		}
		_, _, err := readHumanManualMergeTarget(owned, q.Expected, admit)
		if err == nil {
			err = s.checkHumanManualMergeCapture(owned, installation, v, rawScope)
		}
		var committed *nativeoperation_model.Operation
		if err == nil {
			committed, err = nativeoperation_model.CommitHumanManualMergePrimary(owned, humanManualMergeOperation(installation, v), x.Owner, x.Generation, rawScope, s.clock(), apply)
		}
		if err != nil {
			switch {
			case errors.Is(err, ErrAuthorityLost):
				return refuse(nativeoperation_model.ReasonAuthorityLost)
			case errors.Is(err, nativeoperation_model.ErrOperationExpired):
				return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
			case errors.Is(err, errHumanManualMergeChanged), errors.Is(err, pull_service.ErrHasMerged), errors.Is(err, pull_service.ErrIsClosed):
				return refuse(nativeoperation_model.ReasonStaleBaseOrResult)
			case errors.Is(err, pull_service.ErrManualMergeCommit):
				return refuse(nativeoperation_model.ReasonNativeRefused)
			case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
				current, readErr := nativeoperation_model.LookupOperation(settle, installation, q.OperationID)
				if readErr != nil {
					return sdk.OperationRecord{}, terminalResult{}, readErr
				}
				if current != nil && current.Revoked {
					return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
				}
			}
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if !complete {
			return ToRecord(committed), terminalResult{retain: true}, nil
		}
		if err := TestCrashBarrier(CrashPointHumanManualMergeAfterPrimary); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		// The primary is committed. Its completion consumes historical truth;
		// losing the request cannot turn it into a new merge or erase its receipt.
		completionCtx := context.WithoutCancel(owned)
		record, err := s.FinishHumanManualMerge(completionCtx, installation, q)
		if record.CompletionState == nativeoperation_model.CompletionNeedsIntervention {
			return record, terminalResult{retain: true}, nil
		}
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if err := TestCrashBarrier(CrashPointHumanManualMergeAfterFinish); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		record, err = s.CompleteHumanManualMerge(completionCtx, installation, q)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if err := TestCrashBarrier(CrashPointHumanManualMergeAfterRelease); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return record, terminalResult{}, nil
	})
	return finish(record, err)
}

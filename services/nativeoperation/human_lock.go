// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Persisted native intent and receipt.
	"errors"
	"slices"
	"strings"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"

	"github.com/google/uuid"
)

const CrashPointHumanLockBeforePrimary = "human-lock-before-primary"
const CrashPointHumanLockAfterPrimary = "human-lock-after-primary"

// HumanLockAdmission rechecks this exact request's current human/session,
// installation and lifetime before replay/claim and inside primary SQL.
type HumanLockAdmission func(context.Context) error

var (
	errHumanLockChanged = errors.New("original lock state changed")
	errHumanLockReason  = errors.New("native lock reason refused")
)

func humanLockIntent(q sdk.HumanLockRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanLockRequest(q) {
		return nil, ErrInvalidIntent
	}
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(q.Expected.ActorID), RepositoryID: nativeCommentID(q.Expected.RepositoryID), Kind: nativeoperation_model.KindHumanLock, AuthRevision: sdk.HumanLockAuthorizationRevision, ExpectedNativeRevision: q.Expected.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanLockIntent(q)
	return intent, err
}

// SubmitHumanLock uses live request admission, never background enrollment.
// Its native state/history and operation receipt are one SQL primary effect.
func (s *Service) SubmitHumanLock(ctx context.Context, installation string, q sdk.HumanLockRequest, admit HumanLockAdmission) (sdk.OperationRecord, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, err := humanLockIntent(q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if admit(ctx) != nil {
		return sdk.OperationRecord{}, ErrAuthorityLost
	}
	existing, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if existing != nil {
		return s.reconcileExisting(existing, intent)
	}
	if q.NotAfter <= s.clock() || q.NotAfter > s.clock()+int64(MaxOperationLifetime/time.Second) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: intent.RepositoryID, IssueID: nativeCommentID(q.Expected.IssueID)}
	return s.claimConditionalSubmit(ctx, extensionauth_model.SubmissionDecision{ActorID: intent.ActorID}, installation, intent, scope,
		func(ctx context.Context, op *nativeoperation_model.Operation, _ *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
			return s.executeHumanLock(ctx, op, q, admit, execution)
		})
}

func humanLockParent(ctx context.Context, q sdk.HumanLockRequest, admit HumanLockAdmission) (*issues_model.Issue, *user_model.User, error) {
	if ctx.Err() != nil || admit(ctx) != nil {
		return nil, nil, ErrAuthorityLost
	}
	p := q.Expected
	actor, err := user_model.GetUserByID(ctx, nativeCommentID(p.ActorID))
	if err != nil || !actor.IsAccessAllowed(ctx) {
		return nil, nil, ErrAuthorityLost
	}
	repo, err := repo_model.GetRepositoryByID(ctx, nativeCommentID(p.RepositoryID))
	if err != nil || repo.IsArchived {
		return nil, nil, ErrAuthorityLost
	}
	issue, err := issues_model.GetIssueByID(ctx, nativeCommentID(p.IssueID))
	if err != nil || issue.RepoID != repo.ID || issue.Index != nativeCommentID(p.Index) || issue.IsPull != (p.Subject == "pull") {
		return nil, nil, ErrAuthorityLost
	}
	if issue.IsPull {
		if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil || issue.PullRequest.ID != nativeCommentID(p.SubjectID) {
			return nil, nil, ErrAuthorityLost
		}
	}
	kind := unit.TypeIssues
	if issue.IsPull {
		kind = unit.TypePullRequests
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, actor)
	if err != nil || kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) || !permission.CanAccessAny(perm.AccessModeWrite, unit.TypeIssues, unit.TypePullRequests) {
		return nil, nil, ErrAuthorityLost
	}
	if issue.IsLocked != p.IsLocked {
		return nil, nil, errHumanLockChanged
	}
	// Native IssueLockForm.HasValidReason allows blank text or one current
	// configured reason. Preserve the submitted text instead of normalizing it.
	if q.Lock && strings.TrimSpace(q.Reason) != "" && !slices.Contains(setting.Repository.Issue.LockReasons, q.Reason) {
		return nil, nil, errHumanLockReason
	}
	issue.Repo = repo
	return issue, actor, nil
}

func (s *Service) executeHumanLock(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanLockRequest, admit HumanLockAdmission, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owned, release := execcontext.NewContext(ctx, execution), context.WithoutCancel(ctx)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		r, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(r), terminalResult{}, nil
	}
	if err := TestCrashBarrier(CrashPointHumanLockBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	committed, err := nativeoperation_model.CommitHumanLockPrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		issue, actor, err := humanLockParent(tx, q, admit)
		if err != nil {
			return "", err
		}
		if s.clock() >= op.NotAfter {
			return "", nativeoperation_model.ErrOperationExpired
		}
		if tx.Err() != nil {
			return "", ErrAuthorityLost
		}
		opts := &issues_model.IssueLockOptions{Doer: actor, Issue: issue, Reason: q.Reason}
		kind := issues_model.CommentTypeLock
		if q.Lock {
			err = issues_model.LockIssue(tx, opts)
		} else {
			kind = issues_model.CommentTypeUnlock
			err = issues_model.UnlockIssue(tx, opts)
		}
		if err != nil {
			return "", err
		}
		// The expected state is opposite to the requested state, and the held
		// owner excludes other native writers. This exact history was just added
		// by the native model inside this same transaction, never adopted by content.
		var comment issues_model.Comment
		found, err := db.GetEngine(tx).Where("issue_id=? AND type=? AND poster_id=? AND content=?", issue.ID, kind, actor.ID, q.Reason).Desc("id").Get(&comment)
		if err != nil {
			return "", err
		}
		if !found {
			return "", errors.New("native lock history missing")
		}
		sum := sha256.Sum256([]byte(q.Reason))
		raw, err := json.Marshal(sdk.HumanLockReceipt{RepositoryID: op.RepositoryID, ActorID: op.ActorID, Subject: q.Expected.Subject, SubjectID: nativeCommentID(q.Expected.SubjectID), IssueID: issue.ID, Index: issue.Index, IsLocked: issue.IsLocked, ReasonDigest: hex.EncodeToString(sum[:]), CommentID: comment.ID})
		return string(raw), err
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrAuthorityLost):
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		case errors.Is(err, errHumanLockChanged):
			return refuse(nativeoperation_model.ReasonStaleConversation)
		case errors.Is(err, errHumanLockReason):
			return refuse(nativeoperation_model.ReasonNativeRefused)
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			current, readErr := nativeoperation_model.LookupOperation(release, op.InstallationID, op.OperationID)
			if readErr != nil {
				return sdk.OperationRecord{}, terminalResult{}, readErr
			}
			if current != nil && current.Revoked {
				return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
			}
		}
		return sdk.OperationRecord{}, terminalResult{}, err // Unknown SQL/commit results retain the fence.
	}
	if err := TestCrashBarrier(CrashPointHumanLockAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	// Native lock/unlock has no separate notification step: all native effects
	// are already in the primary transaction. Finish/release can be recovered.
	r, err := nativeoperation_model.SetCompletionAndRelease(release, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, nativeoperation_model.CompletionComplete)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(r), terminalResult{}, nil
}

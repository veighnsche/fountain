// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Persisted native intent and receipt.
	"errors"

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	notify_service "forgejo.org/services/notify"
)

const CrashPointHumanStateBeforePrimary = "human-state-before-primary"
const CrashPointHumanStateAfterPrimary = "human-state-after-primary"

// HumanStateAdmission rechecks this exact request's current human/session,
// installation and lifetime before replay/claim and inside primary SQL.
type HumanStateAdmission func(context.Context) error

var (
	errHumanStateChanged      = errors.New("original state changed")
	errHumanStateDependencies = errors.New("open native dependencies")
)

func humanStateIntent(q sdk.HumanStateRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanStateRequest(q) {
		return nil, ErrInvalidIntent
	}
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(q.Expected.ActorID), RepositoryID: nativeCommentID(q.Expected.RepositoryID), Kind: nativeoperation_model.KindHumanState, AuthRevision: sdk.HumanStateAuthorizationRevision, ExpectedNativeRevision: q.Expected.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanStateIntent(q)
	return intent, err
}

// SubmitHumanState uses live request admission, never background enrollment.
// Its native state/history and operation receipt are one SQL primary effect.
func (s *Service) SubmitHumanState(ctx context.Context, installation string, q sdk.HumanStateRequest, admit HumanStateAdmission) (sdk.OperationRecord, error) {
	submission, record, err := s.beginHumanSubmission(ctx, installation, admit, func() (*ValidIntent, error) { return humanStateIntent(q) })
	if submission == nil {
		return record, err
	}
	intent := submission.intent
	scope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: intent.RepositoryID, IssueID: nativeCommentID(q.Expected.IssueID)}
	return submission.run(scope,
		func(ctx context.Context, op *nativeoperation_model.Operation, _ *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
			return s.executeHumanState(ctx, op, q, admit, execution)
		})
}

func humanStateParent(ctx context.Context, q sdk.HumanStateRequest, admit HumanStateAdmission) (*issues_model.Issue, *user_model.User, error) {
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
	if issue.IsPull && (issue.PullRequest.HasMerged != p.HasMerged || issue.PullRequest.HasMerged) {
		return nil, nil, errHumanStateChanged
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, actor)
	if err != nil {
		return nil, nil, ErrAuthorityLost
	}
	facts := issues_model.ReadDiscussionActionFacts(ctx, repo, issue, actor, permission)
	if facts.StatePermission() != issues_model.NativeActionAllowed {
		return nil, nil, ErrAuthorityLost
	}
	if issue.IsClosed != p.IsClosed {
		return nil, nil, errHumanStateChanged
	}
	if q.Close {
		blocker, err := issues_model.CheckNativeCloseDependencies(ctx, repo, issue)
		if err != nil {
			return nil, nil, err
		}
		if blocker != issues_model.NativeActionAllowed {
			return nil, nil, errHumanStateDependencies
		}
	}
	issue.Repo = repo
	return issue, actor, nil
}

func (s *Service) executeHumanState(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanStateRequest, admit HumanStateAdmission, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owned, release := execcontext.NewContext(ctx, execution), context.WithoutCancel(ctx)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		r, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(r), terminalResult{}, nil
	}
	if err := TestCrashBarrier(CrashPointHumanStateBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	var issue *issues_model.Issue
	var actor *user_model.User
	var comment *issues_model.Comment
	committed, err := nativeoperation_model.CommitHumanStatePrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		var err error
		issue, actor, err = humanStateParent(tx, q, admit)
		if err != nil {
			return "", err
		}
		if s.clock() >= op.NotAfter {
			return "", nativeoperation_model.ErrOperationExpired
		}
		if tx.Err() != nil {
			return "", ErrAuthorityLost
		}
		comment, err = issues_model.ChangeIssueStatus(tx, issue, actor, q.Close)
		if err != nil {
			return "", err
		}
		if comment == nil || comment.ID < 1 {
			return "", errors.New("native state history missing")
		}
		if tx.Err() != nil {
			return "", ErrAuthorityLost
		}
		raw, err := json.Marshal(sdk.HumanStateReceipt{RepositoryID: op.RepositoryID, ActorID: op.ActorID, Subject: q.Expected.Subject, SubjectID: nativeCommentID(q.Expected.SubjectID), IssueID: issue.ID, Index: issue.Index, IsClosed: issue.IsClosed, ClosedUnix: int64(issue.ClosedUnix), CommentID: comment.ID})
		return string(raw), err
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrAuthorityLost):
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		case errors.Is(err, errHumanStateChanged), issues_model.IsErrIssueWasClosed(err), issues_model.IsErrPullWasClosed(err):
			return refuse(nativeoperation_model.ReasonStaleConversation)
		case errors.Is(err, errHumanStateDependencies), issues_model.IsErrDependenciesLeft(err):
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
	if err := TestCrashBarrier(CrashPointHumanStateAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	// Match native successful state completion after the atomic primary. Never
	// replay partially finished stopwatch/notification work after a lost process.
	completion := nativeoperation_model.CompletionComplete
	if owned.Err() != nil {
		completion = nativeoperation_model.CompletionNeedsIntervention
	}
	if completion == nativeoperation_model.CompletionComplete && q.Close {
		if err := issues_model.FinishIssueStopwatchIfPossible(owned, actor, issue); err != nil {
			completion = nativeoperation_model.CompletionNeedsIntervention
		}
	}
	if completion == nativeoperation_model.CompletionComplete {
		notify_service.IssueChangeStatus(owned, actor, "", issue, comment, q.Close)
	}
	r, err := nativeoperation_model.SetCompletionAndRelease(release, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(r), terminalResult{}, nil
}

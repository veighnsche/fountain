// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Persisted native intent and receipt.
	"errors"
	"strconv"
	"time"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	notify_service "forgejo.org/services/notify"

	"github.com/google/uuid"
)

const CrashPointHumanCommentBeforePrimary = "human-comment-before-primary"
const CrashPointHumanCommentAfterPrimary = "human-comment-after-primary"

// HumanCommentAdmission is supplied by trusted native request composition. It
// must revalidate the current human/session, installation and request lifetime
// for this exact request; a retained snapshot or background token cannot do so.
// It runs both before replay/claim and inside the primary SQL transaction.
type HumanCommentAdmission func(context.Context) error

func nativeCommentID(value string) int64 {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != value {
		return 0
	}
	return id
}

func humanCommentIntent(q sdk.HumanCommentRequest) (*ValidIntent, error) {
	p := q.Expected
	if !sdk.ValidHumanCommentRequest(q) {
		return nil, ErrInvalidIntent
	}
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(p.ActorID), RepositoryID: nativeCommentID(p.RepositoryID),
		Kind: nativeoperation_model.KindHumanComment, AuthRevision: sdk.HumanCommentAuthorizationRevision, ExpectedNativeRevision: p.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanCommentIntent(q)
	return intent, err
}

// SubmitHumanComment uses the existing conditional journal, with no background
// credential or enrollment. The native caller derives installation/actor from
// its live admission. This service is not a public route or a background kind.
func (s *Service) SubmitHumanComment(ctx context.Context, installation string, q sdk.HumanCommentRequest, admit HumanCommentAdmission) (sdk.OperationRecord, error) {
	installationID, err := uuid.Parse(installation)
	if err != nil || installationID.String() != installation || admit == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, err := humanCommentIntent(q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if ctx.Err() != nil {
		return sdk.OperationRecord{}, ctx.Err()
	}
	if err := admit(ctx); err != nil {
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
			return s.executeHumanComment(ctx, op, q, admit, execution)
		})
}

// Recheck live DB facts under the held owner, independently of the snapshot's
// action flag. The admission callback separately checks the live request human.
func humanCommentParent(ctx context.Context, q sdk.HumanCommentRequest, admit HumanCommentAdmission) (*issues_model.Issue, *user_model.User, error) {
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
	permission, err := access_model.GetUserRepoPermission(ctx, repo, actor)
	if err != nil {
		return nil, nil, ErrAuthorityLost
	}
	facts := issues_model.ReadDiscussionActionFacts(ctx, repo, issue, actor, permission)
	if facts.Comment() != issues_model.NativeActionAllowed {
		return nil, nil, ErrAuthorityLost
	}
	issue.Repo = repo
	return issue, actor, nil
}

func (s *Service) executeHumanComment(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanCommentRequest, admit HumanCommentAdmission, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owned := execcontext.NewContext(ctx, execution)
	release := context.WithoutCancel(ctx)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		record, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(record), terminalResult{}, err
	}
	if err := TestCrashBarrier(CrashPointHumanCommentBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	var issue *issues_model.Issue
	var actor *user_model.User
	var comment *issues_model.Comment
	committed, err := nativeoperation_model.CommitHumanCommentPrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		var err error
		issue, actor, err = humanCommentParent(tx, q, admit)
		if err != nil {
			return "", err
		}
		if s.clock() >= op.NotAfter {
			return "", nativeoperation_model.ErrOperationExpired
		}
		if err := tx.Err(); err != nil {
			return "", ErrAuthorityLost
		}
		comment, err = issues_model.CreateComment(tx, &issues_model.CreateCommentOptions{Type: issues_model.CommentTypeComment, Doer: actor, Repo: issue.Repo, Issue: issue, Content: q.Body})
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(sdk.HumanCommentReceipt{RepositoryID: op.RepositoryID, ActorID: op.ActorID, Subject: q.Expected.Subject,
			SubjectID: nativeCommentID(q.Expected.SubjectID), IssueID: issue.ID, Index: issue.Index, CommentID: comment.ID})
		return string(raw), err
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrAuthorityLost):
			return refuse(nativeoperation_model.ReasonAuthorityLost)
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
		// Unknown SQL/commit errors retain the fence; never retry an uncertain
		// primary or overwrite a receipt which may have committed.
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if err := TestCrashBarrier(CrashPointHumanCommentAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	completion := nativeoperation_model.CompletionComplete
	mentions, err := issues_model.FindAndUpdateIssueMentions(owned, issue, actor, comment.Content)
	if err != nil {
		completion = nativeoperation_model.CompletionNeedsIntervention
	} else {
		notify_service.CreateIssueComment(owned, actor, issue.Repo, issue, comment, mentions)
	}
	record, err := nativeoperation_model.SetCompletionAndRelease(release, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(record), terminalResult{}, err
}

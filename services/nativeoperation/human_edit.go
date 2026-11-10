// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Persisted native intent and receipt.
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	issue_service "forgejo.org/services/issue"
	notify_service "forgejo.org/services/notify"

	"github.com/google/uuid"
)

const CrashPointHumanEditBeforePrimary = "human-edit-before-primary"
const CrashPointHumanEditAfterPrimary = "human-edit-after-primary"

// HumanEditAdmission revalidates this request's current human, installation and
// lifetime before claim/replay and again inside the primary SQL transaction.
type HumanEditAdmission func(context.Context) error

var errHumanEditChanged = errors.New("original issue edit facts changed")

func editBodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func humanEditIntent(q sdk.HumanEditRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanEditRequest(q) {
		return nil, ErrInvalidIntent
	}
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(q.Expected.ActorID), RepositoryID: nativeCommentID(q.Expected.RepositoryID),
		Kind: nativeoperation_model.KindHumanEdit, AuthRevision: sdk.HumanEditAuthorizationRevision, ExpectedNativeRevision: q.Expected.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanEditIntent(q)
	return intent, err
}

// SubmitHumanEdit uses the existing native journal and reservation, without a
// background credential or enrollment. Only requested fields are authorized.
func (s *Service) SubmitHumanEdit(ctx context.Context, installation string, q sdk.HumanEditRequest, admit HumanEditAdmission) (sdk.OperationRecord, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, err := humanEditIntent(q)
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
			return s.executeHumanEdit(ctx, op, q, admit, execution)
		})
}

func humanEditParent(ctx context.Context, q sdk.HumanEditRequest, admit HumanEditAdmission) (*issues_model.Issue, *user_model.User, error) {
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
	if !facts.Readable || q.Title != nil && facts.TitleEdit() != issues_model.NativeActionAllowed ||
		q.Body != nil && facts.BodyEdit() != issues_model.NativeActionAllowed {
		return nil, nil, ErrAuthorityLost
	}
	if issue.Title != p.Title || editBodyDigest(issue.Content) != p.BodyDigest || int64(issue.ContentVersion) != p.ContentVersion {
		return nil, nil, errHumanEditChanged
	}
	issue.Repo = repo
	return issue, actor, nil
}

func (s *Service) executeHumanEdit(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanEditRequest, admit HumanEditAdmission, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owned, release := execcontext.NewContext(ctx, execution), context.WithoutCancel(ctx)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		r, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(r), terminalResult{}, err
	}
	if err := TestCrashBarrier(CrashPointHumanEditBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	var issue *issues_model.Issue
	var actor *user_model.User
	var oldTitle, oldBody string
	var titleChanged, bodyChanged bool
	committed, err := nativeoperation_model.CommitHumanEditPrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		var err error
		issue, actor, err = humanEditParent(tx, q, admit)
		if err != nil {
			return "", err
		}
		if s.clock() >= op.NotAfter {
			return "", nativeoperation_model.ErrOperationExpired
		}
		oldTitle, oldBody = issue.Title, issue.Content
		titleChanged, bodyChanged = q.Title != nil && *q.Title != oldTitle, q.Body != nil && *q.Body != oldBody
		var titleCommentID int64
		if titleChanged {
			if err := issues_model.IfNeededCreateShadowCopyForIssue(tx, issue); err != nil {
				return "", err
			}
			issue.Title = *q.Title
			if err := issues_model.ChangeIssueTitle(tx, issue, actor, oldTitle); err != nil {
				return "", err
			}
			var comment issues_model.Comment
			found, err := db.GetEngine(tx).Where("issue_id=? AND type=? AND poster_id=? AND old_title=? AND new_title=?", issue.ID, issues_model.CommentTypeChangeTitle, actor.ID, oldTitle, issue.Title).Desc("id").Get(&comment)
			if err != nil {
				return "", err
			}
			if !found {
				return "", errors.New("native title history missing")
			}
			titleCommentID = comment.ID
		}
		if bodyChanged {
			if err := issues_model.ChangeIssueContent(tx, issue, actor, *q.Body, issue.ContentVersion); err != nil {
				return "", err
			}
		}
		if err := tx.Err(); err != nil {
			return "", ErrAuthorityLost
		}
		raw, err := json.Marshal(sdk.HumanEditReceipt{RepositoryID: op.RepositoryID, ActorID: op.ActorID, Subject: q.Expected.Subject, SubjectID: nativeCommentID(q.Expected.SubjectID), IssueID: issue.ID, Index: issue.Index,
			Title: issue.Title, BodyDigest: editBodyDigest(issue.Content), ContentVersion: int64(issue.ContentVersion), TitleCommentID: titleCommentID})
		return string(raw), err
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrAuthorityLost):
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		case errors.Is(err, errHumanEditChanged), errors.Is(err, issues_model.ErrIssueAlreadyChanged):
			return refuse(nativeoperation_model.ReasonStaleConversation)
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
		return sdk.OperationRecord{}, terminalResult{}, err // Uncertain SQL errors retain the fence.
	}
	if err := TestCrashBarrier(CrashPointHumanEditAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	completion := nativeoperation_model.CompletionComplete
	if titleChanged {
		if err := issue_service.CompleteTitleChange(owned, issue, actor, oldTitle); err != nil {
			completion = nativeoperation_model.CompletionNeedsIntervention
		}
	}
	if bodyChanged {
		notify_service.IssueChangeContent(owned, actor, issue, oldBody)
	}
	r, err := nativeoperation_model.SetCompletionAndRelease(release, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(r), terminalResult{}, err
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Exact persisted intent and native receipt.
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"

	"github.com/google/uuid"
)

const CrashPointHumanReviewBeforePrimary = "human-review-before-primary"
const CrashPointHumanReviewAfterPrimary = "human-review-after-primary"

// HumanReviewAdmission rechecks this exact review/inline/thread/reviewer request's current human/session,
// installation and lifetime before claim/replay and inside atomic primary SQL.
type HumanReviewAdmission func(context.Context) error

func humanReviewIntent(q sdk.HumanReviewRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanReviewRequest(q) {
		return nil, ErrInvalidIntent
	}
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(q.Expected.ActorID), RepositoryID: nativeCommentID(q.Expected.RepositoryID), Kind: nativeoperation_model.KindHumanReview, AuthRevision: sdk.HumanReviewAuthorizationRevision, ExpectedNativeRevision: q.Expected.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanReviewIntent(q)
	return intent, err
}

func humanReviewType(event string) issues_model.ReviewType {
	switch event {
	case sdk.HumanReviewEventDraft:
		return issues_model.ReviewTypePending
	case sdk.HumanReviewEventComment:
		return issues_model.ReviewTypeComment
	case sdk.ReviewSubmitEventApproved:
		return issues_model.ReviewTypeApprove
	case sdk.ReviewSubmitEventRequestChanges:
		return issues_model.ReviewTypeReject
	default:
		return issues_model.ReviewTypeUnknown
	}
}

// SubmitHumanReview composes the prepared native primary with the existing
// conditional journal/reservation, without background enrollment or credentials.
func (s *Service) SubmitHumanReview(ctx context.Context, installation string, q sdk.HumanReviewRequest, admit HumanReviewAdmission) (sdk.OperationRecord, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, err := humanReviewIntent(q)
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
	return s.claimConditionalSubmit(ctx, extensionauth_model.SubmissionDecision{ActorID: intent.ActorID}, installation, intent, scope, func(ctx context.Context, op *nativeoperation_model.Operation, _ *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
		return s.executeHumanReview(ctx, op, q, admit, execution)
	})
}

// A bounded composite digest retains all existing own-draft code evidence
// without clipping a large draft's receipt or exposing its text to clients.
func humanReviewCommentsEvidence(ctx context.Context, review *issues_model.Review) (int64, string, error) {
	var comments []*issues_model.Comment
	if err := db.GetEngine(ctx).Where("review_id=? AND type=?", review.ID, issues_model.CommentTypeCode).OrderBy("id ASC").Find(&comments); err != nil {
		return 0, "", err
	}
	type evidence struct {
		ID, IssueID, ReviewID, ActorID, OriginalAuthorID int64
		ResolveDoerID, ContentVersion, Created, Updated  int64
		Content, Path, Commit, Patch                     string
		OriginalAuthor                                   string
		Line                                             int64
		Anchor                                           *issues_model.CodeCommentAnchor
		Invalidated                                      bool
	}
	rows := make([]evidence, 0, len(comments))
	for _, c := range comments {
		if c.IssueID != review.IssueID || c.ReviewID != review.ID || c.PosterID != review.ReviewerID || c.OriginalAuthorID != 0 {
			return 0, "", issues_model.ErrExactReviewRefused{Reason: "pending draft comment identity differs"}
		}
		rows = append(rows, evidence{ID: c.ID, IssueID: c.IssueID, ReviewID: c.ReviewID, ActorID: c.PosterID, OriginalAuthorID: c.OriginalAuthorID, ResolveDoerID: c.ResolveDoerID, ContentVersion: int64(c.ContentVersion), Created: int64(c.CreatedUnix), Updated: int64(c.UpdatedUnix), Content: c.Content, Path: c.TreePath, Commit: c.CommitSHA, Patch: c.Patch, OriginalAuthor: c.OriginalAuthor, Line: c.Line, Anchor: c.OriginalAnchor, Invalidated: c.Invalidated})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return 0, "", err
	}
	return int64(len(rows)), sdk.HumanReviewBodyDigest(string(raw)), nil
}

func (s *Service) executeHumanReview(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanReviewRequest, admit HumanReviewAdmission, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owned, release := execcontext.NewContext(ctx, execution), context.WithoutCancel(ctx)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		r, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(r), terminalResult{}, nil
	}
	if err := TestCrashBarrier(CrashPointHumanReviewBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	var review *issues_model.Review
	var comment *issues_model.Comment
	var actor *user_model.User
	committed, err := nativeoperation_model.CommitHumanReviewPrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		if tx.Err() != nil || admit(tx) != nil {
			return "", ErrAuthorityLost
		}
		if s.clock() >= op.NotAfter {
			return "", nativeoperation_model.ErrOperationExpired
		}
		p := q.Expected
		issue, err := issues_model.GetIssueByID(tx, nativeCommentID(p.IssueID))
		if err != nil {
			return "", ErrAuthorityLost
		}
		if issue.RepoID != op.RepositoryID || issue.Index != nativeCommentID(p.Index) || !issue.IsPull {
			return "", ErrAuthorityLost
		}
		if err := issue.LoadPullRequest(tx); err != nil || issue.PullRequest == nil {
			return "", ErrAuthorityLost
		}
		if err := issue.LoadRepo(tx); err != nil {
			return "", err
		}
		actor, err = user_model.GetUserByID(tx, op.ActorID)
		if err != nil || !actor.IsAccessAllowed(tx) {
			return "", ErrAuthorityLost
		}
		native, err := git.OpenRepository(tx, issue.Repo.RepoPath())
		if err != nil {
			return "", err
		}
		defer native.Close()
		head, err := native.GetRefCommitID(issue.PullRequest.GetGitRefName())
		if err != nil {
			return "", issues_model.ErrExactReviewRefused{Reason: "head changed"}
		}
		base, err := native.GetRefCommitID(git.BranchPrefix + p.BaseBranch)
		if err != nil {
			return "", issues_model.ErrExactReviewRefused{Reason: "base changed"}
		}
		mergeBase, err := native.GetMergeBaseSimple(base, head)
		if err != nil {
			return "", issues_model.ErrExactReviewRefused{Reason: "merge base changed"}
		}
		want := issues_model.PreparedHumanReviewTarget{RepositoryID: op.RepositoryID, ReviewerID: op.ActorID, PullID: nativeCommentID(p.PullID), IssueID: issue.ID, Index: issue.Index, BaseBranch: p.BaseBranch, IsClosed: p.IsClosed, HasMerged: p.HasMerged, IsAuthor: p.IsAuthor, HeadOID: p.HeadOID, BaseOID: p.BaseOID, MergeBaseOID: p.MergeBaseOID, CurrentHeadOID: head, CurrentBaseOID: base, CurrentMergeBaseOID: mergeBase, PendingReviewID: nativeCommentID(p.PendingReviewID), PendingCommitID: p.PendingCommitID}
		review, comment, err = issues_model.SubmitPreparedHumanReview(tx, humanReviewType(q.Event), q.Body, want)
		if err != nil {
			return "", err
		}
		count, digest, err := humanReviewCommentsEvidence(tx, review)
		if err != nil {
			return "", err
		}
		r := sdk.HumanReviewReceipt{RepositoryID: op.RepositoryID, ActorID: op.ActorID, PullID: want.PullID, IssueID: want.IssueID, Index: want.Index, ReviewID: review.ID, Event: q.Event, CommitID: review.CommitID, Official: review.Official, Stale: review.Stale, BodyDigest: sdk.HumanReviewBodyDigest(q.Body), DraftCommentsCount: count, DraftCommentsDigest: digest}
		if comment != nil {
			r.CommentID = comment.ID
		}
		if !sdk.ValidHumanReviewReceipt(r, q) {
			return "", errors.New("native human review receipt differs")
		}
		if tx.Err() != nil {
			return "", ErrAuthorityLost
		}
		raw, err := json.Marshal(r)
		return string(raw), err
	})
	if err != nil {
		var deterministic issues_model.ErrExactReviewRefused
		switch {
		case errors.Is(err, ErrAuthorityLost):
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		case errors.As(err, &deterministic):
			reason := nativeoperation_model.ReasonNativeRefused
			switch deterministic.Reason {
			case "head changed":
				reason = nativeoperation_model.ReasonStaleHead
			case "base changed", "merge base changed":
				reason = nativeoperation_model.ReasonStaleBaseOrResult
			case "pending draft changed":
				reason = nativeoperation_model.ReasonPendingReviewExists
			case "reviewer is inactive", "reviewer cannot read this pull request", "reviewer is blocked", "review workflow unavailable", "cannot review your own pull request":
				reason = nativeoperation_model.ReasonAuthorityLost
			case "human review parent changed", "human review pull changed":
				reason = nativeoperation_model.ReasonPRMismatch
			}
			return refuse(reason)
		case issues_model.IsContentEmptyErr(err):
			return refuse(nativeoperation_model.ReasonNativeRefused)
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			current, e := nativeoperation_model.LookupOperation(release, op.InstallationID, op.OperationID)
			if e != nil {
				return sdk.OperationRecord{}, terminalResult{}, e
			}
			if current != nil && current.Revoked {
				return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
			}
		}
		return sdk.OperationRecord{}, terminalResult{}, err // Unknown commit results retain ownership for evidence recovery.
	}
	if err := TestCrashBarrier(CrashPointHumanReviewAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	completion := nativeoperation_model.CompletionComplete
	if q.Event != sdk.HumanReviewEventDraft {
		if owned.Err() != nil {
			completion = nativeoperation_model.CompletionNeedsIntervention
		} else if err := pull_service.CompleteReviewSubmission(owned, actor, review.Issue, review, comment); err != nil {
			completion = nativeoperation_model.CompletionNeedsIntervention
		}
	}
	r, err := nativeoperation_model.SetCompletionAndRelease(release, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(r), terminalResult{}, nil
}

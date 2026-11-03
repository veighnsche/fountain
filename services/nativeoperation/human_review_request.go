// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/organization"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	issue_service "forgejo.org/services/issue"

	"github.com/google/uuid"
)

const CrashPointHumanReviewRequestBeforePrimary = "human-review-request-before-primary"
const CrashPointHumanReviewRequestAfterPrimary = "human-review-request-after-primary"

func humanReviewRequestIntent(q sdk.HumanReviewRequestChange) (*ValidIntent, error) {
	if !sdk.ValidHumanReviewRequestChange(q) {
		return nil, ErrInvalidIntent
	}
	p := q.Expected.Review
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(p.ActorID), RepositoryID: nativeCommentID(p.RepositoryID), Kind: nativeoperation_model.KindHumanReviewRequest, AuthRevision: sdk.HumanReviewRequestAuthorizationRevision, ExpectedNativeRevision: p.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanReviewRequestIntent(q)
	return intent, err
}

// SubmitHumanReviewRequest uses current human request admission, never background
// enrollment. Request rows, native history and exact receipt commit together.
// Replays and early cancellation use the existing installation-bound journal.
func (s *Service) SubmitHumanReviewRequest(ctx context.Context, installation string, q sdk.HumanReviewRequestChange, admit HumanReviewAdmission) (sdk.OperationRecord, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, err := humanReviewRequestIntent(q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if ctx.Err() != nil {
		return sdk.OperationRecord{}, ctx.Err()
	}
	if admit(ctx) != nil {
		return sdk.OperationRecord{}, ErrAuthorityLost
	}
	finish := func(r sdk.OperationRecord, err error) (sdk.OperationRecord, error) {
		if err == nil || errors.Is(err, ErrCancelledBeforeSubmit) {
			if ctx.Err() != nil || admit(ctx) != nil {
				return sdk.OperationRecord{}, ErrAuthorityLost
			}
		}
		return r, err
	}
	existing, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if existing != nil {
		r, err := s.reconcileExisting(existing, intent)
		return finish(r, err)
	}
	if q.NotAfter <= s.clock() || q.NotAfter > s.clock()+int64(MaxOperationLifetime/time.Second) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: intent.RepositoryID, IssueID: nativeCommentID(q.Expected.Review.IssueID)}
	record, err := s.claimConditionalSubmit(ctx, extensionauth_model.SubmissionDecision{ActorID: intent.ActorID}, installation, intent, scope, func(ctx context.Context, op *nativeoperation_model.Operation, _ *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
		return s.executeHumanReviewRequest(ctx, op, q, admit, execution)
	})
	return finish(record, err)
}

func (s *Service) executeHumanReviewRequest(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanReviewRequestChange, admit HumanReviewAdmission, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owned, release := execcontext.NewContext(ctx, execution), context.WithoutCancel(ctx)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		r, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(r), terminalResult{}, nil
	}
	if err := TestCrashBarrier(CrashPointHumanReviewRequestBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	var receipt sdk.HumanReviewRequestReceipt
	committed, err := nativeoperation_model.CommitHumanReviewRequestPrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		gate := func(ctx context.Context) error {
			if s.clock() >= op.NotAfter {
				return nativeoperation_model.ErrOperationExpired
			}
			return admit(ctx)
		}
		result, err := ApplyPreparedHumanReviewRequest(tx, op.ActorID, q.Expected, q.Requested, gate)
		if err != nil {
			return "", err
		}
		p := q.Expected.Review
		receipt = sdk.HumanReviewRequestReceipt{RepositoryID: op.RepositoryID, ActorID: op.ActorID, PullID: nativeCommentID(p.PullID), IssueID: nativeCommentID(p.IssueID), Index: nativeCommentID(p.Index), ReviewerKind: q.Expected.ReviewerKind, ReviewerID: nativeCommentID(q.Expected.ReviewerID), Before: result.Before, After: result.After, Changed: result.Changed, HistoryIDs: result.HistoryIDs, ReviewsBeforeDigest: result.ReviewsBeforeDigest, ReviewsAfterDigest: result.ReviewsAfterDigest}
		receipt.HistoryDigest, err = humanReviewRequestHistoryDigest(tx, receipt, q.Requested)
		if err != nil {
			return "", err
		}
		if !sdk.ValidHumanReviewRequestReceipt(receipt, q) {
			return "", errors.New("native human reviewer request receipt differs")
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return "", err
		}
		if s.clock() >= op.NotAfter {
			return "", nativeoperation_model.ErrOperationExpired
		}
		return string(raw), nil
	})
	if err != nil {
		var deterministic issues_model.ErrExactReviewRefused
		switch {
		case errors.Is(err, ErrAuthorityLost):
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, ErrHumanReviewRequestMissing), errors.Is(err, ErrHumanReviewRequestTooLarge):
			return refuse(nativeoperation_model.ReasonStaleConversation)
		case errors.As(err, &deterministic):
			reason := nativeoperation_model.ReasonNativeRefused
			switch deterministic.Reason {
			case "reviewer head changed":
				reason = nativeoperation_model.ReasonStaleHead
			case "reviewer base changed", "reviewer merge base changed":
				reason = nativeoperation_model.ReasonStaleBaseOrResult
			case "reviewer policy or selected requests changed", "reviewer history changed":
				reason = nativeoperation_model.ReasonStaleConversation
			case "reviewer actor is inactive", "reviewer workflow unavailable", "reviewer actor cannot read pull":
				reason = nativeoperation_model.ReasonAuthorityLost
			case "reviewer parent changed", "reviewer pull changed":
				reason = nativeoperation_model.ReasonPRMismatch
			case "reviewer primary readback differs", "reviewer history differs from native effect", "created reviewer request differs", "removed reviewer history differs", "reviewer primary did not record expected history", "invalid persisted reviewer history":
				return sdk.OperationRecord{}, terminalResult{}, err // Unexpected SQL effects retain the fence after rollback.
			}
			return refuse(reason)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			current, e := nativeoperation_model.LookupOperation(release, op.InstallationID, op.OperationID)
			if e != nil {
				return sdk.OperationRecord{}, terminalResult{}, e
			}
			if current != nil && current.Revoked {
				return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
			}
		}
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if err := TestCrashBarrier(CrashPointHumanReviewRequestAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	completion := nativeoperation_model.CompletionComplete
	if humanReviewRequestNeedsCompletion(receipt, q.Requested) {
		if err := completeHumanReviewRequest(owned, receipt, q.Requested); err != nil {
			completion = nativeoperation_model.CompletionNeedsIntervention
		}
	}
	r, err := nativeoperation_model.SetCompletionAndRelease(release, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(r), terminalResult{}, nil
}

func humanReviewRequestNeedsCompletion(r sdk.HumanReviewRequestReceipt, requested bool) bool {
	return r.Changed && (r.ReviewerKind == "user" && r.ActorID != r.ReviewerID || r.ReviewerKind == "team" && requested)
}

func completeHumanReviewRequest(ctx context.Context, r sdk.HumanReviewRequestReceipt, requested bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	actor, err := user_model.GetUserByID(ctx, r.ActorID)
	if err != nil {
		return err
	}
	issue, err := issues_model.GetIssueByID(ctx, r.IssueID)
	if err != nil {
		return err
	}
	if err := issue.LoadRepo(ctx); err != nil {
		return err
	}
	if err := issue.LoadPoster(ctx); err != nil {
		return err
	}
	if err := issue.LoadPullRequest(ctx); err != nil {
		return err
	}
	var reviewer *user_model.User
	var team *organization.Team
	if r.ReviewerKind == "user" {
		reviewer, err = user_model.GetUserByID(ctx, r.ReviewerID)
	} else {
		team, err = organization.GetTeamByID(ctx, r.ReviewerID)
	}
	if err != nil {
		return err
	}
	for _, id := range r.HistoryIDs {
		comment, err := issues_model.GetCommentByID(ctx, nativeCommentID(id))
		if err != nil {
			return err
		}
		if err := issue_service.CompleteReviewRequestChange(ctx, issue, actor, reviewer, team, requested, comment); err != nil {
			return err
		}
	}
	return nil
}

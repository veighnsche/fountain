// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Persisted exact native intent and receipt.

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
)

// Offline recovery attributes exact committed SQL evidence. It never resubmits
// review primary, adopts similar prose or repeats uncertain notifications.
func (s *Service) recoverHumanReview(ctx context.Context, a *RecoveryAssessment, r *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanReviewRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human review intent is missing")
	}
	q := saved.Payload
	intent, err := humanReviewIntent(q)
	if err != nil || intent.Canonical != op.Intent || intent.Digest != op.IntentDigest || intent.OperationID != op.OperationID || intent.ActorID != op.ActorID || intent.RepositoryID != op.RepositoryID || intent.AuthRevision != op.AuthRevision || intent.ExpectedNativeRevision != op.ExpectedNativeRevision || intent.NotAfter != op.NotAfter || scope.RepositoryID != op.RepositoryID || scope.IssueID != nativeCommentID(q.Expected.IssueID) || op.TokenID != 0 || op.CredentialFingerprint != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human review scope and intent differ")
	}
	if !op.Admitted && op.EffectState == nativeoperation_model.EffectPending {
		if _, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonRecoveredNoEffect, Cancellation: nativeoperation_model.CancellationNone}, ""); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectNotCommitted)
	}
	if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human review has no atomic committed receipt")
	}
	var receipt sdk.HumanReviewReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || !sdk.ValidHumanReviewReceipt(receipt, q) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human review receipt differs from intent")
	}
	issue, err := issues_model.GetIssueByID(ctx, receipt.IssueID)
	if err != nil || issue.RepoID != receipt.RepositoryID || issue.Index != receipt.Index || !issue.IsPull {
		return fenced(a, ReasonRecoveryMissingEvidence, "human review parent differs")
	}
	if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil || issue.PullRequest.ID != receipt.PullID || issue.PullRequest.BaseRepoID != receipt.RepositoryID {
		return fenced(a, ReasonRecoveryUncertainEffect, "human review pull identity differs")
	}
	review, err := issues_model.GetReviewByID(ctx, receipt.ReviewID)
	if err != nil || review.IssueID != receipt.IssueID || review.ReviewerID != receipt.ActorID || review.ReviewerTeamID != 0 || review.OriginalAuthorID != 0 || review.Type != humanReviewType(q.Event) || review.Content != q.Body || review.CommitID != receipt.CommitID || review.Official != receipt.Official || review.Stale != receipt.Stale || review.Dismissed {
		return fenced(a, ReasonRecoveryMissingEvidence, "human review row differs")
	}
	count, digest, err := humanReviewCommentsEvidence(ctx, review)
	if err != nil || count != receipt.DraftCommentsCount || digest != receipt.DraftCommentsDigest {
		return fenced(a, ReasonRecoveryMissingEvidence, "human review draft comment evidence differs")
	}
	if q.Event != sdk.HumanReviewEventDraft {
		comment, err := issues_model.GetCommentByID(ctx, receipt.CommentID)
		if err != nil || comment.IssueID != receipt.IssueID || comment.ReviewID != receipt.ReviewID || comment.PosterID != receipt.ActorID || comment.OriginalAuthorID != 0 || comment.Type != issues_model.CommentTypeReview || comment.Content != q.Body {
			return fenced(a, ReasonRecoveryMissingEvidence, "human review history differs")
		}
	}
	if op.Completion == nativeoperation_model.CompletionComplete || op.Completion == nativeoperation_model.CompletionNeedsIntervention {
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectCommitted)
	}
	if op.Completion != nativeoperation_model.CompletionPending && op.Completion != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human review completion differs")
	}
	completion := nativeoperation_model.CompletionNeedsIntervention
	if q.Event == sdk.HumanReviewEventDraft {
		completion = nativeoperation_model.CompletionComplete
	}
	if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, r.Owner, r.Generation, completion); err != nil {
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
	a.Checks = append(a.Checks, "atomic human review/history and exact draft evidence match; uncertain final completion is not replayed")
	s.retireStaleCapabilities()
	return *a, nil
}

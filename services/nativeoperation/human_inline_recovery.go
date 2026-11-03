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

// Recovery attributes exact SQL evidence under the stopped writer's fence. It
// never creates another inline, retargets context or retries notifications.
func (s *Service) recoverHumanInline(ctx context.Context, a *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanInlineRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human inline intent is missing")
	}
	q := saved.Payload
	intent, err := humanInlineIntent(q)
	if err != nil || intent.Canonical != op.Intent || intent.Digest != op.IntentDigest || intent.OperationID != op.OperationID || intent.ActorID != op.ActorID || intent.RepositoryID != op.RepositoryID || intent.AuthRevision != op.AuthRevision || intent.ExpectedNativeRevision != op.ExpectedNativeRevision || intent.NotAfter != op.NotAfter || scope.RepositoryID != op.RepositoryID || scope.IssueID != nativeCommentID(q.Expected.Review.IssueID) || op.TokenID != 0 || op.CredentialFingerprint != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human inline scope and intent differ")
	}
	if !op.Admitted && op.EffectState == nativeoperation_model.EffectPending {
		if _, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonRecoveredNoEffect, Cancellation: nativeoperation_model.CancellationNone}, ""); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, reservation, nativeoperation_model.EffectNotCommitted)
	}
	if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human inline has no atomic committed receipt")
	}
	var receipt sdk.HumanInlineReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || !sdk.ValidHumanInlineReceipt(receipt, q) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human inline receipt differs from intent")
	}
	issue, err := issues_model.GetIssueByID(ctx, receipt.IssueID)
	if err != nil || issue.RepoID != receipt.RepositoryID || issue.Index != receipt.Index || !issue.IsPull {
		return fenced(a, ReasonRecoveryMissingEvidence, "human inline parent differs")
	}
	if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil || issue.PullRequest.ID != receipt.PullID || issue.PullRequest.BaseRepoID != receipt.RepositoryID {
		return fenced(a, ReasonRecoveryUncertainEffect, "human inline pull identity differs")
	}
	typ := issues_model.ReviewTypeComment
	if q.Draft {
		typ = issues_model.ReviewTypePending
	}
	review, err := issues_model.GetReviewByID(ctx, receipt.ReviewID)
	if err != nil || review.IssueID != receipt.IssueID || review.ReviewerID != receipt.ActorID || review.ReviewerTeamID != 0 || review.OriginalAuthorID != 0 || review.Type != typ || sdk.HumanReviewBodyDigest(review.Content) != receipt.ReviewDigest || review.CommitID != receipt.CommitID || review.Official != receipt.Official || review.Stale != receipt.Stale || review.Dismissed {
		return fenced(a, ReasonRecoveryMissingEvidence, "human inline review differs")
	}
	inline, err := issues_model.GetCommentByID(ctx, receipt.CommentID)
	anchor := receipt.Anchor
	expected := &issues_model.CodeCommentAnchor{Base: anchor.Base, Head: anchor.Head, Path: anchor.Path, Side: anchor.Side, Scope: anchor.Scope, StartLine: anchor.StartLine, EndLine: anchor.EndLine}
	line, commit := anchor.EndLine, anchor.Head
	if anchor.Side == "old" {
		line, commit = -line, anchor.Base
	}
	if err != nil || inline.Type != issues_model.CommentTypeCode || inline.IssueID != receipt.IssueID || inline.ReviewID != receipt.ReviewID || inline.PosterID != receipt.ActorID || inline.OriginalAuthorID != 0 || inline.Content != q.Body || inline.TreePath != anchor.Path || inline.Line != line || inline.CommitSHA != commit || !inline.OriginalAnchor.Equal(expected) || inline.Invalidated || inline.Patch != "" {
		return fenced(a, ReasonRecoveryMissingEvidence, "human inline row or original context differs")
	}
	count, digest, err := humanReviewCommentsEvidence(ctx, review)
	if err != nil || count != receipt.CommentsCount || digest != receipt.CommentsDigest {
		return fenced(a, ReasonRecoveryMissingEvidence, "human inline complete draft evidence differs")
	}
	if !q.Draft {
		history, err := issues_model.GetCommentByID(ctx, receipt.HistoryID)
		if err != nil || history.Type != issues_model.CommentTypeReview || history.IssueID != receipt.IssueID || history.ReviewID != receipt.ReviewID || history.PosterID != receipt.ActorID || history.OriginalAuthorID != 0 || history.Content != review.Content {
			return fenced(a, ReasonRecoveryMissingEvidence, "human inline review history differs")
		}
	}
	if op.Completion == nativeoperation_model.CompletionComplete || op.Completion == nativeoperation_model.CompletionNeedsIntervention {
		return s.releaseRecovered(ctx, a, reservation, nativeoperation_model.EffectCommitted)
	}
	if op.Completion != nativeoperation_model.CompletionPending && op.Completion != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human inline completion differs")
	}
	completion := nativeoperation_model.CompletionNeedsIntervention
	if q.Draft {
		completion = nativeoperation_model.CompletionComplete
	}
	if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, reservation.Owner, reservation.Generation, completion); err != nil {
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
	a.Checks = append(a.Checks, "atomic inline/review/history and full original draft evidence match; uncertain final completion is not replayed")
	s.retireStaleCapabilities()
	return *a, nil
}

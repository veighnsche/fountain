// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Persisted native intent and receipt.

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
)

// SQL comment and receipt commit together. Offline recovery never republishes
// the body, adopts a similar comment, or retries possibly delivered notifications.
func (s *Service) recoverHumanComment(ctx context.Context, a *RecoveryAssessment, r *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanCommentRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human comment intent is missing")
	}
	q := saved.Payload
	intent, err := humanCommentIntent(q)
	if err != nil || intent.Canonical != op.Intent || intent.Digest != op.IntentDigest || intent.ActorID != op.ActorID || intent.RepositoryID != op.RepositoryID ||
		intent.OperationID != op.OperationID || scope.RepositoryID != op.RepositoryID || scope.IssueID != nativeCommentID(q.Expected.IssueID) || op.TokenID != 0 || op.CredentialFingerprint != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human comment scope and intent differ")
	}
	if !op.Admitted && op.EffectState == nativeoperation_model.EffectPending {
		if _, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonRecoveredNoEffect, Cancellation: nativeoperation_model.CancellationNone,
		}, ""); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectNotCommitted)
	}
	if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human comment has no atomic committed receipt")
	}
	var receipt sdk.HumanCommentReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || receipt.CommentID < 1 || receipt.RepositoryID != op.RepositoryID || receipt.ActorID != op.ActorID ||
		receipt.IssueID != scope.IssueID || receipt.Index != nativeCommentID(q.Expected.Index) || receipt.Subject != q.Expected.Subject || receipt.SubjectID != nativeCommentID(q.Expected.SubjectID) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human comment receipt differs from intent")
	}
	comment, err := issues_model.GetCommentByID(ctx, receipt.CommentID)
	if err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "recorded human comment cannot be read")
	}
	if comment.IssueID != receipt.IssueID || comment.PosterID != receipt.ActorID || comment.Type != issues_model.CommentTypeComment || comment.Content != q.Body {
		return fenced(a, ReasonRecoveryUncertainEffect, "recorded human comment differs from native evidence")
	}
	issue, err := issues_model.GetIssueByID(ctx, receipt.IssueID)
	if err != nil || issue.RepoID != receipt.RepositoryID || issue.Index != receipt.Index || issue.IsPull != (receipt.Subject == "pull") {
		return fenced(a, ReasonRecoveryMissingEvidence, "recorded human comment parent differs")
	}
	if issue.IsPull {
		if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil || issue.PullRequest.ID != receipt.SubjectID {
			return fenced(a, ReasonRecoveryUncertainEffect, "recorded pull comment parent differs")
		}
	}
	if op.Completion == nativeoperation_model.CompletionComplete || op.Completion == nativeoperation_model.CompletionNeedsIntervention {
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectCommitted)
	}
	if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, r.Owner, r.Generation, nativeoperation_model.CompletionNeedsIntervention); err != nil {
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
	a.Checks = append(a.Checks, "atomic human comment receipt matches native row; notification completion requires intervention")
	s.retireStaleCapabilities()
	return *a, nil
}

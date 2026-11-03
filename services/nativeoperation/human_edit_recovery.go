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

// Offline recovery proves the exact atomic effect; it never reapplies an edit
// or repeats notifications/code-owner requests whose delivery is uncertain.
func (s *Service) recoverHumanEdit(ctx context.Context, a *RecoveryAssessment, r *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanEditRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human edit intent is missing")
	}
	q := saved.Payload
	intent, err := humanEditIntent(q)
	if err != nil || intent.Canonical != op.Intent || intent.Digest != op.IntentDigest || intent.ActorID != op.ActorID || intent.RepositoryID != op.RepositoryID ||
		intent.OperationID != op.OperationID || scope.RepositoryID != op.RepositoryID || scope.IssueID != nativeCommentID(q.Expected.IssueID) || op.TokenID != 0 || op.CredentialFingerprint != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human edit scope and intent differ")
	}
	if !op.Admitted && op.EffectState == nativeoperation_model.EffectPending {
		if _, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonRecoveredNoEffect, Cancellation: nativeoperation_model.CancellationNone}, ""); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectNotCommitted)
	}
	if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human edit has no atomic committed receipt")
	}
	title, digest, version := q.Expected.Title, q.Expected.BodyDigest, q.Expected.ContentVersion
	titleChanged := q.Title != nil && *q.Title != title
	if q.Title != nil {
		title = *q.Title
	}
	if q.Body != nil {
		nextDigest := editBodyDigest(*q.Body)
		if nextDigest != digest {
			version++
		}
		digest = nextDigest
	}
	var receipt sdk.HumanEditReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || receipt.RepositoryID != op.RepositoryID || receipt.ActorID != op.ActorID ||
		receipt.IssueID != scope.IssueID || receipt.Index != nativeCommentID(q.Expected.Index) || receipt.Subject != q.Expected.Subject || receipt.SubjectID != nativeCommentID(q.Expected.SubjectID) ||
		receipt.Title != title || receipt.BodyDigest != digest || receipt.ContentVersion != version || titleChanged && receipt.TitleCommentID < 1 || !titleChanged && receipt.TitleCommentID != 0 {
		return fenced(a, ReasonRecoveryUncertainEffect, "human edit receipt differs from intent")
	}
	issue, err := issues_model.GetIssueByID(ctx, receipt.IssueID)
	if err != nil || issue.RepoID != receipt.RepositoryID || issue.Index != receipt.Index || issue.IsPull != (receipt.Subject == "pull") ||
		issue.Title != title || editBodyDigest(issue.Content) != digest || int64(issue.ContentVersion) != version {
		return fenced(a, ReasonRecoveryMissingEvidence, "recorded human edit parent or content differs")
	}
	if issue.IsPull {
		if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil || issue.PullRequest.ID != receipt.SubjectID {
			return fenced(a, ReasonRecoveryUncertainEffect, "recorded human edit pull differs")
		}
	}
	if titleChanged {
		comment, err := issues_model.GetCommentByID(ctx, receipt.TitleCommentID)
		if err != nil || comment.IssueID != issue.ID || comment.PosterID != op.ActorID || comment.Type != issues_model.CommentTypeChangeTitle || comment.OldTitle != q.Expected.Title || comment.NewTitle != title {
			return fenced(a, ReasonRecoveryMissingEvidence, "recorded native title history differs")
		}
	}
	if op.Completion == nativeoperation_model.CompletionComplete || op.Completion == nativeoperation_model.CompletionNeedsIntervention {
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectCommitted)
	}
	if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, r.Owner, r.Generation, nativeoperation_model.CompletionNeedsIntervention); err != nil {
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
	a.Checks = append(a.Checks, "atomic human edit receipt matches native content/history; completion requires intervention")
	s.retireStaleCapabilities()
	return *a, nil
}

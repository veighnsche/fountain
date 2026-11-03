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

// Recovery attributes exact native state/history and never reapplies primary
// or repeats uncertain stopwatch/notification completion.
func (s *Service) recoverHumanState(ctx context.Context, a *RecoveryAssessment, r *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanStateRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human state intent is missing")
	}
	q := saved.Payload
	intent, err := humanStateIntent(q)
	if err != nil || intent.Canonical != op.Intent || intent.Digest != op.IntentDigest || intent.ActorID != op.ActorID || intent.RepositoryID != op.RepositoryID || intent.OperationID != op.OperationID ||
		scope.RepositoryID != op.RepositoryID || scope.IssueID != nativeCommentID(q.Expected.IssueID) || op.TokenID != 0 || op.CredentialFingerprint != "" ||
		intent.AuthRevision != op.AuthRevision || intent.ExpectedNativeRevision != op.ExpectedNativeRevision || intent.NotAfter != op.NotAfter {
		return fenced(a, ReasonRecoveryUncertainEffect, "human state scope and intent differ")
	}
	if !op.Admitted && op.EffectState == nativeoperation_model.EffectPending {
		if _, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonRecoveredNoEffect, Cancellation: nativeoperation_model.CancellationNone}, ""); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectNotCommitted)
	}
	if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human state has no atomic committed receipt")
	}
	var receipt sdk.HumanStateReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || !sdk.ValidHumanStateReceipt(receipt, q) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human state receipt differs from intent")
	}
	issue, err := issues_model.GetIssueByID(ctx, receipt.IssueID)
	if err != nil || issue.RepoID != receipt.RepositoryID || issue.Index != receipt.Index || issue.IsPull != (receipt.Subject == "pull") || issue.IsClosed != receipt.IsClosed || int64(issue.ClosedUnix) != receipt.ClosedUnix {
		return fenced(a, ReasonRecoveryMissingEvidence, "human state or parent differs")
	}
	if issue.IsPull {
		if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil || issue.PullRequest.ID != receipt.SubjectID || issue.PullRequest.HasMerged {
			return fenced(a, ReasonRecoveryUncertainEffect, "human state pull identity differs")
		}
	}
	comment, err := issues_model.GetCommentByID(ctx, receipt.CommentID)
	kind := issues_model.CommentTypeClose
	if !q.Close {
		kind = issues_model.CommentTypeReopen
	}
	if err != nil || comment.IssueID != issue.ID || comment.PosterID != op.ActorID || comment.Type != kind || comment.Content != "" || comment.OriginalAuthorID != 0 {
		return fenced(a, ReasonRecoveryMissingEvidence, "human state native history differs")
	}
	if op.Completion == nativeoperation_model.CompletionComplete || op.Completion == nativeoperation_model.CompletionNeedsIntervention {
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectCommitted)
	}
	if op.Completion != nativeoperation_model.CompletionPending && op.Completion != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human state completion differs")
	}
	if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, r.Owner, r.Generation, nativeoperation_model.CompletionNeedsIntervention); err != nil {
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
	a.Checks = append(a.Checks, "atomic human state/history and receipt match; completion requires intervention")
	s.retireStaleCapabilities()
	return *a, nil
}

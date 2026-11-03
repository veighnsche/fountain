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

// Recovery attributes the atomic native lock/history, never reapplies a lock or
// adopts similar history. No external completion step needs to be retried.
func (s *Service) recoverHumanLock(ctx context.Context, a *RecoveryAssessment, r *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanLockRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human lock intent is missing")
	}
	q := saved.Payload
	intent, err := humanLockIntent(q)
	if err != nil || intent.Canonical != op.Intent || intent.Digest != op.IntentDigest || intent.ActorID != op.ActorID || intent.RepositoryID != op.RepositoryID || intent.OperationID != op.OperationID ||
		scope.RepositoryID != op.RepositoryID || scope.IssueID != nativeCommentID(q.Expected.IssueID) || op.TokenID != 0 || op.CredentialFingerprint != "" ||
		intent.AuthRevision != op.AuthRevision || intent.ExpectedNativeRevision != op.ExpectedNativeRevision || intent.NotAfter != op.NotAfter {
		return fenced(a, ReasonRecoveryUncertainEffect, "human lock scope and intent differ")
	}
	if !op.Admitted && op.EffectState == nativeoperation_model.EffectPending {
		if _, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonRecoveredNoEffect, Cancellation: nativeoperation_model.CancellationNone}, ""); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectNotCommitted)
	}
	if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human lock has no atomic committed receipt")
	}
	var receipt sdk.HumanLockReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || !sdk.ValidHumanLockReceipt(receipt, q) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human lock receipt differs from intent")
	}
	issue, err := issues_model.GetIssueByID(ctx, receipt.IssueID)
	if err != nil || issue.RepoID != receipt.RepositoryID || issue.Index != receipt.Index || issue.IsPull != (receipt.Subject == "pull") || issue.IsLocked != receipt.IsLocked {
		return fenced(a, ReasonRecoveryMissingEvidence, "human lock state or parent differs")
	}
	if issue.IsPull {
		if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil || issue.PullRequest.ID != receipt.SubjectID {
			return fenced(a, ReasonRecoveryUncertainEffect, "human lock pull identity differs")
		}
	}
	comment, err := issues_model.GetCommentByID(ctx, receipt.CommentID)
	kind := issues_model.CommentTypeLock
	if !q.Lock {
		kind = issues_model.CommentTypeUnlock
	}
	if err != nil || comment.IssueID != issue.ID || comment.PosterID != op.ActorID || comment.Type != kind || comment.Content != q.Reason || comment.OriginalAuthorID != 0 {
		return fenced(a, ReasonRecoveryMissingEvidence, "human lock native history differs")
	}
	if op.Completion == nativeoperation_model.CompletionComplete {
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectCommitted)
	}
	if op.Completion != nativeoperation_model.CompletionPending && op.Completion != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human lock completion differs")
	}
	if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, r.Owner, r.Generation, nativeoperation_model.CompletionComplete); err != nil {
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
	a.Checks = append(a.Checks, "atomic human lock state/history and receipt match; no external completion remains")
	s.retireStaleCapabilities()
	return *a, nil
}

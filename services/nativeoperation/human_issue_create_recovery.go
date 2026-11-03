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

// Creation and receipt are one SQL transaction. Recovery never creates another
// issue, adopts similar content or repeats possibly delivered notifications.
func (s *Service) recoverHumanIssueCreate(ctx context.Context, a *RecoveryAssessment, r *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanIssueCreateRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human issue creation intent is missing")
	}
	q := saved.Payload
	intent, err := humanIssueCreateIntent(q)
	if err != nil || intent.Canonical != op.Intent || intent.Digest != op.IntentDigest || intent.ActorID != op.ActorID || intent.RepositoryID != op.RepositoryID ||
		intent.OperationID != op.OperationID || scope.RepositoryID != op.RepositoryID || scope.IssueID != 0 || op.TokenID != 0 || op.CredentialFingerprint != "" ||
		intent.AuthRevision != op.AuthRevision || intent.ExpectedNativeRevision != op.ExpectedNativeRevision || intent.NotAfter != op.NotAfter {
		return fenced(a, ReasonRecoveryUncertainEffect, "human issue creation scope and intent differ")
	}
	if !op.Admitted && op.EffectState == nativeoperation_model.EffectPending {
		if _, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonRecoveredNoEffect, Cancellation: nativeoperation_model.CancellationNone}, ""); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectNotCommitted)
	}
	if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human issue creation has no atomic committed receipt")
	}
	var receipt sdk.HumanIssueCreateReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || !sdk.ValidHumanIssueCreateReceipt(receipt, q) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human issue creation receipt differs from intent")
	}
	issue, err := issues_model.GetIssueByID(ctx, receipt.IssueID)
	if err != nil || issue.RepoID != receipt.RepositoryID || issue.Index != receipt.Index || issue.PosterID != receipt.ActorID ||
		issue.IsPull || issue.IsClosed || issue.OriginalAuthorID != 0 || issue.Title != q.Title || issue.Content != q.Body || issue.ContentVersion != 0 {
		return fenced(a, ReasonRecoveryMissingEvidence, "recorded human issue differs from native evidence")
	}
	if op.Completion == nativeoperation_model.CompletionComplete || op.Completion == nativeoperation_model.CompletionNeedsIntervention {
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectCommitted)
	}
	if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, r.Owner, r.Generation, nativeoperation_model.CompletionNeedsIntervention); err != nil {
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
	a.Checks = append(a.Checks, "atomic human issue creation receipt matches native row; notification completion requires intervention")
	s.retireStaleCapabilities()
	return *a, nil
}

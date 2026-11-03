// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"reflect"

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	user_model "forgejo.org/models/user"
)

// Recovery proves SQL state under the stopped owner's intact fence. It neither
// calls MarkConversation nor reconstructs/replays a human operation from today.
func (s *Service) recoverHumanThread(ctx context.Context, a *RecoveryAssessment, r *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanThreadRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human thread intent is missing")
	}
	q := saved.Payload
	intent, err := humanThreadIntent(q)
	expectedScope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: op.RepositoryID, IssueID: nativeCommentID(q.Expected.Review.IssueID)}
	if err != nil || intent.Canonical != op.Intent || intent.Digest != op.IntentDigest || intent.OperationID != op.OperationID || intent.ActorID != op.ActorID || intent.RepositoryID != op.RepositoryID || intent.AuthRevision != op.AuthRevision || intent.ExpectedNativeRevision != op.ExpectedNativeRevision || intent.NotAfter != op.NotAfter || !reflect.DeepEqual(scope, expectedScope) || r.Revision < 1 || r.Revision-1 != op.ExpectedNativeRevision || op.TokenID != 0 || op.CredentialFingerprint != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human thread scope and intent differ")
	}
	if !op.Admitted && op.EffectState == nativeoperation_model.EffectPending && op.Receipt == "" && op.Completion == "" {
		if _, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonRecoveredNoEffect, Cancellation: nativeoperation_model.CancellationNone}, ""); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectNotCommitted)
	}
	if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human thread has no atomic committed receipt")
	}
	var receipt sdk.HumanThreadReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || !sdk.ValidHumanThreadReceipt(receipt, q) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human thread receipt differs from intent")
	}
	canonicalReceipt, err := json.Marshal(receipt)
	if err != nil || string(canonicalReceipt) != op.Receipt {
		return fenced(a, ReasonRecoveryUncertainEffect, "human thread exact receipt encoding differs")
	}
	issue, err := issues_model.GetIssueByID(ctx, receipt.IssueID)
	if err != nil || issue.RepoID != receipt.RepositoryID || issue.Index != receipt.Index || !issue.IsPull {
		return fenced(a, ReasonRecoveryMissingEvidence, "human thread parent differs")
	}
	if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil || issue.PullRequest.ID != receipt.PullID || issue.PullRequest.BaseRepoID != receipt.RepositoryID {
		return fenced(a, ReasonRecoveryMissingEvidence, "human thread pull identity differs")
	}
	actor, err := user_model.GetUserByID(ctx, receipt.ActorID)
	if err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human thread original actor is missing")
	}
	// This is offline attribution, not fresh permission. A retained principal can
	// prove an original own draft even if current repository permission was lost.
	evidence, err := ReadHumanThreadEvidence(ctx, issue, actor, nativeCommentID(receipt.After.RootCommentID))
	if err != nil || !reflect.DeepEqual(evidence, receipt.After) {
		return fenced(a, ReasonRecoveryMissingEvidence, "human thread complete post-state differs")
	}
	if op.Completion == nativeoperation_model.CompletionComplete {
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectCommitted)
	}
	if op.Completion != nativeoperation_model.CompletionPending && op.Completion != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human thread completion differs")
	}
	if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, r.Owner, r.Generation, nativeoperation_model.CompletionComplete); err != nil {
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
	a.Checks = append(a.Checks, "atomic human thread receipt and complete SQL post-state match; no primary or completion work replayed")
	s.retireStaleCapabilities()
	return *a, nil
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// RecoverConditionalSQLPrimary joins stopped-domain native evidence to one of
// the four atomic SQL primaries. Inspection never executes a primary or sends
// notifications. The exact capture and reservation stay locked until the known
// outcome and proof revocation commit together.
func RecoverConditionalSQLPrimary(ctx context.Context, want *Operation, owner string, generation int64, scope string, inspect func(context.Context, *Operation) (string, error)) (*Operation, error) {
	if !OfflineInhibited() {
		return nil, ErrInhibited
	}
	if want == nil || inspect == nil || owner == "" || generation < 1 || scope == "" || want.ExpectedNativeRevision < 1 {
		return nil, ErrWrongOwner
	}
	switch want.Kind {
	case KindThreadResolve, KindLifecycleIssue, KindLifecycleReview, KindLifecycleNote:
	default:
		return nil, ErrAdmissionLost
	}
	var recorded *Operation
	err := db.WithTx(ctx, func(tx context.Context) error {
		n, err := db.GetEngine(tx).Where("id=1 AND owner=? AND owner_kind=? AND generation=? AND revision=? AND scope=?",
			owner, OwnerConditional, generation, want.ExpectedNativeRevision+1, scope).
			Cols("scope").Update(&Reservation{Scope: scope})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrWrongOwner
		}
		// Serialize with cancellation before inspecting its ordering truth.
		if _, err := db.GetEngine(tx).Where("installation_id=? AND operation_id=? AND kind=?", want.InstallationID, want.OperationID, want.Kind).
			Cols("kind").Update(&Operation{Kind: want.Kind}); err != nil {
			return err
		}
		op, err := LookupOperation(tx, want.InstallationID, want.OperationID)
		if err != nil {
			return err
		}
		if op == nil || !op.Submitted || op.ID != want.ID || op.Kind != want.Kind || op.ActorID != want.ActorID || op.RepositoryID != want.RepositoryID ||
			op.TokenID != want.TokenID || op.CredentialFingerprint != want.CredentialFingerprint || op.PackageID != want.PackageID || op.ControllerGrantID != want.ControllerGrantID ||
			op.AuthRevision != want.AuthRevision || op.ExpectedNativeRevision != want.ExpectedNativeRevision || op.NotAfter != want.NotAfter || op.Intent != want.Intent || op.IntentDigest != want.IntentDigest ||
			op.Admitted != want.Admitted || op.EffectState != want.EffectState || op.Receipt != want.Receipt || op.Completion != want.Completion || op.Reason != want.Reason {
			return ErrAdmissionLost
		}
		if !op.Admitted && (op.EffectState == EffectPending || op.EffectState == EffectNotCommitted) && op.Receipt == "" && op.Completion == "" {
			// These kinds publish SQL effects and their receipt in one transaction.
			// An unadmitted empty capture therefore proves no primary committed.
			if op.EffectState == EffectPending {
				cancellation := CancellationNone
				if op.Revoked {
					cancellation = CancellationCancelled
				}
				reason := op.Reason
				if reason == "" {
					reason = ReasonRecoveredNoEffect
				}
				op, err = SetTerminal(tx, op.InstallationID, op.OperationID, TerminalOutcome{EffectState: EffectNotCommitted, Reason: reason, Cancellation: cancellation}, "")
				if err != nil {
					return err
				}
			}
			recorded = op
			return ReleaseExactOwner(tx, owner, generation)
		}
		if !op.Admitted || op.EffectState != EffectCommitted || op.Receipt == "" {
			return ErrAdmissionLost
		}
		completion, err := inspect(tx, op)
		if err != nil {
			return err
		}
		if completion != CompletionComplete && completion != CompletionNeedsIntervention {
			return ErrAdmissionLost
		}
		if op.Completion == CompletionPending {
			recorded, err = SetCompletionAndRelease(tx, op.InstallationID, op.OperationID, owner, generation, completion)
			return err
		}
		if op.Completion != completion {
			return ErrAdmissionLost
		}
		recorded = op
		return ReleaseExactOwner(tx, owner, generation)
	})
	if err != nil {
		return nil, err
	}
	return recorded, nil
}

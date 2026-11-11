// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// Shared exact human capture/generation lock for SQL-primary consumers.
func lockHumanSQLPrimaryOperation(ctx context.Context, want *Operation, owner string, generation int64, scope, kind string) (*Operation, error) {
	if want == nil || want.Kind != kind || kind != KindHumanManualMerge && kind != KindHumanPullCreate || want.TokenID != 0 || want.CredentialFingerprint != "" || owner == "" || generation < 1 || scope == "" || want.ExpectedNativeRevision < 1 {
		return nil, ErrWrongOwner
	}
	n, err := db.GetEngine(ctx).Where("id=1 AND owner=? AND owner_kind=? AND generation=? AND revision=? AND scope=?", owner, OwnerConditional, generation, want.ExpectedNativeRevision+1, scope).Cols("scope").Update(&Reservation{Scope: scope})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrWrongOwner
	}
	op, err := LookupOperation(ctx, want.InstallationID, want.OperationID)
	if err != nil {
		return nil, err
	}
	if op == nil || !op.Submitted || op.Kind != want.Kind || op.ActorID != want.ActorID || op.RepositoryID != want.RepositoryID || op.AuthRevision != want.AuthRevision || op.ExpectedNativeRevision != want.ExpectedNativeRevision || op.NotAfter != want.NotAfter || op.Intent != want.Intent || op.IntentDigest != want.IntentDigest || op.TokenID != 0 || op.CredentialFingerprint != "" {
		return nil, ErrAdmissionLost
	}
	return op, nil
}

// These transitions share one SQL state machine while callers retain their
// domain-specific target locks and evidence checks in the enclosing transaction.
func commitHumanSQLPrimary(ctx context.Context, want *Operation, owner string, generation int64, scope, kind string, now int64, apply func(context.Context) (string, error)) (recorded *Operation, err error) {
	if apply == nil {
		return nil, ErrWrongOwner
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if _, err := lockHumanSQLPrimaryCapture(ctx, want, owner, generation, scope, kind); err != nil {
			return err
		}
		recorded, err = commitConditionalPrimary(ctx, want.InstallationID, want.OperationID, owner, now, apply)
		return err
	})
	if err != nil {
		return nil, err
	}
	return recorded, nil
}

func lockHumanSQLPrimaryCapture(ctx context.Context, want *Operation, owner string, generation int64, scope, kind string) (*Operation, error) {
	op, err := lockHumanSQLPrimaryOperation(ctx, want, owner, generation, scope, kind)
	if err != nil {
		return nil, err
	}
	if op.EffectState != EffectPending || op.Admitted || op.Receipt != "" || op.Completion != "" {
		return nil, ErrAdmissionLost
	}
	return op, nil
}

func refuseHumanSQLPrimary(ctx context.Context, want *Operation, owner string, generation int64, scope, reason, kind string) (recorded *Operation, err error) {
	err = db.WithTx(ctx, func(ctx context.Context) error {
		op, err := lockHumanSQLPrimaryCapture(ctx, want, owner, generation, scope, kind)
		if err != nil {
			return err
		}
		cancellation := CancellationNone
		if op.Revoked {
			cancellation = CancellationCancelled
		}
		recorded, err = SetTerminal(ctx, want.InstallationID, want.OperationID, TerminalOutcome{EffectState: EffectNotCommitted, Reason: reason, Cancellation: cancellation}, owner)
		return err
	})
	if err != nil {
		return nil, err
	}
	return recorded, nil
}

func withHumanSQLPrimaryReceipt(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt, kind string, apply func(context.Context, *Operation) error) error {
	if receipt == "" || apply == nil {
		return ErrAdmissionLost
	}
	return db.WithTx(ctx, func(tx context.Context) error {
		op, err := lockHumanSQLPrimaryOperation(tx, want, owner, generation, scope, kind)
		if err != nil {
			return err
		}
		if !op.Admitted || op.EffectState != EffectCommitted || op.Completion != CompletionPending || op.Receipt != receipt {
			return ErrAdmissionLost
		}
		return apply(tx, op)
	})
}

func changeHumanSQLPrimaryPhase(ctx context.Context, owner string, generation int64, before, after string) error {
	if before == after || after == "" {
		return ErrAdmissionLost
	}
	n, err := db.GetEngine(ctx).Where("id=1 AND owner=? AND generation=? AND scope=?", owner, generation, before).Cols("scope").Update(&Reservation{Scope: after})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrWrongOwner
	}
	return nil
}

func recordHumanSQLPrimaryFinish(ctx context.Context, want *Operation, owner string, generation int64, started, done, receipt, kind string, succeeded bool) (*Operation, error) {
	var recorded *Operation
	err := withHumanSQLPrimaryReceipt(ctx, want, owner, generation, started, receipt, kind, func(tx context.Context, op *Operation) error {
		if succeeded {
			if err := changeHumanSQLPrimaryPhase(tx, owner, generation, started, done); err != nil {
				return err
			}
		} else {
			op.Completion = CompletionNeedsIntervention
			if _, err := db.GetEngine(tx).ID(op.ID).Cols("completion").Update(op); err != nil {
				return err
			}
		}
		recorded = op
		return nil
	})
	return recorded, err
}

func completeHumanSQLPrimary(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt, kind string) (*Operation, error) {
	var recorded *Operation
	err := withHumanSQLPrimaryReceipt(ctx, want, owner, generation, scope, receipt, kind, func(tx context.Context, _ *Operation) error {
		var err error
		recorded, err = SetCompletionAndRelease(tx, want.InstallationID, want.OperationID, owner, generation, CompletionComplete)
		return err
	})
	return recorded, err
}

func recoverHumanSQLPrimary(ctx context.Context, want *Operation, owner string, generation int64, scope, kind string, inspect func(context.Context, *Operation) (string, error)) (*Operation, error) {
	if !OfflineInhibited() {
		return nil, ErrInhibited
	}
	if inspect == nil {
		return nil, ErrAdmissionLost
	}
	var recorded *Operation
	err := db.WithTx(ctx, func(tx context.Context) error {
		op, err := lockHumanSQLPrimaryOperation(tx, want, owner, generation, scope, kind)
		if err != nil {
			return err
		}
		completion, err := inspect(tx, op)
		if err != nil {
			return err
		}
		switch {
		case !op.Admitted && (op.EffectState == EffectPending || op.EffectState == EffectNotCommitted) && op.Receipt == "" && op.Completion == "" && completion == "":
			if op.EffectState == EffectPending {
				cancellation := CancellationNone
				if op.Revoked {
					cancellation = CancellationCancelled
				}
				op, err = SetTerminal(tx, op.InstallationID, op.OperationID, TerminalOutcome{EffectState: EffectNotCommitted, Reason: ReasonRecoveredNoEffect, Cancellation: cancellation}, "")
				if err != nil {
					return err
				}
			}
			recorded = op
			return ReleaseExactOwner(tx, owner, generation)
		case op.Admitted && op.EffectState == EffectCommitted && op.Receipt != "" && (completion == CompletionComplete || completion == CompletionNeedsIntervention):
			if op.Completion == CompletionPending {
				recorded, err = SetCompletionAndRelease(tx, op.InstallationID, op.OperationID, owner, generation, completion)
				return err
			}
			if op.Completion != completion {
				return ErrAdmissionLost
			}
			recorded = op
			return ReleaseExactOwner(tx, owner, generation)
		default:
			return ErrAdmissionLost
		}
	})
	if err != nil {
		return nil, err
	}
	return recorded, nil
}

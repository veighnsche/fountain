// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// CommitHumanPullCreatePrimary adds strict immutable capture/scope/generation
// binding to the existing atomic SQL primary/cancellation owner. The callback
// rechecks the live human, selected repositories and policy in this transaction.
// Ownership remains held for completion; this primitive grants no Git write.
func CommitHumanPullCreatePrimary(ctx context.Context, want *Operation, owner string, generation int64, scope string, now int64, apply func(context.Context) (string, error)) (recorded *Operation, err error) {
	if apply == nil {
		return nil, ErrWrongOwner
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if _, err := lockHumanPullCreateCapture(ctx, want, owner, generation, scope); err != nil {
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

func lockHumanPullCreateCapture(ctx context.Context, want *Operation, owner string, generation int64, scope string) (*Operation, error) {
	op, err := lockHumanPullCreateOperation(ctx, want, owner, generation, scope)
	if err != nil {
		return nil, err
	}
	if op.EffectState != EffectPending || op.Admitted || op.Receipt != "" || op.Completion != "" {
		return nil, ErrAdmissionLost
	}
	return op, nil
}

// Both primary and completion serialize on the exact held generation/scope and
// immutable human capture. Cancellation may mark the operation revoked, but
// cannot erase completion of an already committed SQL primary.
func lockHumanPullCreateOperation(ctx context.Context, want *Operation, owner string, generation int64, scope string) (*Operation, error) {
	return lockHumanSQLPrimaryOperation(ctx, want, owner, generation, scope, KindHumanPullCreate)
}

// Refusal is ordered under the same exact generation/scope/capture lock as
// admission. A stale caller cannot release a replaced owner or rewrite truth.
func RefuseHumanPullCreate(ctx context.Context, want *Operation, owner string, generation int64, scope, reason string) (recorded *Operation, err error) {
	err = db.WithTx(ctx, func(ctx context.Context) error {
		op, err := lockHumanPullCreateCapture(ctx, want, owner, generation, scope)
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

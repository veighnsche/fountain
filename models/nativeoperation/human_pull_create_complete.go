// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// WithHumanPullCreateReceipt serializes a consumer of the committed human SQL
// primary under its exact immutable capture, owner generation and current scope.
// Cancellation after that commit cannot erase its receipt or completion work.
func WithHumanPullCreateReceipt(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string, apply func(context.Context, *Operation) error) error {
	if receipt == "" || apply == nil {
		return ErrAdmissionLost
	}
	return db.WithTx(ctx, func(tx context.Context) error {
		op, err := lockHumanPullCreateOperation(tx, want, owner, generation, scope)
		if err != nil {
			return err
		}
		if !op.Admitted || op.EffectState != EffectCommitted || op.Completion != CompletionPending || op.Receipt != receipt {
			return ErrAdmissionLost
		}
		return apply(tx, op)
	})
}

// ChangeHumanPullCreatePhase persists one exact completion transition. Domain
// evidence is checked by the caller in the same transaction; Git runs outside it.
func ChangeHumanPullCreatePhase(ctx context.Context, want *Operation, owner string, generation int64, before, after, receipt string) (*Operation, error) {
	if before == after || after == "" {
		return nil, ErrAdmissionLost
	}
	var recorded *Operation
	err := WithHumanPullCreateReceipt(ctx, want, owner, generation, before, receipt, func(tx context.Context, op *Operation) error {
		n, err := db.GetEngine(tx).Where("id=1 AND owner=? AND generation=? AND scope=?", owner, generation, before).Cols("scope").Update(&Reservation{Scope: after})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrWrongOwner
		}
		recorded = op
		return nil
	})
	return recorded, err
}

// Failed or uncertain finishing keeps its claim and proof; it must not resend
// notifications. Successful finishing advances the exact scope before release.
func RecordHumanPullCreateFinish(ctx context.Context, want *Operation, owner string, generation int64, started, done, receipt string, succeeded bool) (*Operation, error) {
	if succeeded {
		return ChangeHumanPullCreatePhase(ctx, want, owner, generation, started, done, receipt)
	}
	var recorded *Operation
	err := WithHumanPullCreateReceipt(ctx, want, owner, generation, started, receipt, func(tx context.Context, op *Operation) error {
		op.Completion = CompletionNeedsIntervention
		if _, err := db.GetEngine(tx).ID(op.ID).Cols("completion").Update(op); err != nil {
			return err
		}
		recorded = op
		return nil
	})
	return recorded, err
}

// Completion and proof revocation are atomic after the service proves exact
// native SQL/Git evidence and successful finishing under the held generation.
func CompleteHumanPullCreate(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string) (*Operation, error) {
	var recorded *Operation
	err := WithHumanPullCreateReceipt(ctx, want, owner, generation, scope, receipt, func(tx context.Context, _ *Operation) error {
		var err error
		recorded, err = SetCompletionAndRelease(tx, want.InstallationID, want.OperationID, owner, generation, CompletionComplete)
		return err
	})
	return recorded, err
}

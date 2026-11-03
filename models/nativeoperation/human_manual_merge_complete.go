// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// WithHumanManualMergeReceipt locks the exact committed SQL primary and its
// pending completion before consuming domain evidence. It grants no Git write.
func WithHumanManualMergeReceipt(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string, apply func(context.Context, *Operation) error) error {
	if receipt == "" || apply == nil {
		return ErrAdmissionLost
	}
	return db.WithTx(ctx, func(tx context.Context) error {
		op, err := lockHumanManualMergeOperation(tx, want, owner, generation, scope)
		if err != nil {
			return err
		}
		if !op.Admitted || op.EffectState != EffectCommitted || op.Completion != CompletionPending || op.Receipt != receipt {
			return ErrAdmissionLost
		}
		return apply(tx, op)
	})
}

// ClaimHumanManualMergeFinish changes the scope once, committing its durable
// claim before notifications/cross-reference actions escape SQL.
func ClaimHumanManualMergeFinish(ctx context.Context, want *Operation, owner string, generation int64, before, started, receipt string) error {
	return WithHumanManualMergeReceipt(ctx, want, owner, generation, before, receipt, func(tx context.Context, _ *Operation) error {
		return changeHumanManualMergePhase(tx, owner, generation, before, started)
	})
}

func changeHumanManualMergePhase(ctx context.Context, owner string, generation int64, before, after string) error {
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

// A failed/uncertain dispatch remains claimed and fenced; it must never resend.
func RecordHumanManualMergeFinish(ctx context.Context, want *Operation, owner string, generation int64, started, done, receipt string, succeeded bool) (*Operation, error) {
	var recorded *Operation
	err := WithHumanManualMergeReceipt(ctx, want, owner, generation, started, receipt, func(tx context.Context, op *Operation) error {
		if succeeded {
			if err := changeHumanManualMergePhase(tx, owner, generation, started, done); err != nil {
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

// Completion and proof revocation are one transaction. The service requires
// successful finishing and exact native SQL/Git evidence before calling this.
func CompleteHumanManualMerge(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string) (*Operation, error) {
	var recorded *Operation
	err := WithHumanManualMergeReceipt(ctx, want, owner, generation, scope, receipt, func(tx context.Context, _ *Operation) error {
		var err error
		recorded, err = SetCompletionAndRelease(tx, want.InstallationID, want.OperationID, owner, generation, CompletionComplete)
		return err
	})
	return recorded, err
}

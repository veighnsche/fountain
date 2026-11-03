// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// RecordHumanMergeEffect joins authoritative observation and its immutable
// outcome under the exact generation/scope. It retains the owner for completion
// or interrupted-writer recovery, including after a not-committed result. Late
// prepared callbacks cannot admit a finalized operation under this held fence.
func RecordHumanMergeEffect(ctx context.Context, want *Operation, owner string, generation int64, scope string, observe func(context.Context, *Operation) (TerminalOutcome, error)) (*Operation, error) {
	if observe == nil {
		return nil, ErrAdmissionLost
	}
	var recorded *Operation
	err := db.WithTx(ctx, func(ctx context.Context) error {
		op, err := lockHumanMergeOutcome(ctx, want, owner, generation, scope)
		if err != nil {
			return err
		}
		if op.IsTerminal() {
			recorded = op // Never rewrite a saved terminal receipt on replay.
			return nil
		}
		if op.EffectState != EffectPending && op.EffectState != EffectIndeterminate || op.Receipt != "" || op.Completion != "" {
			return ErrAdmissionLost
		}
		outcome, err := observe(ctx, op)
		if err != nil {
			return err
		}
		switch outcome.EffectState {
		case EffectCommitted:
			if !op.Admitted || outcome.Receipt == "" || outcome.Completion != CompletionPending {
				return ErrAdmissionLost
			}
		case EffectNotCommitted:
			if op.Admitted || outcome.Receipt != "" || outcome.Completion != "" {
				return ErrAdmissionLost
			}
		case EffectIndeterminate:
			if outcome.Receipt != "" || outcome.Completion != "" {
				return ErrAdmissionLost
			}
		default:
			return ErrAdmissionLost
		}
		op.EffectState, op.Reason, op.Cancellation, op.Completion, op.Receipt = outcome.EffectState, outcome.Reason, outcome.Cancellation, outcome.Completion, outcome.Receipt
		if _, err := db.GetEngine(ctx).ID(op.ID).Cols("effect_state", "reason", "cancellation", "completion", "receipt").Update(op); err != nil {
			return err
		}
		recorded = op
		return nil
	})
	if err != nil {
		return nil, err
	}
	return recorded, nil
}

func lockHumanMergeOutcome(ctx context.Context, want *Operation, owner string, generation int64, scope string) (*Operation, error) {
	if err := lockHumanMergeScope(ctx, want, owner, generation, scope, scope); err != nil {
		return nil, err
	}
	// Lock the operation too, without changing any decision data. Admission,
	// cancellation and competing attribution must settle before observation
	// reads the authoritative mutable flags. Scope is already locked above.
	if _, err := db.GetEngine(ctx).Exec("UPDATE `operation` SET `reason`=`reason` WHERE `installation_id`=? AND `operation_id`=?", want.InstallationID, want.OperationID); err != nil {
		return nil, err
	}
	op, err := LookupOperation(ctx, want.InstallationID, want.OperationID)
	if err != nil {
		return nil, err
	}
	if !sameHumanMergeIntent(op, want) {
		return nil, ErrAdmissionLost
	}
	return op, nil
}

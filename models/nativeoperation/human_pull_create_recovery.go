// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// RecoverHumanPullCreate joins stopped-domain SQL evidence to the exact capture
// and atomic known-outcome release. The service reads Git before this SQL-only
// transaction. No primary, ref write or notification dispatch is replayed.
func RecoverHumanPullCreate(ctx context.Context, want *Operation, owner string, generation int64, scope string, inspect func(context.Context, *Operation) (string, error)) (*Operation, error) {
	if !OfflineInhibited() {
		return nil, ErrInhibited
	}
	if inspect == nil {
		return nil, ErrAdmissionLost
	}
	var recorded *Operation
	err := db.WithTx(ctx, func(tx context.Context) error {
		op, err := lockHumanPullCreateOperation(tx, want, owner, generation, scope)
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

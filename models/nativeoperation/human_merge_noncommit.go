// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// ReleaseHumanMergeNotCommitted releases an exact terminal non-commit without
// changing its immutable refusal/cancellation truth. The service must verify
// native closed-stage and fresh no-effect evidence under these same SQL locks.
// Pending, admitted, committed and uncertain outcomes cannot use this boundary.
func ReleaseHumanMergeNotCommitted(ctx context.Context, want *Operation, owner string, generation int64, scope string, verify func(context.Context, *Operation) error) (*Operation, error) {
	if verify == nil {
		return nil, ErrAdmissionLost
	}
	var recorded *Operation
	err := db.WithTx(ctx, func(tx context.Context) error {
		op, err := lockHumanMergeOutcome(tx, want, owner, generation, scope)
		if err != nil {
			return err
		}
		if op.EffectState != EffectNotCommitted || op.Admitted || op.Receipt != "" || op.Completion != "" {
			return ErrAdmissionLost
		}
		if err := verify(tx, op); err != nil {
			return err
		}
		if err := ReleaseExactOwner(tx, owner, generation); err != nil {
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

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// ApplyHumanMergeMetadata orders native SQL completion under the same scope/op
// locks as attribution. The immutable committed receipt and pending completion
// must still match. Native domain rows provide idempotence; this producer never
// marks all completion done or releases an owner whose writer is not reconciled.
func ApplyHumanMergeMetadata(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string, apply func(context.Context, *Operation) (bool, error)) (changed bool, err error) {
	if apply == nil || receipt == "" {
		return false, ErrAdmissionLost
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		op, err := lockHumanMergeOutcome(ctx, want, owner, generation, scope)
		if err != nil {
			return err
		}
		if !op.Admitted || op.EffectState != EffectCommitted || op.Completion != CompletionPending || op.Receipt != receipt {
			return ErrAdmissionLost
		}
		changed, err = apply(ctx, op)
		return err
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

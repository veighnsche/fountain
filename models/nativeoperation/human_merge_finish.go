// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// ClaimHumanMergeDispatch persists a one-shot completion dispatch marker before
// native finishing or push events escape SQL. A competing original scope loses.
func ClaimHumanMergeDispatch(ctx context.Context, want *Operation, owner string, generation int64, before, started, receipt string) error {
	if before == started {
		return ErrAdmissionLost
	}
	_, err := ApplyHumanMergeMetadata(ctx, want, owner, generation, before, receipt, func(tx context.Context, _ *Operation) (bool, error) {
		return false, lockHumanMergeScope(tx, want, owner, generation, before, started)
	})
	return err
}

// RecordHumanMergeDispatch records one dispatch phase, never owner release. A
// failure stays claimed and needs intervention; even a lost result must not
// automatically resend notifications or repeat partially finished native actions.
func RecordHumanMergeDispatch(ctx context.Context, want *Operation, owner string, generation int64, started, done, receipt string, succeeded bool) (*Operation, error) {
	var recorded *Operation
	_, err := ApplyHumanMergeMetadata(ctx, want, owner, generation, started, receipt, func(tx context.Context, op *Operation) (bool, error) {
		if succeeded {
			if started == done {
				return false, ErrAdmissionLost
			}
			if err := lockHumanMergeScope(tx, want, owner, generation, started, done); err != nil {
				return false, err
			}
		} else {
			op.Completion = CompletionNeedsIntervention
			if _, err := db.GetEngine(tx).ID(op.ID).Cols("completion").Update(op); err != nil {
				return false, err
			}
		}
		recorded = op
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return recorded, nil
}

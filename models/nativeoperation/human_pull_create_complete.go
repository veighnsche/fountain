// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// WithHumanPullCreateReceipt serializes a consumer of the committed human SQL
// primary under its exact immutable capture, owner generation and current scope.
// Cancellation after that commit cannot erase its receipt or completion work.
func WithHumanPullCreateReceipt(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string, apply func(context.Context, *Operation) error) error {
	return withHumanSQLPrimaryReceipt(ctx, want, owner, generation, scope, receipt, KindHumanPullCreate, apply)
}

// ChangeHumanPullCreatePhase persists one exact completion transition. Domain
// evidence is checked by the caller in the same transaction; Git runs outside it.
func ChangeHumanPullCreatePhase(ctx context.Context, want *Operation, owner string, generation int64, before, after, receipt string) (*Operation, error) {
	if before == after || after == "" {
		return nil, ErrAdmissionLost
	}
	var recorded *Operation
	err := WithHumanPullCreateReceipt(ctx, want, owner, generation, before, receipt, func(tx context.Context, op *Operation) error {
		if err := changeHumanSQLPrimaryPhase(tx, owner, generation, before, after); err != nil {
			return err
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
	return recordHumanSQLPrimaryFinish(ctx, want, owner, generation, started, done, receipt, KindHumanPullCreate, false)
}

// Completion and proof revocation are atomic after the service proves exact
// native SQL/Git evidence and successful finishing under the held generation.
func CompleteHumanPullCreate(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string) (*Operation, error) {
	return completeHumanSQLPrimary(ctx, want, owner, generation, scope, receipt, KindHumanPullCreate)
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// CommitHumanManualMergePrimary adds strict immutable capture/scope/generation
// binding to the existing atomic SQL primary/cancellation owner. The callback
// rechecks the live human, exact native PR/commit and policy in this transaction.
// Ownership remains held for completion; this primitive grants no Git write.
func CommitHumanManualMergePrimary(ctx context.Context, want *Operation, owner string, generation int64, scope string, now int64, apply func(context.Context) (string, error)) (recorded *Operation, err error) {
	return commitHumanSQLPrimary(ctx, want, owner, generation, scope, KindHumanManualMerge, now, apply)
}

func lockHumanManualMergeCapture(ctx context.Context, want *Operation, owner string, generation int64, scope string) (*Operation, error) {
	return lockHumanSQLPrimaryCapture(ctx, want, owner, generation, scope, KindHumanManualMerge)
}

// Both primary and completion serialize on the exact held generation/scope and
// immutable human capture. Cancellation may mark the operation revoked, but
// cannot erase completion of an already committed SQL primary.
func lockHumanManualMergeOperation(ctx context.Context, want *Operation, owner string, generation int64, scope string) (*Operation, error) {
	return lockHumanSQLPrimaryOperation(ctx, want, owner, generation, scope, KindHumanManualMerge)
}

// Refusal is ordered under the same exact generation/scope/capture lock as
// admission. A stale caller cannot release a replaced owner or rewrite truth.
func RefuseHumanManualMerge(ctx context.Context, want *Operation, owner string, generation int64, scope, reason string) (recorded *Operation, err error) {
	return refuseHumanSQLPrimary(ctx, want, owner, generation, scope, reason, KindHumanManualMerge)
}

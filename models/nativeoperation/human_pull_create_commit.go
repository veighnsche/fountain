// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// CommitHumanPullCreatePrimary adds strict immutable capture/scope/generation
// binding to the existing atomic SQL primary/cancellation owner. The callback
// rechecks the live human, selected repositories and policy in this transaction.
// Ownership remains held for completion; this primitive grants no Git write.
func CommitHumanPullCreatePrimary(ctx context.Context, want *Operation, owner string, generation int64, scope string, now int64, apply func(context.Context) (string, error)) (recorded *Operation, err error) {
	return commitHumanSQLPrimary(ctx, want, owner, generation, scope, KindHumanPullCreate, now, apply)
}

// Refusal is ordered under the same exact generation/scope/capture lock as
// admission. A stale caller cannot release a replaced owner or rewrite truth.
func RefuseHumanPullCreate(ctx context.Context, want *Operation, owner string, generation int64, scope, reason string) (recorded *Operation, err error) {
	return refuseHumanSQLPrimary(ctx, want, owner, generation, scope, reason, KindHumanPullCreate)
}

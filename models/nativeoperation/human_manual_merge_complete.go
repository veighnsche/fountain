// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// WithHumanManualMergeReceipt locks the exact committed SQL primary and its
// pending completion before consuming domain evidence. It grants no Git write.
func WithHumanManualMergeReceipt(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string, apply func(context.Context, *Operation) error) error {
	return withHumanSQLPrimaryReceipt(ctx, want, owner, generation, scope, receipt, KindHumanManualMerge, apply)
}

// ClaimHumanManualMergeFinish changes the scope once, committing its durable
// claim before notifications/cross-reference actions escape SQL.
func ClaimHumanManualMergeFinish(ctx context.Context, want *Operation, owner string, generation int64, before, started, receipt string) error {
	return WithHumanManualMergeReceipt(ctx, want, owner, generation, before, receipt, func(tx context.Context, _ *Operation) error {
		return changeHumanManualMergePhase(tx, owner, generation, before, started)
	})
}

func changeHumanManualMergePhase(ctx context.Context, owner string, generation int64, before, after string) error {
	return changeHumanSQLPrimaryPhase(ctx, owner, generation, before, after)
}

// A failed/uncertain dispatch remains claimed and fenced; it must never resend.
func RecordHumanManualMergeFinish(ctx context.Context, want *Operation, owner string, generation int64, started, done, receipt string, succeeded bool) (*Operation, error) {
	return recordHumanSQLPrimaryFinish(ctx, want, owner, generation, started, done, receipt, KindHumanManualMerge, succeeded)
}

// Completion and proof revocation are one transaction. The service requires
// successful finishing and exact native SQL/Git evidence before calling this.
func CompleteHumanManualMerge(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string) (*Operation, error) {
	return completeHumanSQLPrimary(ctx, want, owner, generation, scope, receipt, KindHumanManualMerge)
}

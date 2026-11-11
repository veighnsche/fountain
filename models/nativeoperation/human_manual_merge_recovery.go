// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// RecoverHumanManualMerge joins offline native evidence to the exact capture
// lock and atomic known-outcome release. It never runs a primary or dispatch.
// inspect returns the completion established from authoritative stopped state.
func RecoverHumanManualMerge(ctx context.Context, want *Operation, owner string, generation int64, scope string, inspect func(context.Context, *Operation) (string, error)) (*Operation, error) {
	return recoverHumanSQLPrimary(ctx, want, owner, generation, scope, KindHumanManualMerge, inspect)
}

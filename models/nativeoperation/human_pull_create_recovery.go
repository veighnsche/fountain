// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// RecoverHumanPullCreate joins stopped-domain SQL evidence to the exact capture
// and atomic known-outcome release. The service reads Git before this SQL-only
// transaction. No primary, ref write or notification dispatch is replayed.
func RecoverHumanPullCreate(ctx context.Context, want *Operation, owner string, generation int64, scope string, inspect func(context.Context, *Operation) (string, error)) (*Operation, error) {
	return recoverHumanSQLPrimary(ctx, want, owner, generation, scope, KindHumanPullCreate, inspect)
}

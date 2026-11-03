// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// CommitHumanStatePrimary orders direct state, native history and their receipt
// atomically against cancellation and expiry under the held conditional owner.
func CommitHumanStatePrimary(ctx context.Context, installation, operation, owner string, now int64, change func(context.Context) (string, error)) (*Operation, error) {
	return commitConditionalPrimary(ctx, installation, operation, owner, now, change)
}

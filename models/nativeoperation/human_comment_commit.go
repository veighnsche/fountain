// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// CommitHumanCommentPrimary atomically records one ordinary comment and its
// receipt against cancellation/expiry under the held native reservation.
// The insert callback must recheck the live human and exact parent in this tx.
func CommitHumanCommentPrimary(ctx context.Context, installation, operation, owner string, now int64, insert func(context.Context) (string, error)) (*Operation, error) {
	return commitConditionalPrimary(ctx, installation, operation, owner, now, insert)
}

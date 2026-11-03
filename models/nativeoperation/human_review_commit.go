// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// CommitHumanReviewPrimary joins the prepared review/history/bookkeeping and
// exact operation receipt, ordered against cancellation/expiry under its owner.
func CommitHumanReviewPrimary(ctx context.Context, installation, operation, owner string, now int64, change func(context.Context) (string, error)) (*Operation, error) {
	return commitConditionalPrimary(ctx, installation, operation, owner, now, change)
}

// CommitHumanInlinePrimary orders the prepared inline/review/history and exact
// receipt together against cancellation/expiry under their held native owner.
func CommitHumanInlinePrimary(ctx context.Context, installation, operation, owner string, now int64, change func(context.Context) (string, error)) (*Operation, error) {
	return commitConditionalPrimary(ctx, installation, operation, owner, now, change)
}

// CommitHumanThreadPrimary orders exact root resolution and its receipt together
// against cancellation and expiry under the same native conditional owner.
func CommitHumanThreadPrimary(ctx context.Context, installation, operation, owner string, now int64, change func(context.Context) (string, error)) (*Operation, error) {
	return commitConditionalPrimary(ctx, installation, operation, owner, now, change)
}

// CommitHumanReviewRequestPrimary joins selected requests, native history and
// exact receipt against cancellation/expiry under the same conditional owner.
func CommitHumanReviewRequestPrimary(ctx context.Context, installation, operation, owner string, now int64, change func(context.Context) (string, error)) (*Operation, error) {
	return commitConditionalPrimary(ctx, installation, operation, owner, now, change)
}

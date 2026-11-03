// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// CommitHumanEditPrimary orders title/body/history changes and their exact
// receipt atomically against cancellation and expiry under the native owner.
func CommitHumanEditPrimary(ctx context.Context, installation, operation, owner string, now int64, edit func(context.Context) (string, error)) (*Operation, error) {
	return commitConditionalPrimary(ctx, installation, operation, owner, now, edit)
}

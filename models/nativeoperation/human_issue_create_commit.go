// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// CommitHumanIssueCreatePrimary atomically allocates the native index, creates
// the issue and records its receipt under the conditional owner. The callback
// must recheck live human and repository admission inside this transaction.
func CommitHumanIssueCreatePrimary(ctx context.Context, installation, operation, owner string, now int64, insert func(context.Context) (string, error)) (*Operation, error) {
	return commitConditionalPrimary(ctx, installation, operation, owner, now, insert)
}

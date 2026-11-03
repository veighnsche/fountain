// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import "context"

// CompleteHumanMerge joins exact immutable receipt/scope/owner locking to the
// existing atomic completion/release owner. Native stage quiescence and domain
// bookkeeping are proved by the service before entering. A failed release rolls
// back completion; this function never repairs or retries native effects.
func CompleteHumanMerge(ctx context.Context, want *Operation, owner string, generation int64, scope, receipt string) (*Operation, error) {
	var recorded *Operation
	_, err := ApplyHumanMergeMetadata(ctx, want, owner, generation, scope, receipt, func(tx context.Context, _ *Operation) (bool, error) {
		var err error
		recorded, err = SetCompletionAndRelease(tx, want.InstallationID, want.OperationID, owner, generation, CompletionComplete)
		return false, err
	})
	if err != nil {
		return nil, err
	}
	return recorded, nil
}

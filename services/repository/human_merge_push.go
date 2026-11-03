// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repository

import (
	"context"

	sdk "forgejo.org/extension-sdk"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

// CompleteHumanMergePush consumes the exact receipt under the native operation's
// one-shot completion claim and runs the same engine as an ordinary queued push.
// It neither queues a second push completion nor releases human ownership.
func CompleteHumanMergePush(ctx context.Context, service *nativeoperation_service.Service, installation string, q sdk.HumanMergeRequest) (sdk.OperationRecord, error) {
	return service.DispatchHumanMergePushEvent(ctx, installation, q, ProcessPushUpdates)
}

// SubmitHumanMerge composes the human operation owner with the single native
// push-event engine. The service performs live admission and receipt-bound
// completion; the repository join supplies no background or merge authority.
func SubmitHumanMerge(ctx context.Context, service *nativeoperation_service.Service, installation string, q sdk.HumanMergeRequest, admit nativeoperation_service.HumanReviewAdmission) (sdk.OperationRecord, error) {
	return service.SubmitHumanMerge(ctx, installation, q, admit, ProcessPushUpdates)
}

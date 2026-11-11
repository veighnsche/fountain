// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"
	"forgejo.org/services/nativeownership"
)

const familyCollaboration = execcontext.FamilyCollaboration

// collabScope uses the shared persisted ownership contract.
type collabScope = execcontext.Scope

// withCollabOwnership claims the idle reservation for one pull-service
// collaboration writer, advancing the revision before its effects, and
// releases it after the writer returns. A busy reservation refuses before
// any effect. Nested calls reuse the enclosing ownership instead of
// claiming again, so writers driven under an existing execution
// (conditional operations, bound hook callbacks, enclosing service
// claims) never claim twice.
func withCollabOwnership(ctx context.Context, resource string, repositoryID int64, fn func(ctx context.Context) error) error {
	return withCollabOwnershipScope(ctx, resource, collabScope{RepositoryID: repositoryID}, fn)
}

func withCollabOwnershipScope(ctx context.Context, resource string, scope collabScope, fn func(context.Context) error) error {
	scope.Kind = nativeoperation_model.OwnerOrdinary
	scope.Family = familyCollaboration
	encoded, err := execcontext.EncodeWriterScope(scope)
	if err != nil {
		return err
	}
	return nativeownership.WithOrdinary(ctx, filepath.Join(setting.AppDataPath, "nativeop-exec"), familyCollaboration, resource, encoded, fn)
}

// Deferred workers may briefly wait for their parent to release ownership.
// This never lends the parent's execution and never retries actual effects.
// Longer contention returns to the durable queue for its normal retry cycle.
func withCollabOwnershipWait(ctx context.Context, resource string, scope collabScope, fn func(context.Context) error) error {
	deadline := time.Now().Add(time.Second)
	backoff := 25 * time.Millisecond
	for {
		entered := false
		err := withCollabOwnershipScope(ctx, resource, scope, func(owned context.Context) error {
			entered = true
			return fn(owned)
		})
		if entered || !errors.Is(err, nativeoperation_model.ErrBusy) || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(backoff, time.Until(deadline))):
		}
		backoff = min(backoff*2, 250*time.Millisecond)
	}
}

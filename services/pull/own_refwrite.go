// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"
	"forgejo.org/services/nativeownership"
)

// refWriteScope retains the pull writer's name for the shared persisted scope.
type refWriteScope = execcontext.Scope

// familyRefWrite names the direct single-ref native writers. It must equal
// the service family of the same writers.
const familyRefWrite = execcontext.FamilyRefWrite

// ErrMergeNotStarted marks reservation contention before the merge callback.
// Deferred automerges may retry this refusal, never an error after entry.
var ErrMergeNotStarted = errors.New("merge refused before effects")

func withMergeRefWriteOwnership(ctx context.Context, resource string, scope refWriteScope, fn func(context.Context) error) error {
	entered := false
	err := withRefWriteOwnership(ctx, resource, scope, func(owned context.Context) error {
		entered = true
		return fn(owned)
	})
	if !entered && errors.Is(err, nativeoperation_model.ErrBusy) {
		return fmt.Errorf("%w: %w", ErrMergeNotStarted, err)
	}
	return err
}

// Ref-write kinds carried in the resource string.
const (
	refWritePRRef    = "pr-ref"
	refWriteMerge    = "merge"
	refWritePRUpdate = "pr-update"
)

// withRefWriteOwnership claims the idle reservation for one pull ref
// writer, advancing the revision before its effects, and releases it after
// the writer returns. A busy reservation refuses before any effect. Nested
// calls reuse the enclosing ownership instead of claiming again, so writers
// driven under an existing execution (conditional merges, bound hook
// callbacks) never claim twice.
func withRefWriteOwnership(ctx context.Context, resource string, scope refWriteScope, fn func(ctx context.Context) error) error {
	scope.Kind = nativeoperation_model.OwnerOrdinary
	scope.Family = familyRefWrite
	encoded, err := execcontext.EncodeWriterScope(scope)
	if err != nil {
		return err
	}
	return nativeownership.WithOrdinary(ctx, filepath.Join(setting.AppDataPath, "nativeop-exec"), familyRefWrite, resource, encoded, fn)
}

// recordRefWriteResult records the realized new OID of the calling ref
// writer's single-ref scope after the effect is computed. It requires the
// caller's own held ownership. Under an enclosing conditional execution the
// conditional owner manages its own scope (including any derived-ref tuple),
// so recording here is a no-op: the narrow ordinary projection must never
// overwrite a conditional scope.
func recordRefWriteResult(ctx context.Context, newOID string) error {
	exec := execcontext.FromContext(ctx)
	if exec == nil || exec.Owner == "" {
		return errors.New("ref result recording requires ownership")
	}
	if execcontext.IsConditionalOwner(exec.Owner) {
		return nil
	}
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return err
	}
	if reservation.Owner != exec.Owner || reservation.Generation != exec.Generation {
		return nativeoperation_model.ErrWrongOwner
	}
	document, err := execcontext.DecodeScopeDocument(reservation.Scope)
	if err != nil {
		return err
	}
	encoded, err := document.WithRefResult(newOID)
	if err != nil {
		return err
	}
	return nativeoperation_model.UpdateScopeExact(ctx, exec.Owner, exec.Generation, document.Original(), encoded)
}

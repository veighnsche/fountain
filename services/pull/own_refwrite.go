// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"
	"forgejo.org/services/nativeownership"
)

// refWriteScope mirrors the service Scope JSON fields the pull ref writers
// record. Unknown fields in either direction are ignored by encoding/json,
// so service-side additions never break this projection.
type refWriteScope struct {
	Kind         string `json:"kind"`
	Family       string `json:"family,omitempty"`
	RepositoryID int64  `json:"repository_id"`
	Ref          string `json:"ref,omitempty"`
	OldOID       string `json:"old_oid,omitempty"`
	NewOID       string `json:"new_oid,omitempty"`
	PRNumber     int64  `json:"pr_number,omitempty"`
}

// familyRefWrite names the direct single-ref native writers. It must equal
// the service family of the same writers.
const familyRefWrite = "ref-write"

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
	encoded, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	return nativeownership.WithOrdinary(ctx, filepath.Join(setting.AppDataPath, "nativeop-exec"), familyRefWrite, resource, string(encoded), fn)
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
	if strings.HasPrefix(exec.Owner, "cond:") {
		return nil
	}
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return err
	}
	if reservation.Owner != exec.Owner {
		return nativeoperation_model.ErrWrongOwner
	}
	// A nested ref write must preserve the enclosing owner's fields,
	// including the PR sweep's bound head branch.
	var scope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(reservation.Scope), &scope); err != nil {
		return err
	}
	if scope == nil {
		return errors.New("ref scope is not an object")
	}
	encodedOID, err := json.Marshal(newOID)
	if err != nil {
		return err
	}
	scope["new_oid"] = encodedOID
	encoded, err := json.Marshal(scope)
	if err != nil {
		return err
	}
	return nativeoperation_model.UpdateScopeWhere(ctx, exec.Owner, string(encoded))
}

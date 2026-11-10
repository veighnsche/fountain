// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"fmt"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/services/nativeownership"
)

const (
	FamilyBranchCreate      = execcontext.FamilyBranchCreate
	FamilyBranchDelete      = execcontext.FamilyBranchDelete
	FamilyActionsTask       = execcontext.FamilyActionsTask
	FamilyPushCompletion    = execcontext.FamilyPushCompletion
	PRCreateCompletionPhase = execcontext.PRCreateCompletionPhase
)

type ScopedRef = execcontext.ScopedRef
type Scope = execcontext.Scope

func conditionalOwner(installationID, operationID string) string {
	return execcontext.ConditionalOwner(installationID, operationID)
}

func encodeScope(scope Scope) (string, error) { return execcontext.EncodeScope(scope) }
func decodeScope(raw string) (Scope, error)   { return execcontext.DecodeScope(raw) }

// WithOrdinaryOwnership claims the idle reservation for one ordinary native
// writer, advancing the revision before its effects, and releases it after
// the writer returns. A busy reservation refuses before any effect. Nested
// calls reuse the enclosing ownership instead of claiming again. A canceled
// context refuses before claiming, and the release runs detached from
// cancellation, so a client disconnect mid-write cannot stick the global
// reservation.
func (s *Service) WithOrdinaryOwnership(ctx context.Context, family, resource string, scope Scope, fn func(ctx context.Context) error) error {
	if execcontext.FromContext(ctx) != nil {
		return fn(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if family == "" || resource == "" {
		return errors.New("ordinary ownership requires a writer identity")
	}
	dir, err := s.execDir()
	if err != nil {
		return err
	}
	scope.Kind = nativeoperation_model.OwnerOrdinary
	scope.Family = family
	encoded, err := encodeScope(scope)
	if err != nil {
		return err
	}
	entered := false
	err = nativeownership.WithOrdinary(ctx, dir, family, resource, encoded, func(owned context.Context) error {
		entered = true
		return fn(owned)
	})
	if !entered && errors.Is(err, nativeoperation_model.ErrBusy) {
		return fmt.Errorf("%w: ordinary writer %s is fenced", ErrBusy, family)
	}
	return err
}

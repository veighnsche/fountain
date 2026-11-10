// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	execcontext "forgejo.org/modules/nativeoperation"
)

// FamilyAuthority is the ordinary writer family for native authority
// changes: users, credentials/keys/auth sources, collaborators,
// organizations and teams, external-directory synchronization and native
// administration. Every mapped authority writer claims this family before
// its effects, advancing the native revision so an old permission
// observation cannot authorize a competing conditional write.
const FamilyAuthority = execcontext.FamilyAuthority

type AuthorityTarget = execcontext.AuthorityTarget

// CrashPointAuthorityAfterEffects pauses one top-level authority writer
// after its effects commit and before its owner releases. It is a
// disclosed test instrument for the interrupted authority-writer recovery
// proof; production never sets the barrier variables.
const CrashPointAuthorityAfterEffects = "authority-after-effects"

// WithAuthorityOwnership claims the idle reservation for one ordinary
// authority writer and releases it after the writer returns. A busy
// reservation or offline inhibition refuses before any effect. Nested
// calls reuse the enclosing ownership without claiming again and without
// pausing at the crash barrier: only the top-level authority claim can
// hold the barrier point.
func WithAuthorityOwnership(ctx context.Context, resource string, repositoryID int64, fn func(ctx context.Context) error) error {
	target, _ := execcontext.ParseAuthorityResource(resource)
	return WithAuthorityOwnershipFor(ctx, resource, repositoryID, target, fn)
}

// WithAuthorityOwnershipFor binds recovery identity independently of a writer's diagnostic label.
func WithAuthorityOwnershipFor(ctx context.Context, resource string, repositoryID int64, target AuthorityTarget, fn func(ctx context.Context) error) error {
	if execcontext.FromContext(ctx) != nil {
		return fn(ctx)
	}
	scope := Scope{
		RepositoryID: repositoryID,
		AuthorityOp:  target.Operation,
		AuthorityID:  target.ID,
		AuthorityID2: target.ID2,
	}
	return Default().WithOrdinaryOwnership(ctx, FamilyAuthority, resource, scope, func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return err
		}
		return TestCrashBarrier(CrashPointAuthorityAfterEffects)
	})
}

// parseAuthorityResource splits one structured authority resource label
// ("team/5/member/9") into its operation ("team/member") and entity IDs.
// Name-based create labels and multi-entity batch labels carry no
// attributable operation and report ok=false: their writers still order
// before their effects, but offline recovery leaves them fenced.
func parseAuthorityResource(resource string) (op string, id1, id2 int64, ok bool) {
	target, ok := execcontext.ParseAuthorityResource(resource)
	return target.Operation, target.ID, target.ID2, ok
}

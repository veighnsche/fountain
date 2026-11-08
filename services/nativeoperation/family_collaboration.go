// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	execcontext "forgejo.org/modules/nativeoperation"
)

// FamilyCollaboration is the ordinary writer family for native
// collaboration inputs: issues, comments, dependencies, PR metadata,
// reviews and conversations, plus the labels, milestones, assignees,
// locks, pins, project assignments and content-history records that
// version them or gate eligibility. Every mapped collaboration writer
// claims this family before its effects, advancing the native revision
// so an old accepted-input observation cannot authorize a competing
// conditional write. Native text and relations stay canonical; this
// family adds no approval fields and interprets no factory decisions.
const FamilyCollaboration = execcontext.FamilyCollaboration

// CrashPointCollaborationAfterEffects pauses one top-level collaboration
// writer after its effects commit and before its owner releases. It is a
// disclosed test instrument for the interrupted collaboration-writer
// recovery proof; production never sets the barrier variables.
const CrashPointCollaborationAfterEffects = "collaboration-after-effects"

// WithCollaborationOwnership claims the idle reservation for one ordinary
// collaboration writer and releases it after the writer returns. A busy
// reservation or offline inhibition refuses before any effect. Nested
// calls reuse the enclosing ownership without claiming again and without
// pausing at the crash barrier: only the top-level collaboration claim
// can hold the barrier point.
func WithCollaborationOwnership(ctx context.Context, resource string, repositoryID int64, fn func(ctx context.Context) error) error {
	if execcontext.FromContext(ctx) != nil {
		return fn(ctx)
	}
	scope := Scope{RepositoryID: repositoryID}
	return Default().WithOrdinaryOwnership(ctx, FamilyCollaboration, resource, scope, func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return err
		}
		return TestCrashBarrier(CrashPointCollaborationAfterEffects)
	})
}

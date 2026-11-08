// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"strings"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
)

// BoundCallbackContext binds one synchronous native callback (post-receive,
// proc-receive or default-branch bookkeeping) to the held reservation
// owner. It verifies the host-private execution proof against the durable
// owner and returns a context carrying that execution for nested
// participating writers; nested writers reuse the enclosing ownership
// instead of claiming again. An idle reservation returns the context
// unchanged so ordinary callbacks keep existing behavior. A held
// reservation without a matching proof refuses before any effect: callbacks
// reenter only with host-authenticated execution binding and never borrow
// another owner's authority. The capability path is attached only when the
// file at that path holds the same secret, so bound grandchildren of the
// callback keep proving the same execution.
func BoundCallbackContext(ctx context.Context, proof, capabilityPath string) (context.Context, error) {
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return nil, err
	}
	if reservation.Owner == "" {
		return ctx, nil
	}
	if !execcontext.VerifyProof(reservation.Verifier, proof) {
		return nil, nativeoperation_model.ErrBusy
	}
	path := ""
	if capabilityPath != "" {
		if secret, err := execcontext.ReadCapabilityFile(capabilityPath); err == nil && secret == proof {
			path = capabilityPath
		}
	}
	return execcontext.NewContext(ctx, &execcontext.Execution{
		Owner:          reservation.Owner,
		Generation:     reservation.Generation,
		CapabilityPath: path,
	}), nil
}

// RefineReceiveScope records the ref tuples a bound receive callback
// observed on the held receive owner, so offline recovery reconciles that
// family's realized ref effects and the ordinary gate admits the owner's
// own refs. It refines only receive-family holders when the proof
// matches; anything else is a silent no-op and the caller keeps its
// existing behavior.
func RefineReceiveScope(ctx context.Context, proof string, refs []ScopedRef) {
	if proof == "" || len(refs) == 0 {
		return
	}
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil || reservation.Owner == "" {
		return
	}
	if !execcontext.VerifyProof(reservation.Verifier, proof) {
		return
	}
	scope, err := decodeScope(reservation.Scope)
	if err != nil || (scope.Family != FamilyReceiveHTTP && scope.Family != FamilyReceiveSSH) {
		return
	}
	_ = UpdateScopeRefs(ctx, reservation.Owner, refs)
}

// RefineCollabPullScope records the derived PR ref a pull-creation owner
// realizes. The PR index (hence the ref name) is unknowable at claim
// time, and the nested ref write reuses the enclosing collaboration
// execution instead of claiming again, so the held scope cannot name the
// ref up front. It refines only pull/new holders for refs/pull/* effects
// with a matching proof; anything else is a silent no-op and the caller
// keeps its existing behavior. The gate's repository check still applies,
// and offline recovery keeps fencing the multi-statement creation for
// intervention.
func RefineCollabPullScope(ctx context.Context, proof string, refs []ScopedRef) {
	if proof == "" || len(refs) == 0 {
		return
	}
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil || reservation.Owner == "" {
		return
	}
	if !execcontext.VerifyProof(reservation.Verifier, proof) {
		return
	}
	family, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
	if !ok || family != FamilyCollaboration || !strings.HasPrefix(resource, "pull/new/") {
		return
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Ref, "refs/pull/") {
			return
		}
	}
	if _, err := decodeScope(reservation.Scope); err != nil {
		return
	}
	_ = UpdateScopeRefs(ctx, reservation.Owner, refs)
}

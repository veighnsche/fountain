// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"strings"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/modules/log"
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
	if _, err := RefineReceiveScopeChecked(ctx, proof, refs); err != nil {
		log.Error("Receive scope refinement failed: %v", err)
	}
}

// RefinementResult distinguishes an unrelated owner from a persisted refinement.
type RefinementResult uint8

const (
	RefinementNotApplicable RefinementResult = iota
	RefinementUpdated
)

// RefineReceiveScopeChecked reports persistence and proof failures before hook admission.
func RefineReceiveScopeChecked(ctx context.Context, proof string, refs []ScopedRef) (RefinementResult, error) {
	if proof == "" || len(refs) == 0 {
		return RefinementNotApplicable, nil
	}
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return RefinementNotApplicable, err
	}
	if reservation.Owner == "" {
		return RefinementNotApplicable, nil
	}
	document, err := execcontext.DecodeScopeDocument(reservation.Scope)
	if err != nil {
		return RefinementNotApplicable, err
	}
	if family := document.Scope().Family; family != FamilyReceiveHTTP && family != FamilyReceiveSSH {
		return RefinementNotApplicable, nil
	}
	if !execcontext.VerifyProof(reservation.Verifier, proof) {
		return RefinementNotApplicable, nativeoperation_model.ErrBusy
	}
	return refineScopeRefs(ctx, reservation, document, refs)
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
	if _, err := RefineCollabPullScopeChecked(ctx, proof, refs); err != nil {
		log.Error("Pull scope refinement failed: %v", err)
	}
}

// RefineCollabPullScopeChecked reports failures while binding a derived pull ref.
func RefineCollabPullScopeChecked(ctx context.Context, proof string, refs []ScopedRef) (RefinementResult, error) {
	if proof == "" || len(refs) == 0 {
		return RefinementNotApplicable, nil
	}
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return RefinementNotApplicable, err
	}
	if reservation.Owner == "" {
		return RefinementNotApplicable, nil
	}
	family, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
	if !ok || family != FamilyCollaboration {
		return RefinementNotApplicable, nil
	}
	claim, err := execcontext.ParseCollabResource(resource)
	if err != nil || claim.Kind != "pull" || claim.Op != "create" {
		return RefinementNotApplicable, nil
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Ref, "refs/pull/") {
			return RefinementNotApplicable, nil
		}
	}
	if !execcontext.VerifyProof(reservation.Verifier, proof) {
		return RefinementNotApplicable, nativeoperation_model.ErrBusy
	}
	document, err := execcontext.DecodeScopeDocument(reservation.Scope)
	if err != nil {
		return RefinementNotApplicable, err
	}
	return refineScopeRefs(ctx, reservation, document, refs)
}

func refineScopeRefs(ctx context.Context, reservation *nativeoperation_model.Reservation, document *execcontext.ScopeDocument, refs []ScopedRef) (RefinementResult, error) {
	encoded, err := document.UpdateRefs(refs)
	if err != nil {
		return RefinementNotApplicable, err
	}
	if err := nativeoperation_model.UpdateScopeExact(ctx, reservation.Owner, reservation.Generation, document.Original(), encoded); err != nil {
		return RefinementNotApplicable, err
	}
	return RefinementUpdated, nil
}

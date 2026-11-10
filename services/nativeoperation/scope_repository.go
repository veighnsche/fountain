// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
)

// RecordScopeRefResult records the realized new OID of a single-ref
// ordinary scope for the owner carried by ctx. It refuses when the context
// carries no execution: result recording without ownership hides a missing
// claim.
func RecordScopeRefResult(ctx context.Context, newOID string) error {
	exec := execcontext.FromContext(ctx)
	if exec == nil || exec.Owner == "" {
		return errors.New("ref result recording requires ownership")
	}
	return UpdateScopeRefResult(ctx, exec.Owner, newOID)
}

// UpdateScopeRefResult records the realized new OID of a single-ref
// ordinary scope after the effect is computed but before it is pushed. Only
// the holding owner records; anything else refuses with ErrWrongOwner and
// the scope is untouched.
func UpdateScopeRefResult(ctx context.Context, owner, newOID string) error {
	if owner == "" || newOID == "" {
		return nativeoperation_model.ErrWrongOwner
	}
	reservation, document, err := scopeDocumentForOwner(ctx, owner)
	if err != nil {
		return err
	}
	if previous := document.Scope().NewOID; previous != "" && previous != newOID {
		return nativeoperation_model.ErrWrongOwner
	}
	encoded, err := document.WithNewOID(newOID)
	if err != nil {
		return err
	}
	return nativeoperation_model.UpdateScopeExact(ctx, owner, reservation.Generation, document.Original(), encoded)
}

// UpdateScopeRefs records realized end states for refs in the held owner's
// scope. Entries recorded before the effect keep their old OID while the
// new OID is set; refs without an entry are added with an empty old OID.
// Only the holding owner records; anything else refuses with ErrWrongOwner
// and the scope is untouched.
func UpdateScopeRefs(ctx context.Context, owner string, refs []ScopedRef) error {
	if owner == "" || len(refs) == 0 {
		return nativeoperation_model.ErrWrongOwner
	}
	reservation, document, err := scopeDocumentForOwner(ctx, owner)
	if err != nil {
		return err
	}
	encoded, err := document.UpdateRefs(refs)
	if err != nil {
		return err
	}
	return nativeoperation_model.UpdateScopeExact(ctx, owner, reservation.Generation, document.Original(), encoded)
}

// AppendScopeRefs unions observed ref effects into the scope of the
// currently held owner: prepared tuples, fetch results or realized OIDs
// that were unknowable at claim time. Only the holding owner appends;
// anything else refuses with ErrWrongOwner and the scope is untouched. A
// contradicting tuple for an already recorded ref refuses without
// changing the scope.
func AppendScopeRefs(ctx context.Context, owner string, refs []ScopedRef) error {
	if owner == "" || len(refs) == 0 {
		return nativeoperation_model.ErrWrongOwner
	}
	reservation, document, err := scopeDocumentForOwner(ctx, owner)
	if err != nil {
		return err
	}
	encoded, err := document.AppendRefs(refs)
	if err != nil {
		if errors.Is(err, execcontext.ErrScopeConflict) {
			return nativeoperation_model.ErrWrongOwner
		}
		return err
	}
	return nativeoperation_model.UpdateScopeExact(ctx, owner, reservation.Generation, document.Original(), encoded)
}

func scopeDocumentForOwner(ctx context.Context, owner string) (*nativeoperation_model.Reservation, *execcontext.ScopeDocument, error) {
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return nil, nil, err
	}
	if reservation.Owner != owner {
		return nil, nil, nativeoperation_model.ErrWrongOwner
	}
	if execution := execcontext.FromContext(ctx); execution != nil && (execution.Owner != owner || execution.Generation != reservation.Generation) {
		return nil, nil, nativeoperation_model.ErrWrongOwner
	}
	document, err := execcontext.DecodeScopeDocument(reservation.Scope)
	if err != nil {
		return nil, nil, err
	}
	return reservation, document, nil
}

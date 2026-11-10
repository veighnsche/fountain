// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
	execcontext "forgejo.org/modules/nativeoperation"
)

type OrdinaryStore struct{}

func (OrdinaryStore) Claim(ctx context.Context, owner, scope, verifier string) (int64, error) {
	claimed, err := ClaimOrdinary(ctx, owner, scope, verifier)
	if err != nil {
		return 0, err
	}
	return claimed.Generation, nil
}

func (OrdinaryStore) Release(ctx context.Context, owner string, generation int64) error {
	reservation, err := ReadReservation(ctx)
	if err != nil {
		return err
	}
	if reservation.Owner != owner || reservation.Generation != generation {
		return ErrWrongOwner
	}
	document, err := execcontext.DecodeScopeDocument(reservation.Scope)
	if err != nil {
		return err
	}
	if document.Scope().NativeRename != nil {
		return execcontext.ErrIndeterminateEffect
	}
	affected, err := db.GetEngine(ctx).Where(scopeWitnessCondition(), owner, generation, reservation.Scope).Cols("owner", "owner_kind", "verifier", "scope").Update(&Reservation{})
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrWrongOwner
	}
	return nil
}

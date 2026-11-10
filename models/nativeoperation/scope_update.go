// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
	"forgejo.org/modules/setting"
)

// UpdateScopeWhere rewrites the held reservation's scope JSON only while
// owner still holds it. Owners record observed ref effects (prepared
// tuples, fetch results, realized OIDs) that were unknowable at claim
// time; anything else refuses with ErrWrongOwner and the scope is
// untouched. The mutation must preserve the scope's kind, family and
// repository identity.
func UpdateScopeWhere(ctx context.Context, owner, scopeJSON string) error {
	if owner == "" || scopeJSON == "" {
		return ErrWrongOwner
	}
	affected, err := db.GetEngine(ctx).Where("id=1 AND owner=?", owner).Cols("scope").Update(&Reservation{Scope: scopeJSON})
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrWrongOwner
	}
	return nil
}

// UpdateScopeExact refines one observed witness without normalizing it or
// replacing another generation's scope, including a concurrent refinement.
func UpdateScopeExact(ctx context.Context, owner string, generation int64, before, after string) error {
	if owner == "" || generation <= 0 || before == "" || after == "" {
		return ErrWrongOwner
	}
	if before == after {
		matched, err := db.GetEngine(ctx).Where(scopeWitnessCondition(), owner, generation, before).Exist(new(Reservation))
		if err != nil {
			return err
		}
		if !matched {
			return ErrWrongOwner
		}
		return nil
	}
	affected, err := db.GetEngine(ctx).Where(scopeWitnessCondition(), owner, generation, before).Cols("scope").Update(&Reservation{Scope: after})
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrWrongOwner
	}
	return nil
}

func scopeWitnessCondition() string {
	ownerMatch, scopeMatch := "CAST(owner AS BLOB)=CAST(? AS BLOB)", "CAST(scope AS BLOB)=CAST(? AS BLOB)"
	if setting.Database.Type.IsMySQL() {
		ownerMatch, scopeMatch = "CAST(owner AS BINARY)=CAST(? AS BINARY)", "CAST(scope AS BINARY)=CAST(? AS BINARY)"
	} else if setting.Database.Type.IsPostgreSQL() {
		ownerMatch, scopeMatch = "convert_to(owner,'UTF8')=convert_to(?,'UTF8')", "convert_to(scope,'UTF8')=convert_to(?,'UTF8')"
	}
	return "id=1 AND " + ownerMatch + " AND generation=? AND " + scopeMatch
}

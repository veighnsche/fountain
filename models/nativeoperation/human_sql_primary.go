// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// Shared exact human capture/generation lock for SQL-primary consumers.
func lockHumanSQLPrimaryOperation(ctx context.Context, want *Operation, owner string, generation int64, scope, kind string) (*Operation, error) {
	if want == nil || want.Kind != kind || kind != KindHumanManualMerge && kind != KindHumanPullCreate || want.TokenID != 0 || want.CredentialFingerprint != "" || owner == "" || generation < 1 || scope == "" || want.ExpectedNativeRevision < 1 {
		return nil, ErrWrongOwner
	}
	n, err := db.GetEngine(ctx).Where("id=1 AND owner=? AND owner_kind=? AND generation=? AND revision=? AND scope=?", owner, OwnerConditional, generation, want.ExpectedNativeRevision+1, scope).Cols("scope").Update(&Reservation{Scope: scope})
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrWrongOwner
	}
	op, err := LookupOperation(ctx, want.InstallationID, want.OperationID)
	if err != nil {
		return nil, err
	}
	if op == nil || !op.Submitted || op.Kind != want.Kind || op.ActorID != want.ActorID || op.RepositoryID != want.RepositoryID || op.AuthRevision != want.AuthRevision || op.ExpectedNativeRevision != want.ExpectedNativeRevision || op.NotAfter != want.NotAfter || op.Intent != want.Intent || op.IntentDigest != want.IntentDigest || op.TokenID != 0 || op.CredentialFingerprint != "" {
		return nil, ErrAdmissionLost
	}
	return op, nil
}

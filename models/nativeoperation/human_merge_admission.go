// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
)

// The service validates the canonical SDK envelope. Both the scope binding and
// final admission also compare its persisted immutable columns inside their tx.
func sameHumanMergeCapture(op, want *Operation) bool {
	return sameHumanMergeIntent(op, want) && op.EffectState == EffectPending && op.Receipt == "" && op.Completion == ""
}

func sameHumanMergeIntent(op, want *Operation) bool {
	return op != nil && want != nil && want.Kind == KindHumanMerge && op.Kind == want.Kind && op.Submitted &&
		op.InstallationID == want.InstallationID && op.OperationID == want.OperationID &&
		op.ActorID == want.ActorID && op.RepositoryID == want.RepositoryID &&
		op.AuthRevision == want.AuthRevision && op.ExpectedNativeRevision == want.ExpectedNativeRevision &&
		op.NotAfter == want.NotAfter && op.Intent == want.Intent && op.IntentDigest == want.IntentDigest &&
		op.TokenID == 0 && op.CredentialFingerprint == ""
}

// This CAS locks the exact generation/scope until its transaction ends. A newer
// owner, changed scope or restarted generation cannot be admitted by a stale
// prepared callback. Setting the same scope at admission retains the row lock.
func lockHumanMergeScope(ctx context.Context, want *Operation, owner string, generation int64, before, after string) error {
	if want == nil || owner == "" || generation < 1 || before == "" || after == "" || want.ExpectedNativeRevision < 1 {
		return ErrWrongOwner
	}
	n, err := db.GetEngine(ctx).Where("id=1 AND owner=? AND owner_kind=? AND generation=? AND revision=? AND scope=?", owner, OwnerConditional, generation, want.ExpectedNativeRevision+1, before).
		Cols("scope").Update(&Reservation{Scope: after})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrWrongOwner
	}
	return nil
}

// BindHumanMergeScope binds the server-computed result once, from the exact
// unprepared scope. This records no admission and does not authorize a ref write.
func BindHumanMergeScope(ctx context.Context, want *Operation, owner string, generation int64, before, after string, now int64) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		if before == after {
			return ErrAdmissionLost
		}
		if err := lockHumanMergeScope(ctx, want, owner, generation, before, after); err != nil {
			return err
		}
		op, err := LookupOperation(ctx, want.InstallationID, want.OperationID)
		if err != nil {
			return err
		}
		if !sameHumanMergeCapture(op, want) || op.Revoked || op.Admitted {
			return ErrAdmissionLost
		}
		if op.NotAfter <= now {
			return ErrOperationExpired
		}
		return nil
	})
}

// RecordHumanMergeAdmission retains the exact scope/generation lock and reuses
// the existing atomic cancellation/duplicate ordering. It never rolls back a
// recorded lost attempt merely to return its refusal as an error.
func RecordHumanMergeAdmission(ctx context.Context, want *Operation, owner string, generation int64, scope string, now int64, admit bool, reason string) (recorded *Operation, admitted bool, err error) {
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if err := lockHumanMergeScope(ctx, want, owner, generation, scope, scope); err != nil {
			return err
		}
		op, err := LookupOperation(ctx, want.InstallationID, want.OperationID)
		if err != nil {
			return err
		}
		if !sameHumanMergeCapture(op, want) {
			return ErrAdmissionLost
		}
		if admit && now >= op.NotAfter {
			admit, reason = false, ReasonExpiredBeforeAdmission
		}
		recorded, admitted, err = RecordAdmissionAttempt(ctx, want.InstallationID, want.OperationID, owner, admit, reason)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return recorded, admitted, nil
}

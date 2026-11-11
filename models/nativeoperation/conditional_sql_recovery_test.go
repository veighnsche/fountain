// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation_test

import (
	"context"
	"errors"
	"os"
	"testing"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/setting"

	"github.com/stretchr/testify/require"
)

func conditionalSQLRecoveryCapture(t *testing.T, kind string) (*nativeoperation_model.Operation, *nativeoperation_model.Reservation) {
	t.Helper()
	unittest.PrepareTestEnv(t)
	before := setting.AppDataPath
	setting.AppDataPath = t.TempDir()
	t.Cleanup(func() { setting.AppDataPath = before })
	idle, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	op := testOperation("recover-atomic-sql", idle.Revision)
	op.Kind = kind
	held, err := nativeoperation_model.ClaimConditional(t.Context(), op, "cond:"+testInstallation+"/"+op.OperationID, `{"kind":"conditional"}`, "fixture-verifier")
	require.NoError(t, err)
	op, err = nativeoperation_model.LookupOperation(t.Context(), op.InstallationID, op.OperationID)
	require.NoError(t, err)
	return op, held
}

func TestConditionalSQLRecoveryRequiresExactStoppedCapture(t *testing.T) {
	op, held := conditionalSQLRecoveryCapture(t, nativeoperation_model.KindLifecycleNote)
	inspected := false
	inspect := func(context.Context, *nativeoperation_model.Operation) (string, error) {
		inspected = true
		return nativeoperation_model.CompletionComplete, nil
	}
	_, err := nativeoperation_model.RecoverConditionalSQLPrimary(t.Context(), op, held.Owner, held.Generation, held.Scope, inspect)
	require.ErrorIs(t, err, nativeoperation_model.ErrInhibited)
	require.NoError(t, os.WriteFile(nativeoperation_model.OfflineMarkerPath(), []byte("stopped\n"), 0o600))
	for _, tc := range []struct {
		owner      string
		generation int64
		scope      string
	}{
		{held.Owner + "-other", held.Generation, held.Scope},
		{held.Owner, held.Generation + 1, held.Scope},
		{held.Owner, held.Generation, held.Scope + " "},
	} {
		_, err := nativeoperation_model.RecoverConditionalSQLPrimary(t.Context(), op, tc.owner, tc.generation, tc.scope, inspect)
		require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
	}
	changed := *op
	changed.IntentDigest = "another-capture"
	_, err = nativeoperation_model.RecoverConditionalSQLPrimary(t.Context(), &changed, held.Owner, held.Generation, held.Scope, inspect)
	require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
	require.False(t, inspected)
	stillHeld, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	require.Equal(t, held.Owner, stillHeld.Owner)
	require.Equal(t, held.Generation, stillHeld.Generation)
}

func TestConditionalSQLRecoveryProvesCancelledPrePrimaryWithoutEffects(t *testing.T) {
	for _, kind := range []string{nativeoperation_model.KindThreadResolve, nativeoperation_model.KindLifecycleIssue, nativeoperation_model.KindLifecycleReview, nativeoperation_model.KindLifecycleNote} {
		t.Run(kind, func(t *testing.T) {
			op, held := conditionalSQLRecoveryCapture(t, kind)
			op, err := nativeoperation_model.RevokeOperation(t.Context(), op.InstallationID, op.OperationID)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(nativeoperation_model.OfflineMarkerPath(), []byte("stopped\n"), 0o600))
			recovered, err := nativeoperation_model.RecoverConditionalSQLPrimary(t.Context(), op, held.Owner, held.Generation, held.Scope,
				func(context.Context, *nativeoperation_model.Operation) (string, error) {
					t.Fatal("pre-primary recovery must not run native completion or effect inspection")
					return "", nil
				})
			require.NoError(t, err)
			require.Equal(t, nativeoperation_model.EffectNotCommitted, recovered.EffectState)
			require.Equal(t, nativeoperation_model.CancellationCancelled, recovered.Cancellation)
			require.Empty(t, recovered.Receipt)
			idle, err := nativeoperation_model.ReadReservation(t.Context())
			require.NoError(t, err)
			require.Empty(t, idle.Owner)
			require.Equal(t, held.Revision, idle.Revision)
		})
	}
}

func TestConditionalSQLRecoveryInspectionFailureKeepsPrimaryFence(t *testing.T) {
	op, held := conditionalSQLRecoveryCapture(t, nativeoperation_model.KindLifecycleNote)
	committed, err := nativeoperation_model.CommitLifecycleNotePrimary(t.Context(), op.InstallationID, op.OperationID, held.Owner, 1900000000,
		func(context.Context) (string, error) { return `{"native":"primary-receipt"}`, nil })
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(nativeoperation_model.OfflineMarkerPath(), []byte("stopped\n"), 0o600))
	missingEvidence := errors.New("missing exact native primary evidence")
	_, err = nativeoperation_model.RecoverConditionalSQLPrimary(t.Context(), committed, held.Owner, held.Generation, held.Scope,
		func(context.Context, *nativeoperation_model.Operation) (string, error) { return "", missingEvidence })
	require.ErrorIs(t, err, missingEvidence)
	fresh, err := nativeoperation_model.LookupOperation(t.Context(), op.InstallationID, op.OperationID)
	require.NoError(t, err)
	require.Equal(t, committed.Receipt, fresh.Receipt)
	require.Equal(t, nativeoperation_model.CompletionPending, fresh.Completion)
	stillHeld, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	require.Equal(t, held.Owner, stillHeld.Owner)
	require.Equal(t, held.Generation, stillHeld.Generation)
}

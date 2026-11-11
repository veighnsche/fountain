// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation_test

import (
	"context"
	"os"
	"testing"

	"forgejo.org/models/db"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

type humanSQLProfile struct {
	kind        string
	commit      func(context.Context, *nativeoperation_model.Operation, string, int64, string, int64, func(context.Context) (string, error)) (*nativeoperation_model.Operation, error)
	refuse      func(context.Context, *nativeoperation_model.Operation, string, int64, string, string) (*nativeoperation_model.Operation, error)
	withReceipt func(context.Context, *nativeoperation_model.Operation, string, int64, string, string, func(context.Context, *nativeoperation_model.Operation) error) error
	phase       func(context.Context, *nativeoperation_model.Operation, string, int64, string, string, string) error
	finish      func(context.Context, *nativeoperation_model.Operation, string, int64, string, string, string, bool) (*nativeoperation_model.Operation, error)
	complete    func(context.Context, *nativeoperation_model.Operation, string, int64, string, string) (*nativeoperation_model.Operation, error)
	recover     func(context.Context, *nativeoperation_model.Operation, string, int64, string, func(context.Context, *nativeoperation_model.Operation) (string, error)) (*nativeoperation_model.Operation, error)
}

func humanSQLProfiles() []humanSQLProfile {
	return []humanSQLProfile{
		{nativeoperation_model.KindHumanManualMerge, nativeoperation_model.CommitHumanManualMergePrimary,
			nativeoperation_model.RefuseHumanManualMerge, nativeoperation_model.WithHumanManualMergeReceipt, nativeoperation_model.ClaimHumanManualMergeFinish,
			nativeoperation_model.RecordHumanManualMergeFinish, nativeoperation_model.CompleteHumanManualMerge, nativeoperation_model.RecoverHumanManualMerge},
		{nativeoperation_model.KindHumanPullCreate, nativeoperation_model.CommitHumanPullCreatePrimary,
			nativeoperation_model.RefuseHumanPullCreate, nativeoperation_model.WithHumanPullCreateReceipt,
			func(ctx context.Context, op *nativeoperation_model.Operation, owner string, generation int64, before, after, receipt string) error {
				_, err := nativeoperation_model.ChangeHumanPullCreatePhase(ctx, op, owner, generation, before, after, receipt)
				return err
			}, nativeoperation_model.RecordHumanPullCreateFinish, nativeoperation_model.CompleteHumanPullCreate, nativeoperation_model.RecoverHumanPullCreate},
	}
}

func claimHumanSQLProfile(t *testing.T, profile humanSQLProfile) (*nativeoperation_model.Operation, string, int64) {
	t.Helper()
	reservation, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	want := testOperation("human-sql", reservation.Revision)
	want.Kind, want.TokenID, want.CredentialFingerprint = profile.kind, 0, ""
	owner := "cond:" + want.InstallationID + "/" + want.OperationID
	claimed, err := nativeoperation_model.ClaimConditional(t.Context(), want, owner, "primary", "verifier")
	require.NoError(t, err)
	return want, owner, claimed.Generation
}

func TestHumanSQLProfilesPreserveCancellationAndCapturedPhases(t *testing.T) {
	for _, profile := range humanSQLProfiles() {
		t.Run(profile.kind, func(t *testing.T) {
			unittest.PrepareTestEnv(t)
			want, owner, generation := claimHumanSQLProfile(t, profile)
			_, err := profile.commit(t.Context(), want, owner, generation, "primary", 1000000000, func(tx context.Context) (string, error) {
				require.True(t, db.InTransaction(tx))
				return "exact receipt", nil
			})
			require.NoError(t, err)
			_, err = nativeoperation_model.RevokeOperation(t.Context(), want.InstallationID, want.OperationID)
			require.NoError(t, err)
			called := false
			consume := func(context.Context, *nativeoperation_model.Operation) error { called = true; return nil }
			require.ErrorIs(t, profile.withReceipt(t.Context(), want, owner, generation+1, "primary", "exact receipt", consume), nativeoperation_model.ErrWrongOwner)
			require.False(t, called)
			changed := *want
			changed.IntentDigest += "different"
			require.ErrorIs(t, profile.withReceipt(t.Context(), &changed, owner, generation, "primary", "exact receipt", consume), nativeoperation_model.ErrAdmissionLost)
			require.False(t, called)
			require.ErrorIs(t, profile.phase(t.Context(), want, owner, generation, "primary", "primary", "exact receipt"), nativeoperation_model.ErrAdmissionLost)
			require.NoError(t, profile.phase(t.Context(), want, owner, generation, "primary", "started", "exact receipt"))
			require.Error(t, profile.phase(t.Context(), want, owner, generation, "primary", "started", "exact receipt"), "a one-shot phase cannot be claimed twice")
			_, err = profile.finish(t.Context(), want, owner, generation, "started", "done", "exact receipt", true)
			require.NoError(t, err)
			completed, err := profile.complete(t.Context(), want, owner, generation, "done", "exact receipt")
			require.NoError(t, err)
			require.Equal(t, nativeoperation_model.EffectCommitted, completed.EffectState)
			require.Equal(t, nativeoperation_model.CompletionComplete, completed.Completion)
			require.Equal(t, "exact receipt", completed.Receipt)
			_, err = profile.complete(t.Context(), want, owner, generation, "done", "exact receipt")
			require.Error(t, err, "released proof never reopens a completion")
		})
	}
}

func TestHumanSQLProfilesRefuseCancelledPrimaryAndRecoverWithoutReplay(t *testing.T) {
	for _, profile := range humanSQLProfiles() {
		t.Run(profile.kind, func(t *testing.T) {
			unittest.PrepareTestEnv(t)
			useIsolatedAppData(t)
			want, owner, generation := claimHumanSQLProfile(t, profile)
			_, err := nativeoperation_model.RevokeOperation(t.Context(), want.InstallationID, want.OperationID)
			require.NoError(t, err)
			called := false
			_, err = profile.commit(t.Context(), want, owner, generation, "primary", 1000000000, func(context.Context) (string, error) { called = true; return "must not commit", nil })
			require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
			require.False(t, called)
			require.NoError(t, os.WriteFile(nativeoperation_model.OfflineMarkerPath(), []byte("stopped\n"), 0o600))
			recovered, err := profile.recover(t.Context(), want, owner, generation, "primary", func(tx context.Context, current *nativeoperation_model.Operation) (string, error) {
				require.True(t, db.InTransaction(tx))
				require.True(t, current.Revoked)
				return "", nil
			})
			require.NoError(t, err)
			require.Equal(t, nativeoperation_model.EffectNotCommitted, recovered.EffectState)
			require.Equal(t, nativeoperation_model.CancellationCancelled, recovered.Cancellation)
			require.Empty(t, recovered.Receipt)
			reservation, err := nativeoperation_model.ReadReservation(t.Context())
			require.NoError(t, err)
			require.Empty(t, reservation.Owner)
		})
	}
}

func TestHumanSQLProfilesKeepUncertainCompletionClaimed(t *testing.T) {
	for _, profile := range humanSQLProfiles() {
		t.Run(profile.kind, func(t *testing.T) {
			unittest.PrepareTestEnv(t)
			useIsolatedAppData(t)
			want, owner, generation := claimHumanSQLProfile(t, profile)
			_, err := profile.commit(t.Context(), want, owner, generation, "primary", 1000000000, func(context.Context) (string, error) { return "exact receipt", nil })
			require.NoError(t, err)
			require.NoError(t, profile.phase(t.Context(), want, owner, generation, "primary", "started", "exact receipt"))
			failed, err := profile.finish(t.Context(), want, owner, generation, "started", "done", "exact receipt", false)
			require.NoError(t, err)
			require.Equal(t, nativeoperation_model.CompletionNeedsIntervention, failed.Completion)
			_, err = profile.refuse(t.Context(), want, owner, generation, "started", "must not erase committed truth")
			require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
			_, err = profile.complete(t.Context(), want, owner, generation, "started", "exact receipt")
			require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
			reservation, err := nativeoperation_model.ReadReservation(t.Context())
			require.NoError(t, err)
			require.Equal(t, owner, reservation.Owner, "uncertain external finishing keeps its exact claim")
			require.NoError(t, os.WriteFile(nativeoperation_model.OfflineMarkerPath(), []byte("stopped\n"), 0o600))
			recovered, err := profile.recover(t.Context(), want, owner, generation, "started", func(context.Context, *nativeoperation_model.Operation) (string, error) {
				return nativeoperation_model.CompletionNeedsIntervention, nil
			})
			require.NoError(t, err)
			require.Equal(t, nativeoperation_model.EffectCommitted, recovered.EffectState)
			require.Equal(t, "exact receipt", recovered.Receipt)
			require.Equal(t, nativeoperation_model.CompletionNeedsIntervention, recovered.Completion)
		})
	}
}

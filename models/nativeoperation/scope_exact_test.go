// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestUpdateScopeExactRequiresByteExactWitness(t *testing.T) {
	const owner = "ord:branch/7/Main/proof"
	const before = `{ "kind":"ordinary", "future":"Witness", "number":1e+09 }`
	const after = `{ "kind":"ordinary", "future":"Witness", "number":1e+09, "new_oid":"allocated" }`
	for _, test := range []struct {
		name, owner, before, after string
		generationOffset           int64
		valid                      bool
	}{
		{"exact update", owner, before, after, 0, true},
		{"exact no-op", owner, before, before, 0, true},
		{"owner case differs", strings.ToLower(owner), before, after, 0, false},
		{"old generation", owner, before, after, -1, false},
		{"future generation", owner, before, after, 1, false},
		{"scope case differs", owner, strings.Replace(before, "Witness", "witness", 1), after, 0, false},
		{"scope whitespace differs", owner, strings.Replace(before, `{ `, `{`, 1), after, 0, false},
		{"scope number form differs", owner, strings.Replace(before, "1e+09", "1000000000", 1), after, 0, false},
		{"inexact no-op", owner, strings.Replace(before, `{ `, `{`, 1), strings.Replace(before, `{ `, `{`, 1), 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			unittest.PrepareTestEnv(t)
			useIsolatedAppData(t)
			claimed, err := nativeoperation_model.ClaimOrdinary(t.Context(), owner, before, "verifier")
			require.NoError(t, err)
			err = nativeoperation_model.UpdateScopeExact(t.Context(), test.owner, claimed.Generation+test.generationOffset, test.before, test.after)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
			}
			held, err := nativeoperation_model.ReadReservation(t.Context())
			require.NoError(t, err)
			require.Equal(t, owner, held.Owner)
			require.Equal(t, claimed.Generation, held.Generation)
			want := before
			if test.valid {
				want = test.after
			}
			require.Equal(t, want, held.Scope)
		})
	}
}

func TestUpdateScopeExactRejectsSupersededScopeAndGeneration(t *testing.T) {
	unittest.PrepareTestEnv(t)
	useIsolatedAppData(t)
	ctx := t.Context()
	const owner = "ord:branch/7/main/proof"
	const before = `{"kind":"ordinary","future":1e+09}`
	const refined = `{"kind":"ordinary","future":1e+09,"new_oid":"allocated"}`
	claimed, err := nativeoperation_model.ClaimOrdinary(ctx, owner, before, "verifier")
	require.NoError(t, err)
	require.NoError(t, nativeoperation_model.UpdateScopeExact(ctx, owner, claimed.Generation, before, refined))
	require.ErrorIs(t, nativeoperation_model.UpdateScopeExact(ctx, owner, claimed.Generation, before, before), nativeoperation_model.ErrWrongOwner)
	require.ErrorIs(t, nativeoperation_model.UpdateScopeExact(ctx, owner, claimed.Generation, before, `{"kind":"ordinary","new_oid":"another"}`), nativeoperation_model.ErrWrongOwner)
	require.NoError(t, nativeoperation_model.UpdateScopeExact(ctx, owner, claimed.Generation, refined, refined))
	require.NoError(t, nativeoperation_model.ReleaseExactOwner(ctx, owner, claimed.Generation))
	replacement, err := nativeoperation_model.ClaimOrdinary(ctx, owner, refined, "replacement-verifier")
	require.NoError(t, err)
	require.Greater(t, replacement.Generation, claimed.Generation)
	require.ErrorIs(t, nativeoperation_model.UpdateScopeExact(ctx, owner, claimed.Generation, refined, refined), nativeoperation_model.ErrWrongOwner)
	require.ErrorIs(t, nativeoperation_model.UpdateScopeExact(ctx, owner, claimed.Generation, refined, before), nativeoperation_model.ErrWrongOwner)
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, replacement.Generation, held.Generation)
	require.Equal(t, []byte(refined), []byte(held.Scope)) //nolint:testifylint // Scope compare-and-swap preserves exact persisted bytes, including JSON spelling.
}

func TestOrdinaryStoreReleaseKeepsActiveRenameAfterSwallowedFailure(t *testing.T) {
	unittest.PrepareTestEnv(t)
	useIsolatedAppData(t)
	ctx := t.Context()
	var capability, activeScope, owner string
	var generation int64
	err := execcontext.RunOrdinary(ctx, nativeoperation_model.OrdinaryStore{}, t.TempDir(), "branch", "7/main", `{"kind":"ordinary","repository_id":7,"future":1e+09}`, func(owned context.Context) error {
		execution := execcontext.FromContext(owned)
		require.NotNil(t, execution)
		capability, owner, generation = execution.CapabilityPath, execution.Owner, execution.Generation
		held, err := nativeoperation_model.ReadReservation(owned)
		require.NoError(t, err)
		document, err := execcontext.DecodeScopeDocument(held.Scope)
		require.NoError(t, err)
		activeScope, err = document.WithNativeRename(&execcontext.RefRenamePlan{
			RepositoryID: 7, OwnerName: "owner", RepositoryName: "repo", Target: execcontext.TargetCode,
			FromRef: "refs/heads/main", ToRef: "refs/heads/renamed", TipOID: strings.Repeat("1", 40),
		})
		require.NoError(t, err)
		require.NoError(t, nativeoperation_model.UpdateScopeExact(owned, owner, generation, held.Scope, activeScope))
		// An enclosing callback may swallow its nested writer's error. The
		// persisted active rename still prevents the lease from opening.
		return nil
	})
	require.ErrorIs(t, err, execcontext.ErrIndeterminateEffect)
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, owner, held.Owner)
	require.Equal(t, generation, held.Generation)
	require.Equal(t, activeScope, held.Scope)
	_, err = os.Stat(capability)
	require.True(t, errors.Is(err, os.ErrNotExist), "retained fence must not retain an executable capability")
}

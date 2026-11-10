// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"fmt"
	"testing"

	"forgejo.org/models/db"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"

	"github.com/stretchr/testify/require"
)

func TestCheckedReceiveRefinementPreservesWitnessAndReportsProofFailure(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	owner := "ord:receive-http/1/main/0123456789abcdef"
	before := `{"kind":"ordinary", "repository_id":1,"family":"receive-http","refs":[{"ref":"refs/heads/topic","old_oid":"old", "future_ref": { "keep" : [1, 2] }}],"future_root" : { "keep" : true }}`
	_, err := nativeoperation_model.ClaimOrdinary(ctx, owner, before, execcontext.Verifier("receive-proof"))
	require.NoError(t, err)
	refs := []ScopedRef{{Ref: "refs/heads/topic", OldOID: "different", NewOID: "new"}}
	result, err := RefineReceiveScopeChecked(ctx, "wrong-proof", refs)
	require.ErrorIs(t, err, nativeoperation_model.ErrBusy)
	require.Equal(t, RefinementNotApplicable, result)
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, before, held.Scope)

	result, err = RefineReceiveScopeChecked(ctx, "receive-proof", refs)
	require.NoError(t, err)
	require.Equal(t, RefinementUpdated, result)
	held, err = nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Contains(t, held.Scope, `"future_root" : { "keep" : true }`)
	require.Contains(t, held.Scope, `"future_ref": { "keep" : [1, 2] }`)
	scope, err := decodeScope(held.Scope)
	require.NoError(t, err)
	require.Equal(t, []ScopedRef{{Ref: "refs/heads/topic", OldOID: "old", NewOID: "new"}}, scope.Refs)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = RefineReceiveScopeChecked(canceled, "receive-proof", refs)
	require.ErrorIs(t, err, context.Canceled)
}

func TestCheckedRefinementReportsPersistenceFailure(t *testing.T) {
	if !setting.Database.Type.IsSQLite3() {
		t.Skip("SQLite trigger injects the scope persistence failure")
	}
	for _, tc := range []struct {
		name, family, owner, ref string
		refine                   func(context.Context, string, []ScopedRef) (RefinementResult, error)
	}{
		{"receive", FamilyReceiveHTTP, "ord:receive-http/1/main/0123456789abcdef", "refs/heads/topic", RefineReceiveScopeChecked},
		{"pull creation", FamilyCollaboration, "ord:collaboration/pull/new/1/0123456789abcdef", "refs/pull/1/head", RefineCollabPullScopeChecked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unittest.PrepareTestEnv(t)
			ctx := t.Context()
			before, err := encodeScope(Scope{Kind: nativeoperation_model.OwnerOrdinary, RepositoryID: 1, Family: tc.family})
			require.NoError(t, err)
			_, err = nativeoperation_model.ClaimOrdinary(ctx, tc.owner, before, execcontext.Verifier("scope-proof"))
			require.NoError(t, err)
			_, err = db.GetEngine(ctx).Exec(fmt.Sprintf("CREATE TRIGGER reject_scope_refinement BEFORE UPDATE OF scope ON `%s` BEGIN SELECT RAISE(ABORT, 'scope-refinement-storage-failure'); END", db.TableName(new(nativeoperation_model.Reservation))))
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := db.GetEngine(context.WithoutCancel(ctx)).Exec("DROP TRIGGER reject_scope_refinement")
				require.NoError(t, err)
			})
			result, err := tc.refine(ctx, "scope-proof", []ScopedRef{{Ref: tc.ref, NewOID: testHead}})
			require.ErrorContains(t, err, "scope-refinement-storage-failure")
			require.Equal(t, RefinementNotApplicable, result)
			held, err := nativeoperation_model.ReadReservation(ctx)
			require.NoError(t, err)
			require.Equal(t, before, held.Scope)
		})
	}
}

func TestExplicitAuthorityIdentityDoesNotDependOnDiagnosticLabel(t *testing.T) {
	unittest.PrepareTestEnv(t)
	useIsolatedAppData(t)
	target := AuthorityTarget{Operation: "team/member", ID: 1, ID2: 2}
	for _, label := range []string{"team/999/member/998", "renamed-human-readable-label"} {
		err := WithAuthorityOwnershipFor(t.Context(), label, 0, target, func(ctx context.Context) error {
			held, err := nativeoperation_model.ReadReservation(ctx)
			require.NoError(t, err)
			require.Contains(t, held.Owner, label)
			scope, err := decodeScope(held.Scope)
			require.NoError(t, err)
			require.Equal(t, target.Operation, scope.AuthorityOp)
			require.Equal(t, target.ID, scope.AuthorityID)
			require.Equal(t, target.ID2, scope.AuthorityID2)
			return nil
		})
		require.NoError(t, err)
	}
	err := WithAuthorityOwnershipFor(t.Context(), "user/2", 0, AuthorityTarget{}, func(ctx context.Context) error {
		held, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		scope, err := decodeScope(held.Scope)
		require.NoError(t, err)
		require.Empty(t, scope.AuthorityOp)
		require.Zero(t, scope.AuthorityID)
		return nil
	})
	require.NoError(t, err)
}

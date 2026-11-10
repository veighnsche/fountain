// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"testing"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestNestedRefResultsPreserveUnknownEnclosingWitness(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	owner := "ord:collaboration/batch/pr-sweep/0123456789abcdef"
	before := `{"kind":"ordinary","family":"collaboration","repository_id":1,"head_ref":"refs/heads/topic","new_oid":"first", "future": { "keep" : [1, 2] }}`
	claimed, err := nativeoperation_model.ClaimOrdinary(ctx, owner, before, "verifier")
	require.NoError(t, err)
	owned := execcontext.NewContext(ctx, &execcontext.Execution{Owner: owner, Generation: claimed.Generation})
	for _, tip := range []string{"second", "third"} {
		require.NoError(t, recordRefWriteResult(owned, tip))
		held, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		require.Contains(t, held.Scope, `"future": { "keep" : [1, 2] }`)
		document, err := execcontext.DecodeScopeDocument(held.Scope)
		require.NoError(t, err)
		require.Equal(t, tip, document.Scope().NewOID)
		require.Equal(t, "refs/heads/topic", document.Scope().HeadRef)
	}
	stale := execcontext.NewContext(ctx, &execcontext.Execution{Owner: owner, Generation: claimed.Generation - 1})
	require.ErrorIs(t, recordRefWriteResult(stale, "stale"), nativeoperation_model.ErrWrongOwner)
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	document, err := execcontext.DecodeScopeDocument(held.Scope)
	require.NoError(t, err)
	require.Equal(t, "third", document.Scope().NewOID)
}

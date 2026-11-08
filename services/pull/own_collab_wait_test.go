// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"context"
	"encoding/json"
	"testing"

	//nolint:depguard // Verify the persisted enclosing ownership scope.
	execcontext "forgejo.org/modules/nativeoperation"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

func TestCollabOwnershipWaitDoesNotRepeatEffects(t *testing.T) {
	unittest.PrepareTestEnv(t)
	calls := 0
	err := withCollabOwnershipWait(t.Context(), execcontext.CollabBatchResource("pr-sweep"), collabScope{RepositoryID: 1}, func(context.Context) error {
		calls++
		return nativeoperation_model.ErrBusy
	})
	require.ErrorIs(t, err, nativeoperation_model.ErrBusy)
	require.Equal(t, 1, calls, "callback errors must never replay effects")
	state, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	require.Empty(t, state.Owner)
}

func TestNestedRefResultPreservesSweepBranch(t *testing.T) {
	unittest.PrepareTestEnv(t)
	err := withCollabOwnershipScope(t.Context(), execcontext.CollabBatchResource("pr-sweep"), collabScope{RepositoryID: 1, HeadRef: "refs/heads/branch2"}, func(ctx context.Context) error {
		require.NoError(t, recordRefWriteResult(ctx, "new-tip"))
		held, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		var recorded map[string]any
		require.NoError(t, json.Unmarshal([]byte(held.Scope), &recorded))
		require.Equal(t, "refs/heads/branch2", recorded["head_ref"])
		require.Equal(t, "new-tip", recorded["new_oid"])
		require.Equal(t, familyCollaboration, recorded["family"])
		return nil
	})
	require.NoError(t, err)
}

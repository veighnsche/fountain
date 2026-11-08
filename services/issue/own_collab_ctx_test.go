// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package issue

import (
	"context"
	"testing"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

// TestCollabOwnershipCanceledContext proves a canceled caller never
// strands the reservation: a pre-canceled claim refuses before any
// effect, and a cancellation racing the writer still releases.
func TestCollabOwnershipCanceledContext(t *testing.T) {
	unittest.PrepareTestEnv(t)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	ran := false
	require.ErrorIs(t, withCollabOwnership(canceled, "issue/1/title", 1, func(ctx context.Context) error {
		ran = true
		return nil
	}), context.Canceled)
	require.False(t, ran)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	require.NoError(t, withCollabOwnership(ctx, "issue/1/title", 1, func(ctx context.Context) error {
		held, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		require.Equal(t, `{"kind":"ordinary","family":"collaboration","repository_id":1}`, held.Scope)
		cancel()
		return nil
	}))

	idle, err := nativeoperation_model.ReadReservation(context.WithoutCancel(ctx))
	require.NoError(t, err)
	require.Empty(t, idle.Owner)
}

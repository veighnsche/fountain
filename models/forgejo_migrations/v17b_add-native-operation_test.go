// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"testing"

	"forgejo.org/models/db"
	migration_tests "forgejo.org/models/gitea_migrations/test"

	"github.com/stretchr/testify/require"
)

func Test_addNativeOperation(t *testing.T) {
	x, deferable := migration_tests.PrepareTestEnv(t, 0)
	defer deferable()
	if x == nil || t.Failed() {
		return
	}

	require.NoError(t, addNativeOperation(x))

	type reservationRow struct {
		ID       int64
		Revision int64
		Owner    string
	}
	var rows []reservationRow
	require.NoError(t, db.GetEngine(t.Context()).Table("reservation").Find(&rows))
	require.Len(t, rows, 1)
	require.Equal(t, int64(1), rows[0].ID)
	require.Equal(t, int64(1), rows[0].Revision)
	require.Empty(t, rows[0].Owner)

	// A repeated upgrade keeps the single seed row.
	require.NoError(t, addNativeOperation(x))
	rows = nil
	require.NoError(t, db.GetEngine(t.Context()).Table("reservation").Find(&rows))
	require.Len(t, rows, 1)
	require.NoError(t, widenNativeOperationCompletion(x))
}

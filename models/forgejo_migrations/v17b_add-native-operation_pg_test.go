// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"os"
	"testing"

	nativeoperation_model "forgejo.org/models/nativeoperation"

	"code.forgejo.org/xorm/xorm"
	"code.forgejo.org/xorm/xorm/names"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for the disposable PostgreSQL fixture
)

// Test_addNativeOperationOnPostgres proves a fresh PostgreSQL initializes the
// native-operation tables and the idle reservation seed, and that a repeated
// upgrade is a no-op. It also upgrades retained original-width data without
// losing the receipt or held reservation. It skips without FORGEJO_TEST_PG_DSN, so ordinary runs
// stay on SQLite.
func Test_addNativeOperationOnPostgres(t *testing.T) {
	dsn := os.Getenv("FORGEJO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("FORGEJO_TEST_PG_DSN is not set; needs a disposable PostgreSQL")
	}
	x, err := xorm.NewEngine("pgx", dsn)
	require.NoError(t, err)
	x.SetMapper(names.GonicMapper{})
	t.Cleanup(func() { _ = x.Close() })

	_, err = x.Exec("DROP TABLE IF EXISTS `operation`")
	require.NoError(t, err)
	_, err = x.Exec("DROP TABLE IF EXISTS `reservation`")
	require.NoError(t, err)

	require.NoError(t, addNativeOperation(x))

	type reservationRow struct {
		ID       int64
		Revision int64
		Owner    string
	}
	var rows []reservationRow
	require.NoError(t, x.Table("reservation").Find(&rows))
	require.Len(t, rows, 1)
	require.Equal(t, int64(1), rows[0].ID)
	require.Equal(t, int64(1), rows[0].Revision)
	require.Empty(t, rows[0].Owner)

	// A repeated upgrade keeps the single seed row.
	require.NoError(t, addNativeOperation(x))
	rows = nil
	require.NoError(t, x.Table("reservation").Find(&rows))
	require.Len(t, rows, 1)

	// The historical migration really created the constrained 16-character
	// column, not a fresh schema derived from today's model.
	var width int
	has, err := x.SQL("SELECT character_maximum_length FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'operation' AND column_name = 'completion'").Get(&width)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, 16, width)
	op := nativeoperation_model.Operation{
		InstallationID: "retained-installation", OperationID: "retained-operation",
		Kind: nativeoperation_model.KindHumanMerge, ActorID: 4, RepositoryID: 1,
		AuthRevision: "retained-auth", IntentDigest: "retained-digest", Intent: "retained-intent",
		Submitted: true, Admitted: true, EffectState: nativeoperation_model.EffectCommitted,
		Completion: nativeoperation_model.CompletionPending, Receipt: "retained-receipt",
	}
	_, err = x.Insert(&op)
	require.NoError(t, err)
	held := nativeoperation_model.Reservation{
		ID: 1, Revision: 7, Owner: "retained-operation", Generation: 9,
		OwnerKind: "human", Verifier: "retained-verifier", Scope: "retained-started-scope",
	}
	_, err = x.ID(1).AllCols().Update(&held)
	require.NoError(t, err)
	var beforeHeld nativeoperation_model.Reservation
	has, err = x.ID(1).Get(&beforeHeld)
	require.NoError(t, err)
	require.True(t, has)
	var beforeOp nativeoperation_model.Operation
	has, err = x.ID(op.ID).Get(&beforeOp)
	require.NoError(t, err)
	require.True(t, has)

	require.NoError(t, widenNativeOperationCompletion(x))
	// The upgrade is safe to repeat, preserving all operation/owner data.
	require.NoError(t, widenNativeOperationCompletion(x))
	// Read through a fresh connection as after an application restart. The
	// original pgx connection caches SELECT * metadata for the old width.
	require.NoError(t, x.Close())
	x, err = xorm.NewEngine("pgx", dsn)
	require.NoError(t, err)
	x.SetMapper(names.GonicMapper{})
	var afterOp nativeoperation_model.Operation
	has, err = x.ID(op.ID).Get(&afterOp)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, beforeOp, afterOp)
	var afterHeld nativeoperation_model.Reservation
	has, err = x.ID(1).Get(&afterHeld)
	require.NoError(t, err)
	require.True(t, has)
	require.Equal(t, beforeHeld, afterHeld)
	changed, err := x.ID(op.ID).Cols("completion").Update(&nativeoperation_model.Operation{Completion: nativeoperation_model.CompletionNeedsIntervention})
	require.NoError(t, err)
	require.EqualValues(t, 1, changed)
	afterOp = nativeoperation_model.Operation{}
	has, err = x.ID(op.ID).Get(&afterOp)
	require.NoError(t, err)
	require.True(t, has)
	beforeOp.Completion = nativeoperation_model.CompletionNeedsIntervention
	beforeOp.UpdatedUnix = afterOp.UpdatedUnix
	require.Equal(t, beforeOp, afterOp)

	_, err = x.Exec("DROP TABLE IF EXISTS `operation`")
	require.NoError(t, err)
	_, err = x.Exec("DROP TABLE IF EXISTS `reservation`")
	require.NoError(t, err)
}

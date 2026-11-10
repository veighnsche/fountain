// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"strings"
	"testing"

	migration_tests "forgejo.org/models/gitea_migrations/test"

	"code.forgejo.org/xorm/xorm"
	"code.forgejo.org/xorm/xorm/schemas"
	"github.com/stretchr/testify/require"
)

// These fixtures describe installed databases before the proof and kind
// migrations, independently of both the runtime model and migration records.
type desktopGrantBeforeProof struct {
	ID           string `xorm:"pk VARCHAR(32)"`
	ActorID      int64  `xorm:"NOT NULL INDEX"`
	OAuthGrantID int64  `xorm:"'oauth_grant_id' NOT NULL INDEX"`
	RepositoryID int64  `xorm:"NOT NULL INDEX"`
	RuntimeID    string `xorm:"VARCHAR(32) NOT NULL"`
	Revoked      bool   `xorm:"NOT NULL DEFAULT FALSE"`
	CreatedUnix  int64  `xorm:"created"`
}

func (*desktopGrantBeforeProof) TableName() string { return "native_desktop_grant" }

type desktopGrantBeforeKind struct {
	ID           string `xorm:"pk VARCHAR(32)"`
	ActorID      int64  `xorm:"NOT NULL INDEX"`
	OAuthGrantID int64  `xorm:"'oauth_grant_id' NOT NULL INDEX"`
	RepositoryID int64  `xorm:"NOT NULL INDEX"`
	RuntimeID    string `xorm:"VARCHAR(32) NOT NULL"`
	ProofSHA256  string `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	Revoked      bool   `xorm:"NOT NULL DEFAULT FALSE"`
	CreatedUnix  int64  `xorm:"created"`
}

func (*desktopGrantBeforeKind) TableName() string { return "native_desktop_grant" }

type desktopGrantMigrationRow struct {
	ID           string
	Kind         string
	ActorID      int64
	OAuthGrantID int64 `xorm:"'oauth_grant_id'"`
	RepositoryID int64
	RuntimeID    string
	ProofSHA256  string
	Revoked      bool
	CreatedUnix  int64
}

func assertDesktopGrantMigrationSchema(t *testing.T, x *xorm.Engine, proof, kind bool) {
	t.Helper()
	tables, err := x.DBMetas()
	require.NoError(t, err)
	require.Len(t, tables, 1)
	table := tables[0]
	require.Equal(t, "native_desktop_grant", table.Name)
	columns := []string{"id", "actor_id", "oauth_grant_id", "repository_id", "runtime_id", "revoked", "created_unix"}
	if proof {
		columns = append(columns, "proof_sh_a256")
	}
	if kind {
		columns = append(columns, "kind")
	}
	require.ElementsMatch(t, columns, table.ColumnsSeq())
	require.Equal(t, []string{"id"}, table.PrimaryKeys)
	for _, name := range columns {
		column := table.GetColumn(name)
		require.Equal(t, name == "created_unix", column.Nullable, name)
		if name != "proof_sh_a256" && name != "kind" && name != "revoked" {
			require.True(t, column.DefaultIsEmpty, name)
		}
	}
	require.False(t, table.GetColumn("id").IsAutoIncrement)
	revoked := table.GetColumn("revoked")
	require.False(t, revoked.DefaultIsEmpty)
	require.Contains(t, []string{"false", "0"}, strings.ToLower(revoked.Default))
	widths := map[string]int64{"id": 32, "runtime_id": 32}
	if proof {
		widths["proof_sh_a256"] = 64
	}
	if kind {
		widths["kind"] = 16
	}
	for name, width := range widths {
		column := table.GetColumn(name)
		if x.Dialect().URI().DBType == schemas.SQLITE {
			require.Equal(t, schemas.Text, column.SQLType.Name, name)
		} else {
			require.Equal(t, schemas.Varchar, column.SQLType.Name, name)
			require.Equal(t, width, column.Length, name)
		}
		if name == "proof_sh_a256" || name == "kind" {
			require.False(t, column.DefaultIsEmpty, name)
			require.Equal(t, "''", column.Default, name)
		}
	}
	require.Len(t, table.Indexes, 3)
	for _, name := range []string{"actor_id", "oauth_grant_id", "repository_id"} {
		index := table.Indexes[name]
		require.NotNil(t, index, name)
		require.Equal(t, schemas.IndexType, index.Type, name)
		require.Equal(t, []string{name}, index.Cols, name)
		require.Equal(t, "IDX_native_desktop_grant_"+name, index.XName(table.Name))
	}
}

func readDesktopGrantMigrationRows(t *testing.T, x *xorm.Engine) []desktopGrantMigrationRow {
	t.Helper()
	tables, err := x.DBMetas()
	require.NoError(t, err)
	require.Len(t, tables, 1)
	columns := strings.Join(tables[0].ColumnsSeq(), ",")
	var rows []desktopGrantMigrationRow
	require.NoError(t, x.Table("native_desktop_grant").Select(columns).OrderBy("id").Find(&rows))
	return rows
}

func Test_nativeDesktopGrantMigrations(t *testing.T) {
	t.Run("fresh sequence", func(t *testing.T) {
		x, cleanup := migration_tests.PrepareTestEnv(t, 0)
		defer cleanup()
		require.NotNil(t, x)

		require.NoError(t, addNativeDesktopGrant(x))
		assertDesktopGrantMigrationSchema(t, x, false, false)
		_, err := x.Exec("INSERT INTO `native_desktop_grant` (`id`, `actor_id`, `oauth_grant_id`, `repository_id`, `runtime_id`, `created_unix`) VALUES (?, ?, ?, ?, ?, ?)",
			strings.Repeat("a", 32), 11, 22, 33, strings.Repeat("b", 32), 1700000000)
		require.NoError(t, err)
		before := readDesktopGrantMigrationRows(t, x)
		require.Len(t, before, 1)
		require.False(t, before[0].Revoked)

		require.NoError(t, bindNativeDesktopProof(x))
		assertDesktopGrantMigrationSchema(t, x, true, false)
		require.Equal(t, before, readDesktopGrantMigrationRows(t, x))

		require.NoError(t, addDesktopIssueGrantKind(x))
		assertDesktopGrantMigrationSchema(t, x, true, true)
		require.Equal(t, before, readDesktopGrantMigrationRows(t, x))
	})

	for _, tc := range []struct {
		name  string
		bean  any
		proof bool
	}{
		{name: "pre-v17e", bean: new(desktopGrantBeforeProof)},
		{name: "pre-v17g", bean: new(desktopGrantBeforeKind), proof: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, cleanup := migration_tests.PrepareTestEnv(t, 0, tc.bean)
			defer cleanup()
			require.NotNil(t, x)
			assertDesktopGrantMigrationSchema(t, x, tc.proof, false)
			for i, revoked := range []bool{false, true} {
				_, err := x.Exec("INSERT INTO `native_desktop_grant` (`id`, `actor_id`, `oauth_grant_id`, `repository_id`, `runtime_id`, `revoked`, `created_unix`) VALUES (?, ?, ?, ?, ?, ?, ?)",
					strings.Repeat(string(rune('a'+i)), 32), 11+i, 22+i, 33+i, strings.Repeat(string(rune('c'+i)), 32), revoked, 1700000000+i)
				require.NoError(t, err)
			}
			if tc.proof {
				_, err := x.Exec("UPDATE `native_desktop_grant` SET `proof_sh_a256` = ? WHERE `revoked` = ?", strings.Repeat("e", 64), true)
				require.NoError(t, err)
			}
			before := readDesktopGrantMigrationRows(t, x)
			require.Len(t, before, 2)
			if !tc.proof {
				require.NoError(t, bindNativeDesktopProof(x))
				assertDesktopGrantMigrationSchema(t, x, true, false)
				require.Equal(t, before, readDesktopGrantMigrationRows(t, x))
			}
			require.NoError(t, addDesktopIssueGrantKind(x))
			assertDesktopGrantMigrationSchema(t, x, true, true)
			require.Equal(t, before, readDesktopGrantMigrationRows(t, x))

			for _, upgrade := range []func(*xorm.Engine) error{addNativeDesktopGrant, bindNativeDesktopProof, addDesktopIssueGrantKind} {
				require.NoError(t, upgrade(x))
			}
			assertDesktopGrantMigrationSchema(t, x, true, true)
			require.Equal(t, before, readDesktopGrantMigrationRows(t, x))
		})
	}
}

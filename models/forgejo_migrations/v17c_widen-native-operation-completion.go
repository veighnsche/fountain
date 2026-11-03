// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"fmt"

	"code.forgejo.org/xorm/xorm"
	"code.forgejo.org/xorm/xorm/schemas"
)

func init() {
	registerMigration(&Migration{
		Description: "widen native operation completion for needs_intervention",
		Upgrade:     widenNativeOperationCompletion,
	})
}

func widenNativeOperationCompletion(x *xorm.Engine) error {
	// SyncWithOptions does not widen PostgreSQL VARCHAR columns. Keep the
	// original ledger and reservation intact while allowing the longer status.
	var query string
	switch x.Dialect().URI().DBType {
	case schemas.POSTGRES:
		query = "ALTER TABLE `operation` ALTER COLUMN `completion` TYPE VARCHAR(32)"
	case schemas.MYSQL:
		query = "ALTER TABLE `operation` MODIFY `completion` VARCHAR(32) NOT NULL DEFAULT ''"
	case schemas.SQLITE:
		// SQLite does not enforce the declared VARCHAR length.
		return nil
	default:
		return fmt.Errorf("unsupported native operation database: %s", x.Dialect().URI().DBType)
	}
	_, err := x.Exec(query)
	return err
}

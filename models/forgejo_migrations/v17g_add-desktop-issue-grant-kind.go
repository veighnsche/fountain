// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"code.forgejo.org/xorm/xorm"
	"forgejo.org/models/desktopgrant"
)

func init() {
	registerMigration(&Migration{Description: "separate desktop issue-only enrollment from code access", Upgrade: func(engine *xorm.Engine) error { return engine.Sync(new(desktopgrant.Grant)) }})
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"forgejo.org/modules/timeutil"

	"code.forgejo.org/xorm/xorm"
)

func init() {
	registerMigration(&Migration{Description: "add repository-scoped native desktop enrollment", Upgrade: addNativeDesktopGrant})
}

type v17dNativeDesktopGrant struct {
	ID           string             `xorm:"pk VARCHAR(32)"`
	ActorID      int64              `xorm:"NOT NULL INDEX"`
	OAuthGrantID int64              `xorm:"'oauth_grant_id' NOT NULL INDEX"`
	RepositoryID int64              `xorm:"NOT NULL INDEX"`
	RuntimeID    string             `xorm:"VARCHAR(32) NOT NULL"`
	Revoked      bool               `xorm:"NOT NULL DEFAULT FALSE"`
	CreatedUnix  timeutil.TimeStamp `xorm:"created"`
}

func (*v17dNativeDesktopGrant) TableName() string { return "native_desktop_grant" }

func addNativeDesktopGrant(engine *xorm.Engine) error {
	return engine.Sync(new(v17dNativeDesktopGrant))
}

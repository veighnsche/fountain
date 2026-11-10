// Copyright 2023 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	std_ctx "context"
	"fmt"
	"net/http"

	"forgejo.org/models/db"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/base"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

const (
	tplSettingsBlockedUsers base.TplName = "user/settings/blocked_users"
)

// BlockedUsers render the blocked users list page.
func BlockedUsers(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("settings.blocked_users")
	ctx.Data["PageIsBlockedUsers"] = true
	ctx.Data["BaseLink"] = setting.AppSubURL + "/user/settings/blocked_users"
	ctx.Data["BaseLinkNew"] = setting.AppSubURL + "/user/settings/blocked_users"

	blockedUsers, err := user_model.ListBlockedUsers(ctx, ctx.Doer.ID, db.ListOptions{})
	if err != nil {
		ctx.ServerError("ListBlockedUsers", err)
		return
	}

	ctx.Data["BlockedUsers"] = blockedUsers
	ctx.HTML(http.StatusOK, tplSettingsBlockedUsers)
}

// UnblockUser unblocks a particular user for the doer.
func UnblockUser(ctx *context.Context) {
	// One authority writer owns the block change before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	doerID := ctx.Doer.ID
	blockedID := ctx.FormInt64("user_id")
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("user/%d/unblock/%d", doerID, blockedID), 0, nativeoperation_service.AuthorityTarget{Operation: "user/unblock", ID: doerID, ID2: blockedID}, func(ctx std_ctx.Context) error {
		return user_model.UnblockUser(ctx, doerID, blockedID)
	}); err != nil {
		ctx.ServerError("UnblockUser", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("settings.user_unblock_success"))
	ctx.Redirect(setting.AppSubURL + "/user/settings/blocked_users")
}

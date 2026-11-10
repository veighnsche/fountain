// Copyright 2023 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package utils

import (
	std_ctx "context"
	"fmt"
	"net/http"

	user_model "forgejo.org/models/user"
	api "forgejo.org/modules/structs"
	"forgejo.org/services/context"
	nativeoperation_service "forgejo.org/services/nativeoperation"
	user_service "forgejo.org/services/user"
)

// ListUserBlockedUsers lists the blocked users of the provided doer.
func ListUserBlockedUsers(ctx *context.APIContext, doer *user_model.User) {
	count, err := user_model.CountBlockedUsers(ctx, doer.ID)
	if err != nil {
		ctx.InternalServerError(err)
		return
	}

	blockedUsers, err := user_model.ListBlockedUsers(ctx, doer.ID, GetListOptions(ctx))
	if err != nil {
		ctx.InternalServerError(err)
		return
	}

	apiBlockedUsers := make([]*api.BlockedUser, len(blockedUsers))
	for i, blockedUser := range blockedUsers {
		apiBlockedUsers[i] = &api.BlockedUser{
			BlockID: blockedUser.ID,
			Created: blockedUser.CreatedUnix.AsTime(),
		}
		if err != nil {
			ctx.InternalServerError(err)
			return
		}
	}

	ctx.SetTotalCountHeader(count)
	ctx.JSON(http.StatusOK, apiBlockedUsers)
}

// BlockUser blocks the blockUser from the doer.
func BlockUser(ctx *context.APIContext, doer, blockUser *user_model.User) {
	err := user_service.BlockUser(ctx, doer.ID, blockUser.ID)
	if err != nil {
		if nativeoperation_service.IsBusy(err) {
			ctx.Error(http.StatusServiceUnavailable, "", "A native operation is in progress; retry shortly.")
			return
		}
		ctx.InternalServerError(err)
		return
	}

	ctx.Status(http.StatusNoContent)
}

// UnblockUser unblocks the blockUser from the doer.
func UnblockUser(ctx *context.APIContext, doer, blockUser *user_model.User) {
	// One authority writer owns the block change before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("user/%d/unblock/%d", doer.ID, blockUser.ID), 0, nativeoperation_service.AuthorityTarget{Operation: "user/unblock", ID: doer.ID, ID2: blockUser.ID}, func(ctx std_ctx.Context) error {
		return user_model.UnblockUser(ctx, doer.ID, blockUser.ID)
	}); err != nil {
		if nativeoperation_service.IsBusy(err) {
			ctx.Error(http.StatusServiceUnavailable, "", "A native operation is in progress; retry shortly.")
			return
		}
		ctx.InternalServerError(err)
		return
	}

	ctx.Status(http.StatusNoContent)
}

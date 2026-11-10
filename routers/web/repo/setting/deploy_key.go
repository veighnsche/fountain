// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	std_ctx "context"
	"fmt"
	"net/http"

	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/models/db"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/web"
	asymkey_service "forgejo.org/services/asymkey"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

// DeployKeys render the deploy keys list of a repository page
func DeployKeys(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.settings.deploy_keys") + " / " + ctx.Tr("secrets.secrets")
	ctx.Data["PageIsSettingsKeys"] = true
	ctx.Data["DisableSSH"] = setting.SSH.Disabled

	keys, err := db.Find[asymkey_model.DeployKey](ctx, asymkey_model.ListDeployKeysOptions{RepoID: ctx.Repo.Repository.ID})
	if err != nil {
		ctx.ServerError("ListDeployKeys", err)
		return
	}
	ctx.Data["Deploykeys"] = keys

	ctx.HTML(http.StatusOK, tplDeployKeys)
}

// DeployKeysPost response for adding a deploy key of a repository
func DeployKeysPost(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.AddKeyForm)
	ctx.Data["Title"] = ctx.Tr("repo.settings.deploy_keys")
	ctx.Data["PageIsSettingsKeys"] = true
	ctx.Data["DisableSSH"] = setting.SSH.Disabled

	keys, err := db.Find[asymkey_model.DeployKey](ctx, asymkey_model.ListDeployKeysOptions{RepoID: ctx.Repo.Repository.ID})
	if err != nil {
		ctx.ServerError("ListDeployKeys", err)
		return
	}
	ctx.Data["Deploykeys"] = keys

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplDeployKeys)
		return
	}

	content, err := asymkey_model.CheckPublicKeyString(form.Content)
	if err != nil {
		if db.IsErrSSHDisabled(err) {
			ctx.Flash.Info(ctx.Tr("settings.ssh_disabled"))
		} else if asymkey_model.IsErrKeyUnableVerify(err) {
			ctx.Flash.Info(ctx.Tr("form.unable_verify_ssh_key"))
		} else if err == asymkey_model.ErrKeyIsPrivate {
			ctx.Data["HasError"] = true
			ctx.Data["Err_Content"] = true
			ctx.Flash.Error(ctx.Tr("form.must_use_public_key"))
		} else {
			ctx.Data["HasError"] = true
			ctx.Data["Err_Content"] = true
			ctx.Flash.Error(ctx.Tr("form.invalid_ssh_key", err.Error()))
		}
		ctx.Redirect(ctx.Repo.RepoLink + "/settings/keys")
		return
	}

	// One authority writer owns the deploy-key change before its
	// effects, advancing the native revision so old permission
	// observations go stale.
	repoID := ctx.Repo.Repository.ID
	var key *asymkey_model.DeployKey
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("repo/%d/deploy-key", repoID), repoID, nativeoperation_service.AuthorityTarget{Operation: "repo/deploy-key", ID: repoID}, func(ctx std_ctx.Context) error {
		var err error
		key, err = asymkey_model.AddDeployKey(ctx, repoID, form.Title, content, !form.IsWritable)
		return err
	}); err != nil {
		ctx.Data["HasError"] = true
		switch {
		case asymkey_model.IsErrDeployKeyAlreadyExist(err):
			ctx.Data["Err_Content"] = true
			ctx.RenderWithErr(ctx.Tr("repo.settings.key_been_used"), tplDeployKeys, &form)
		case asymkey_model.IsErrKeyAlreadyExist(err):
			ctx.Data["Err_Content"] = true
			ctx.RenderWithErr(ctx.Tr("settings.ssh_key_been_used"), tplDeployKeys, &form)
		case asymkey_model.IsErrKeyNameAlreadyUsed(err):
			ctx.Data["Err_Title"] = true
			ctx.RenderWithErr(ctx.Tr("repo.settings.key_name_used"), tplDeployKeys, &form)
		case asymkey_model.IsErrDeployKeyNameAlreadyUsed(err):
			ctx.Data["Err_Title"] = true
			ctx.RenderWithErr(ctx.Tr("repo.settings.key_name_used"), tplDeployKeys, &form)
		default:
			ctx.ServerError("AddDeployKey", err)
		}
		return
	}

	log.Trace("Deploy key added: %d", ctx.Repo.Repository.ID)
	ctx.Flash.Success(ctx.Tr("repo.settings.add_key_success", key.Name))
	ctx.Redirect(ctx.Repo.RepoLink + "/settings/keys")
}

// DeleteDeployKey response for deleting a deploy key
func DeleteDeployKey(ctx *context.Context) {
	// One authority writer owns the deploy-key change before its
	// effects, advancing the native revision so old permission
	// observations go stale.
	repoID := ctx.Repo.Repository.ID
	keyID := ctx.FormInt64("id")
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("repo/%d/deploy-key/%d", repoID, keyID), repoID, nativeoperation_service.AuthorityTarget{Operation: "repo/deploy-key", ID: repoID, ID2: keyID}, func(ctx std_ctx.Context) error {
		return asymkey_service.DeleteDeployKey(ctx, keyID, repoID)
	}); err != nil {
		ctx.Flash.Error("DeleteDeployKey: " + err.Error())
	} else {
		ctx.Flash.Success(ctx.Tr("repo.settings.deploy_key_deletion_success"))
	}

	ctx.JSONRedirect(ctx.Repo.RepoLink + "/settings/keys")
}

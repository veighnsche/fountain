// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	std_ctx "context"
	"fmt"
	"net/http"

	"forgejo.org/models/auth"
	"forgejo.org/modules/base"
	"forgejo.org/modules/util"
	"forgejo.org/modules/web"
	shared_user "forgejo.org/routers/web/shared/user"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

type OAuth2CommonHandlers struct {
	OwnerID            int64        // 0 for instance-wide, otherwise OrgID or UserID
	BasePathList       string       // the base URL for the application list page, eg: "/user/setting/applications"
	BasePathEditPrefix string       // the base URL for the application edit page, will be appended with app id, eg: "/user/setting/applications/oauth2"
	TplAppEdit         base.TplName // the template for the application edit page
}

func (oa *OAuth2CommonHandlers) renderEditPage(ctx *context.Context) {
	app := ctx.Data["App"].(*auth.OAuth2Application)
	ctx.Data["FormActionPath"] = fmt.Sprintf("%s/%d", oa.BasePathEditPrefix, app.ID)

	if ctx.ContextUser != nil && ctx.ContextUser.IsOrganization() {
		if err := shared_user.LoadHeaderCount(ctx); err != nil {
			ctx.ServerError("LoadHeaderCount", err)
			return
		}
	}

	ctx.HTML(http.StatusOK, oa.TplAppEdit)
}

// AddApp adds an oauth2 application
func (oa *OAuth2CommonHandlers) AddApp(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.EditOAuth2ApplicationForm)
	if ctx.HasError() {
		ctx.Flash.Error(ctx.GetErrMsg())
		// go to the application list page
		ctx.Redirect(oa.BasePathList)
		return
	}

	// One authority writer owns the credential change before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	var app *auth.OAuth2Application
	var secret string
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("owner/%d/oauth2-app", oa.OwnerID), 0, nativeoperation_service.AuthorityTarget{Operation: "owner/oauth2-app", ID: oa.OwnerID}, func(ctx std_ctx.Context) error {
		created, err := auth.CreateOAuth2Application(ctx, auth.CreateOAuth2ApplicationOptions{
			Name:               form.Name,
			RedirectURIs:       util.SplitTrimSpace(form.RedirectURIs, "\n"),
			UserID:             oa.OwnerID,
			ConfidentialClient: form.ConfidentialClient,
		})
		if err != nil {
			return err
		}
		app = created
		secret, err = app.GenerateClientSecret(ctx)
		return err
	}); err != nil {
		ctx.ServerError("CreateOAuth2Application", err)
		return
	}

	// render the edit page with secret
	ctx.Flash.Success(ctx.Tr("settings.create_oauth2_application_success"), true)
	ctx.Data["App"] = app
	ctx.Data["ClientSecret"] = secret

	oa.renderEditPage(ctx)
}

// EditShow displays the given application
func (oa *OAuth2CommonHandlers) EditShow(ctx *context.Context) {
	app, err := auth.GetOAuth2ApplicationByID(ctx, ctx.ParamsInt64("id"))
	if err != nil {
		if auth.IsErrOAuthApplicationNotFound(err) {
			ctx.NotFound("Application not found", err)
			return
		}
		ctx.ServerError("GetOAuth2ApplicationByID", err)
		return
	}
	if app.UID != oa.OwnerID {
		ctx.NotFound("Application not found", nil)
		return
	}
	ctx.Data["App"] = app
	oa.renderEditPage(ctx)
}

// EditSave saves the oauth2 application
func (oa *OAuth2CommonHandlers) EditSave(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.EditOAuth2ApplicationForm)

	if ctx.HasError() {
		app, err := auth.GetOAuth2ApplicationByID(ctx, ctx.ParamsInt64("id"))
		if err != nil {
			if auth.IsErrOAuthApplicationNotFound(err) {
				ctx.NotFound("Application not found", err)
				return
			}
			ctx.ServerError("GetOAuth2ApplicationByID", err)
			return
		}
		if app.UID != oa.OwnerID {
			ctx.NotFound("Application not found", nil)
			return
		}
		ctx.Data["App"] = app

		oa.renderEditPage(ctx)
		return
	}

	// One authority writer owns the credential change before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	appID := ctx.ParamsInt64("id")
	var updated *auth.OAuth2Application
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("oauth2-app/%d", appID), 0, nativeoperation_service.AuthorityTarget{Operation: "oauth2-app", ID: appID}, func(ctx std_ctx.Context) error {
		app, err := auth.UpdateOAuth2Application(ctx, auth.UpdateOAuth2ApplicationOptions{
			ID:                 appID,
			Name:               form.Name,
			RedirectURIs:       util.SplitTrimSpace(form.RedirectURIs, "\n"),
			UserID:             oa.OwnerID,
			ConfidentialClient: form.ConfidentialClient,
		})
		if err != nil {
			return err
		}
		updated = app
		return nil
	}); err != nil {
		ctx.ServerError("UpdateOAuth2Application", err)
		return
	}
	ctx.Data["App"] = updated
	ctx.Flash.Success(ctx.Tr("settings.update_oauth2_application_success"))
	ctx.Redirect(oa.BasePathList)
}

// RegenerateSecret regenerates the secret
func (oa *OAuth2CommonHandlers) RegenerateSecret(ctx *context.Context) {
	app, err := auth.GetOAuth2ApplicationByID(ctx, ctx.ParamsInt64("id"))
	if err != nil {
		if auth.IsErrOAuthApplicationNotFound(err) {
			ctx.NotFound("Application not found", err)
			return
		}
		ctx.ServerError("GetOAuth2ApplicationByID", err)
		return
	}
	if app.UID != oa.OwnerID {
		ctx.NotFound("Application not found", nil)
		return
	}
	ctx.Data["App"] = app
	// One authority writer owns the credential change before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	var secret string
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("oauth2-app/%d", app.ID), 0, nativeoperation_service.AuthorityTarget{Operation: "oauth2-app", ID: app.ID}, func(ctx std_ctx.Context) error {
		regenerated, err := app.GenerateClientSecret(ctx)
		if err != nil {
			return err
		}
		secret = regenerated
		return nil
	}); err != nil {
		ctx.ServerError("GenerateClientSecret", err)
		return
	}
	ctx.Data["ClientSecret"] = secret
	ctx.Flash.Success(ctx.Tr("settings.update_oauth2_application_success"), true)
	oa.renderEditPage(ctx)
}

// DeleteApp deletes the given oauth2 application
func (oa *OAuth2CommonHandlers) DeleteApp(ctx *context.Context) {
	// One authority writer owns the credential change before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	appID := ctx.ParamsInt64("id")
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("oauth2-app/%d", appID), 0, nativeoperation_service.AuthorityTarget{Operation: "oauth2-app", ID: appID}, func(ctx std_ctx.Context) error {
		return auth.DeleteOAuth2Application(ctx, appID, oa.OwnerID)
	}); err != nil {
		ctx.ServerError("DeleteOAuth2Application", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("settings.remove_oauth2_application_success"))
	ctx.JSONRedirect(oa.BasePathList)
}

// RevokeGrant revokes the grant
func (oa *OAuth2CommonHandlers) RevokeGrant(ctx *context.Context) {
	// One authority writer owns the credential change before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	grantID := ctx.ParamsInt64("grantId")
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("oauth2-grant/%d", grantID), 0, nativeoperation_service.AuthorityTarget{Operation: "oauth2-grant", ID: grantID}, func(ctx std_ctx.Context) error {
		return auth.RevokeOAuth2Grant(ctx, grantID, oa.OwnerID)
	}); err != nil {
		ctx.ServerError("RevokeOAuth2Grant", err)
		return
	}

	ctx.Flash.Success(ctx.Tr("settings.revoke_oauth2_grant_success"))
	ctx.JSONRedirect(oa.BasePathList)
}

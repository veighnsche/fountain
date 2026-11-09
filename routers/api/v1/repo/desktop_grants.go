// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	extension "forgejo.org/extension-sdk"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/desktopgrant"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
)

var desktopID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var desktopProof = regexp.MustCompile(`^[a-f0-9]{64}$`)

// DesktopOAuthConfig exposes only the operator-selected public application's
// identifier. It creates no application, authorization or repository grant.
func DesktopOAuthConfig(ctx *context.APIContext) {
	ctx.Resp.Header().Set("Cache-Control", "no-store")
	if !setting.OAuth2.Enabled || setting.OAuth2.FactoryDesktopClientID == "" || !setting.OAuth2.EnableAdditionalGrantScopes {
		ctx.NotFound()
		return
	}
	app, err := auth_model.GetOAuth2ApplicationByClientID(ctx, setting.OAuth2.FactoryDesktopClientID)
	if err != nil || app.ConfidentialClient || !app.ContainsRedirectURI("http://127.0.0.1/") {
		ctx.NotFound()
		return
	}
	ctx.JSON(http.StatusOK, map[string]string{"client_id": app.ClientID, "scope": "read:repository"})
}

// DesktopOAuthIdentity verifies the authenticated self without requiring a
// broader user scope or any repository's code-unit permission.
func DesktopOAuthIdentity(ctx *context.APIContext) {
	_, app, ok := desktopAuthorization(ctx)
	if !ok {
		return
	}
	if app.ClientID != setting.OAuth2.FactoryDesktopClientID {
		ctx.Error(http.StatusForbidden, "desktop identity", errors.New("configured desktop application required"))
		return
	}
	ctx.JSON(http.StatusOK, extension.Actor{ID: strconv.FormatInt(ctx.Doer().ID, 10), Username: ctx.Doer().Name})
}

// OAuth grants can be shared by several devices. Only this profile knows the
// preimage; metadata and a shared account token cannot adopt its enrollment.
func desktopProofAdmitted(ctx *context.APIContext, expected string) bool {
	if expected == "" {
		return true // Retained legacy enrollments authorize repository reads only.
	}
	proof := ctx.Req.Header.Get("X-Factory-Desktop-Proof")
	digest := sha256.Sum256([]byte(proof))
	if len(ctx.Req.Header.Values("X-Factory-Desktop-Proof")) != 1 || !desktopProof.MatchString(proof) || subtle.ConstantTimeCompare([]byte(hex.EncodeToString(digest[:])), []byte(expected)) != 1 {
		ctx.Error(http.StatusForbidden, "desktop enrollment", errors.New("native profile proof required"))
		return false
	}
	return true
}

// The authentication method validated this identity. Do not parse the token a
// second time or accept account/application selectors from a desktop payload.
func desktopAuthorization(ctx *context.APIContext) (*auth_model.OAuth2Grant, *auth_model.OAuth2Application, bool) {
	identity, ok := ctx.Authentication().(interface{ OAuth2GrantID() int64 })
	if !ok || ctx.Doer() == nil || identity.OAuth2GrantID() <= 0 || len(ctx.Req.Header.Values("Authorization")) != 1 || !strings.HasPrefix(ctx.Req.Header.Get("Authorization"), "Bearer ") || ctx.Req.URL.RawQuery != "" || ctx.Req.URL.ForceQuery {
		ctx.Error(http.StatusUnauthorized, "desktop authorization", errors.New("current OAuth bearer authorization required"))
		return nil, nil, false
	}
	grant, err := auth_model.GetOAuth2GrantByID(ctx, identity.OAuth2GrantID())
	if err != nil || grant.UserID != ctx.Doer().ID {
		ctx.Error(http.StatusUnauthorized, "desktop authorization", errors.New("OAuth authorization no longer available"))
		return nil, nil, false
	}
	app, err := auth_model.GetOAuth2ApplicationByID(ctx, grant.ApplicationID)
	if err != nil || app.ConfidentialClient {
		ctx.Error(http.StatusForbidden, "desktop authorization", errors.New("public native OAuth client required"))
		return nil, nil, false
	}
	ctx.Resp.Header().Set("Cache-Control", "no-store")
	return grant, app, true
}

func desktopProjection(ctx *context.APIContext, grant *desktopgrant.Grant, app *auth_model.OAuth2Application) extension.DesktopGrant {
	repository := ctx.Repo().Repository
	return extension.DesktopGrant{ID: grant.ID, RuntimeID: grant.RuntimeID, ApplicationID: app.ClientID,
		Actor:      extension.Actor{ID: strconv.FormatInt(ctx.Doer().ID, 10), Username: ctx.Doer().Name, SiteAdmin: ctx.Doer().IsAdmin},
		Repository: extension.Repository{ID: strconv.FormatInt(repository.ID, 10), Owner: ctx.Repo().Owner.Name, Name: repository.Name, DefaultBranch: repository.DefaultBranch, Permission: "read"},
		Active:     !grant.Revoked, Operations: []string{extension.DesktopRepositoryRead}, RuntimeBound: grant.ProofSHA256 != ""}
}

func EnrollDesktopGrant(ctx *context.APIContext) {
	authorization, app, ok := desktopAuthorization(ctx)
	if !ok {
		return
	}
	var request extension.DesktopGrantEnrollment
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Resp, ctx.Req.Body, 4096))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&request) != nil || decoder.Decode(&trailing) != io.EOF || !desktopID.MatchString(request.CommandID) || !desktopID.MatchString(request.RuntimeID) || (request.ProofSHA256 != "" && !desktopProof.MatchString(request.ProofSHA256)) {
		ctx.Error(http.StatusBadRequest, "desktop enrollment", errors.New("bounded command and runtime identities required"))
		return
	}
	if !desktopProofAdmitted(ctx, request.ProofSHA256) {
		return
	}
	grant, created, err := desktopgrant.Enroll(ctx, &desktopgrant.Grant{ID: request.CommandID, ActorID: ctx.Doer().ID, OAuthGrantID: authorization.ID, RepositoryID: ctx.Repo().Repository.ID, RuntimeID: request.RuntimeID, ProofSHA256: request.ProofSHA256})
	if errors.Is(err, desktopgrant.ErrConflict) {
		ctx.Error(http.StatusConflict, "desktop enrollment", err)
		return
	}
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "desktop enrollment", errors.New("enrollment storage unavailable"))
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	ctx.JSON(status, desktopProjection(ctx, grant, app))
}

func currentDesktopGrant(ctx *context.APIContext) (*desktopgrant.Grant, *auth_model.OAuth2Application, bool) {
	authorization, app, ok := desktopAuthorization(ctx)
	if !ok {
		return nil, nil, false
	}
	id := ctx.Params(":grant")
	if !desktopID.MatchString(id) {
		ctx.NotFound()
		return nil, nil, false
	}
	grant, err := desktopgrant.Get(ctx, id)
	if errors.Is(err, desktopgrant.ErrNotFound) || err == nil && (grant.ActorID != ctx.Doer().ID || grant.OAuthGrantID != authorization.ID || grant.RepositoryID != ctx.Repo().Repository.ID) {
		ctx.NotFound()
		return nil, nil, false
	}
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "desktop enrollment", errors.New("enrollment storage unavailable"))
		return nil, nil, false
	}
	if !desktopProofAdmitted(ctx, grant.ProofSHA256) {
		return nil, nil, false
	}
	return grant, app, true
}

func GetDesktopGrant(ctx *context.APIContext) {
	grant, app, ok := currentDesktopGrant(ctx)
	if ok {
		ctx.JSON(http.StatusOK, desktopProjection(ctx, grant, app))
	}
}

func RevokeDesktopGrant(ctx *context.APIContext) {
	grant, app, ok := currentDesktopGrant(ctx)
	if !ok {
		return
	}
	if err := desktopgrant.Revoke(ctx, grant); err != nil {
		ctx.Error(http.StatusInternalServerError, "desktop enrollment", errors.New("enrollment withdrawal unavailable"))
		return
	}
	grant.Revoked = true
	ctx.JSON(http.StatusOK, desktopProjection(ctx, grant, app))
}

func ReadDesktopRepository(ctx *context.APIContext) {
	grant, app, ok := currentDesktopGrant(ctx)
	if !ok {
		return
	}
	if grant.Revoked {
		ctx.Error(http.StatusForbidden, "desktop enrollment", errors.New("desktop enrollment withdrawn"))
		return
	}
	ctx.JSON(http.StatusOK, desktopProjection(ctx, grant, app).Repository)
}

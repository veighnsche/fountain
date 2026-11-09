// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/desktopgrant"
	"forgejo.org/models/extensionauth"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
	native_service "forgejo.org/services/nativeoperation"
)

// DesktopControlOAuthConfig is a distinct, explicit consent purpose. The read
// configuration is unchanged and never silently requests write scope.
func DesktopControlOAuthConfig(ctx *context.APIContext) {
	if !setting.FactoryControlConfigured() {
		ctx.NotFound()
		return
	}
	// Use the same configured-client validation as the read endpoint.
	desktopOAuthConfig(ctx, setting.OAuth2.FactoryDesktopControlClientID, "write:repository")
}

func desktopControllerError(ctx *context.APIContext, err error) {
	status := http.StatusServiceUnavailable
	if errors.Is(err, native_service.ErrInvalidIntent) {
		status = http.StatusBadRequest
	}
	if errors.Is(err, native_service.ErrAuthorityLost) {
		status = http.StatusForbidden
	}
	if errors.Is(err, native_service.ErrIntentConflict) || errors.Is(err, desktopgrant.ErrController) || errors.Is(err, desktopgrant.ErrConflict) {
		status = http.StatusConflict
	}
	ctx.Error(status, "desktop controller", errors.New("controller request refused; original commands and effects are retained"))
}

func decodeDesktopControl(ctx *context.APIContext, request any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Resp, ctx.Req.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(request) != nil || decoder.Decode(&trailing) != io.EOF {
		ctx.Error(http.StatusBadRequest, "desktop controller", errors.New("bounded exact command required"))
		return false
	}
	return true
}

func GetDesktopPlacement(ctx *context.APIContext) {
	if _, _, ok := currentDesktopGrant(ctx); !ok {
		return
	}
	p, err := desktopgrant.ReadPlacement(ctx, ctx.Repo().Repository.ID)
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, p.Projection())
}

func SelectDesktopRuntime(ctx *context.APIContext) {
	g, _, ok := currentDesktopGrant(ctx)
	if !ok {
		return
	}
	var request sdk.RuntimeSelection
	if !decodeDesktopControl(ctx, &request) {
		return
	}
	receipt, err := native_service.Default().SelectDesktopRuntime(ctx, g.ID, request)
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, receipt)
}

func desktopOperationProjection(ctx *context.APIContext, g *desktopgrant.OperationGrant) sdk.DesktopOperationGrant {
	active := false
	for _, kind := range strings.Split(g.Operations, ",") {
		_, err := extensionauth.CheckControllerUse(ctx, extensionauth.ControllerUse{GrantID: g.ID, InstallationID: g.InstallationID,
			ActorID: g.ActorID, RepositoryID: g.RepositoryID, CredentialFingerprint: g.Fingerprint(), Kind: kind})
		if err == nil {
			active = true
			break
		}
	}
	return sdk.DesktopOperationGrant{ID: g.ID, EnrollmentID: g.EnrollmentID, RuntimeID: g.RuntimeID, InstallationID: g.InstallationID,
		Generation: g.Generation, Actor: sdk.Actor{ID: strconv.FormatInt(g.ActorID, 10), Username: ctx.Doer().Name},
		Repository: sdk.Repository{ID: strconv.FormatInt(g.RepositoryID, 10), Owner: ctx.Repo().Owner.Name, Name: ctx.Repo().Repository.Name, DefaultBranch: ctx.Repo().Repository.DefaultBranch},
		Operations: strings.Split(g.Operations, ","), NotAfter: g.NotAfter, Active: active}
}

func AuthorizeDesktopOperations(ctx *context.APIContext) {
	g, _, ok := currentDesktopGrant(ctx)
	if !ok {
		return
	}
	var request sdk.DesktopOperationAuthorization
	if !decodeDesktopControl(ctx, &request) {
		return
	}
	grant, err := native_service.Default().AuthorizeDesktopOperations(ctx, g.ID, request)
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, desktopOperationProjection(ctx, grant))
}

func currentDesktopOperation(ctx *context.APIContext) (*desktopgrant.OperationGrant, bool) {
	_, app, ok := desktopAuthorization(ctx)
	if !ok {
		return nil, false
	}
	g, err := desktopgrant.Get(ctx, ctx.Params(":grant"))
	if err != nil || g.ActorID != ctx.Doer().ID || g.RepositoryID != ctx.Repo().Repository.ID || g.ProofSHA256 == "" {
		ctx.NotFound()
		return nil, false
	}
	grant, err := desktopgrant.GetOperationGrant(ctx, ctx.Params(":operation_grant"))
	if err != nil || grant.CredentialKind != desktopgrant.CredentialOAuth || grant.ApplicationID != app.ID || grant.ProofSHA256 != g.ProofSHA256 || grant.EnrollmentID != g.ID || grant.RepositoryID != g.RepositoryID || grant.ActorID != g.ActorID {
		ctx.NotFound()
		return nil, false
	}
	if !desktopProofAdmitted(ctx, grant.ProofSHA256) {
		return nil, false
	}
	// A new login may observe/retire the old owner's history using its exact
	// native proof. New effects still revalidate the ORIGINAL OAuth grant in
	// CheckControllerUse; current login never renews captured authority.
	return grant, true
}

func GetDesktopOperationGrant(ctx *context.APIContext) {
	if g, ok := currentDesktopOperation(ctx); ok {
		ctx.JSON(http.StatusOK, desktopOperationProjection(ctx, g))
	}
}

func RevokeDesktopOperationGrant(ctx *context.APIContext) {
	g, ok := currentDesktopOperation(ctx)
	if !ok {
		return
	}
	err := desktopgrant.WithPlacement(ctx, g.RepositoryID, func(ctx stdcontext.Context, _ *desktopgrant.Placement) error {
		return desktopgrant.RevokeOperationGrant(ctx, g)
	})
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	g.Revoked = true
	ctx.JSON(http.StatusOK, desktopOperationProjection(ctx, g))
}

func GetDesktopNativeRevision(ctx *context.APIContext) {
	if _, _, ok := currentDesktopGrant(ctx); !ok {
		return
	}
	status, err := native_service.Default().Status(ctx)
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"revision": status.Revision, "idle": status.Idle, "inhibited": status.Inhibited})
}

func GetDesktopOperation(ctx *context.APIContext) {
	g, ok := currentDesktopOperation(ctx)
	if !ok {
		return
	}
	lookup, err := native_service.Default().Get(ctx, g.InstallationID, ctx.Params(":operation"))
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	if lookup.Record != nil && lookup.Record.ControllerGrantID != g.ID {
		ctx.NotFound()
		return
	}
	ctx.JSON(http.StatusOK, lookup)
}

func ClaimDesktopExecution(ctx *context.APIContext) {
	g, ok := currentDesktopOperation(ctx)
	if !ok {
		return
	}
	var request sdk.ExecutionClaimRequest
	if !decodeDesktopControl(ctx, &request) {
		return
	}
	receipt, err := native_service.Default().ClaimDesktopExecution(ctx, g, request)
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, receipt)
}

// GetDesktopExecution observes the original claim without dispatch admission.
// Current login and native proof permit history reads even after withdrawal;
// neither a retained claim nor its absence grants another native launch.
func GetDesktopExecution(ctx *context.APIContext) {
	g, ok := currentDesktopOperation(ctx)
	if !ok {
		return
	}
	id := ctx.Params(":claim")
	if !desktopID.MatchString(id) {
		ctx.NotFound()
		return
	}
	claim, err := desktopgrant.GetExecutionClaim(ctx, id)
	if errors.Is(err, desktopgrant.ErrNotFound) {
		ctx.NotFound()
		return
	}
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	if claim.GrantID != g.ID || claim.InstallationID != g.InstallationID ||
		claim.RepositoryID != g.RepositoryID || claim.ActorID != g.ActorID ||
		claim.RuntimeID != g.RuntimeID || claim.Generation != g.Generation {
		ctx.NotFound()
		return
	}
	ctx.JSON(http.StatusOK, native_service.ClaimProjection(claim, false))
}

func RetireDesktopExecution(ctx *context.APIContext) {
	g, ok := currentDesktopOperation(ctx)
	if !ok {
		return
	}
	var request sdk.ExecutionRetirement
	if !decodeDesktopControl(ctx, &request) {
		return
	}
	if !desktopProof.MatchString(request.ManifestDigest) || !desktopProof.MatchString(request.ReceiptDigest) || !desktopID.MatchString(ctx.Params(":claim")) {
		desktopControllerError(ctx, native_service.ErrInvalidIntent)
		return
	}
	var claim *desktopgrant.ExecutionClaim
	claimID := ctx.Params(":claim")
	err := desktopgrant.WithPlacement(ctx, g.RepositoryID, func(ctx stdcontext.Context, _ *desktopgrant.Placement) error {
		var err error
		claim, err = desktopgrant.RetireExecutionClaim(ctx, claimID, g.ID, request.ManifestDigest, request.State, request.ReceiptDigest)
		return err
	})
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, native_service.ClaimProjection(claim, false))
}

// SubmitDesktopOperation uses the public OAuth principal and native profile
// proof directly. The private appliance callback and a synthetic PAT are never
// used as desktop authority. Native effects share their existing guarded owner.
func SubmitDesktopOperation(ctx *context.APIContext) {
	g, ok := currentDesktopOperation(ctx)
	if !ok {
		return
	}
	var request sdk.OperationIntent
	if !decodeDesktopControl(ctx, &request) {
		return
	}
	if request.ActorID != strconv.FormatInt(g.ActorID, 10) || request.RepositoryID != strconv.FormatInt(g.RepositoryID, 10) || request.ControllerGrantID != g.ID || request.InstallationID != g.InstallationID {
		desktopControllerError(ctx, native_service.ErrInvalidIntent)
		return
	}
	intent, err := native_service.ValidateIntent(request.OperationID, g.ActorID, g.RepositoryID, request.Kind, request.AuthorizationRevision,
		request.ExpectedNativeRevision, request.NotAfter, request.Payload, time.Now().Unix())
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	receipt, err := native_service.Default().Submit(ctx, extensionauth.SubmissionDecision{ActorID: g.ActorID, CredentialFingerprint: g.Fingerprint(), ControllerGrantID: g.ID}, g.InstallationID, intent)
	if err != nil {
		desktopControllerError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, receipt)
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package extensions

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/desktopgrant"
	"forgejo.org/models/extensionauth"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	native_service "forgejo.org/services/nativeoperation"
)

func resolveRuntimeController(ctx context.Context, entry *callbackAdmission, request sdk.CallbackRequest) (*sdk.RuntimeControllerView, *sdk.RuntimeSelectionReceipt, int) {
	if setting.OAuth2.FactoryAppliancePackageID == "" || entry.authority.ExtensionID != setting.OAuth2.FactoryAppliancePackageID {
		return nil, nil, http.StatusForbidden
	}
	installation, err := humanOperationAdmission(ctx, entry, request.RepositoryID, "")
	if err != nil {
		return nil, nil, http.StatusForbidden
	}
	if _, status := resolveRepository(ctx, entry, request.RepositoryID); status != http.StatusOK {
		return nil, nil, status
	}
	repositoryID, _ := strconv.ParseInt(request.RepositoryID, 10, 64)
	if request.Operation == sdk.OperationRuntimeControllerRead {
		p, err := desktopgrant.ReadPlacement(ctx, repositoryID)
		if err != nil {
			return nil, nil, http.StatusServiceUnavailable
		}
		actor, status := resolveCurrentUser(ctx, entry)
		if status != http.StatusOK {
			return nil, nil, status
		}
		repository, err := repo_model.GetRepositoryByID(ctx, repositoryID)
		if err != nil {
			return nil, nil, http.StatusServiceUnavailable
		}
		permission, err := access_model.GetUserRepoPermission(ctx, repository, actor)
		if err != nil {
			return nil, nil, http.StatusServiceUnavailable
		}
		view := &sdk.RuntimeControllerView{InstallationID: installation, RuntimeID: desktopgrant.ApplianceRuntime(installation), CanSelect: permission.IsAdmin() && permission.CanWrite(unit.TypeCode) && !repository.IsArchived && !repository.IsMirror, Placement: p.Projection(), Actors: []sdk.RuntimeActor{}}
		if view.CanSelect {
			bindings, err := extensionauth.ListBindings(ctx, installation)
			if err != nil {
				return nil, nil, http.StatusServiceUnavailable
			}
			for _, binding := range bindings {
				if binding.RepositoryID != repositoryID {
					continue
				}
				token, actor := strconv.FormatInt(binding.TokenID, 10), strconv.FormatInt(binding.ActorID, 10)
				i := slices.IndexFunc(view.Actors, func(a sdk.RuntimeActor) bool { return a.TokenID == token && a.ActorID == actor })
				if i < 0 {
					view.Actors = append(view.Actors, sdk.RuntimeActor{TokenID: token, ActorID: actor, Operations: []string{binding.Kind}})
				} else {
					view.Actors[i].Operations = append(view.Actors[i].Operations, binding.Kind)
				}
			}
		}
		return view, nil, http.StatusOK
	}
	q := request.RuntimeSelection
	if q == nil || q.InstallationID != installation {
		return nil, nil, http.StatusForbidden
	}
	admit := func(ctx context.Context) error {
		_, err := humanOperationAdmission(ctx, entry, request.RepositoryID, installation)
		return err
	}
	receipt, err := native_service.Default().SelectApplianceRuntime(ctx, installation, entry.authority.ExtensionID, q.RuntimeID, entry.actorID, repositoryID, q.RuntimeSelection, admit)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, native_service.ErrAuthorityLost) {
			status = http.StatusForbidden
		}
		if errors.Is(err, native_service.ErrInvalidIntent) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, native_service.ErrIntentConflict) {
			status = http.StatusConflict
		}
		return nil, nil, status
	}
	if err := admit(ctx); err != nil {
		return nil, nil, http.StatusForbidden
	}
	return nil, &receipt, http.StatusOK
}

func resolveRuntimeAuthorization(ctx context.Context, entry *callbackAdmission, request sdk.CallbackRequest) (*sdk.ApplianceOperationGrant, int) {
	q := request.RuntimeAuthorization
	if q == nil || entry.authority.ExtensionID != setting.OAuth2.FactoryAppliancePackageID {
		return nil, http.StatusForbidden
	}
	repositoryID, _ := strconv.ParseInt(request.RepositoryID, 10, 64)
	admit := func(ctx context.Context) error {
		installation, err := humanOperationAdmission(ctx, entry, request.RepositoryID, q.InstallationID)
		if err != nil || installation != q.InstallationID {
			return native_service.ErrAuthorityLost
		}
		actor, status := resolveCurrentUser(ctx, entry)
		if status != http.StatusOK {
			return native_service.ErrAuthorityLost
		}
		repository, err := repo_model.GetRepositoryByID(ctx, repositoryID)
		if err != nil || repository.IsArchived || repository.IsMirror || user_model.IsBlocked(ctx, repository.OwnerID, actor.ID) {
			return native_service.ErrAuthorityLost
		}
		permission, err := access_model.GetUserRepoPermission(ctx, repository, actor)
		if err != nil || !permission.IsAdmin() || !permission.CanWrite(unit.TypeCode) {
			return native_service.ErrAuthorityLost
		}
		return nil
	}
	g, err := native_service.Default().AuthorizeApplianceOperations(ctx, q.InstallationID, entry.authority.ExtensionID, repositoryID, entry.actorID, *q, admit)
	if err != nil {
		if errors.Is(err, native_service.ErrIntentConflict) {
			return nil, http.StatusConflict
		}
		if errors.Is(err, native_service.ErrInvalidIntent) {
			return nil, http.StatusBadRequest
		}
		return nil, http.StatusForbidden
	}
	if err := admit(ctx); err != nil {
		return nil, http.StatusForbidden
	}
	return runtimeGrantProjection(ctx, g, request.RepositoryID), http.StatusOK
}

func runtimeGrantProjection(ctx context.Context, g *desktopgrant.OperationGrant, repository string) *sdk.ApplianceOperationGrant {
	active := true
	for _, kind := range strings.Split(g.Operations, ",") {
		if _, err := extensionauth.CheckControllerUse(ctx, extensionauth.ControllerUse{GrantID: g.ID, InstallationID: g.InstallationID, PackageID: g.PackageID, ActorID: g.ActorID, RepositoryID: g.RepositoryID, TokenID: g.TokenID, CredentialFingerprint: g.TokenFingerprint, Kind: kind}); err != nil {
			active = false
			break
		}
	}
	return &sdk.ApplianceOperationGrant{ID: g.ID, InstallationID: g.InstallationID, RuntimeID: g.RuntimeID, RepositoryID: repository, TokenID: strconv.FormatInt(g.TokenID, 10), ActorID: strconv.FormatInt(g.ActorID, 10), Generation: g.Generation, SelectionRevision: g.SelectionRevision, Operations: strings.Split(g.Operations, ","), NotAfter: g.NotAfter, Revoked: g.Revoked, Active: active}
}

func resolveRuntimeGrant(ctx context.Context, entry *callbackAdmission, request sdk.CallbackRequest) (*sdk.ApplianceOperationGrant, int) {
	if entry.authority.ExtensionID == "" {
		return nil, http.StatusForbidden
	}
	installation, err := humanOperationAdmission(ctx, entry, request.RepositoryID, "")
	if err != nil {
		return nil, http.StatusForbidden
	}
	if _, status := resolveRepository(ctx, entry, request.RepositoryID); status != http.StatusOK {
		return nil, status
	}
	g, err := desktopgrant.GetOperationGrant(ctx, request.RuntimeGrantID)
	if err != nil || g.CredentialKind != desktopgrant.CredentialPAT || g.InstallationID != installation || g.PackageID != entry.authority.ExtensionID || strconv.FormatInt(g.RepositoryID, 10) != request.RepositoryID {
		return nil, http.StatusNotFound
	}
	return runtimeGrantProjection(ctx, g, request.RepositoryID), http.StatusOK
}

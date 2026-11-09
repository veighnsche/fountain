// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package extensions

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/desktopgrant"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
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
		return &sdk.RuntimeControllerView{InstallationID: installation, RuntimeID: desktopgrant.ApplianceRuntime(installation), CanSelect: permission.IsAdmin() && permission.CanWrite(unit.TypeCode) && !repository.IsArchived && !repository.IsMirror, Placement: p.Projection()}, nil, http.StatusOK
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

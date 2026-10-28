// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	sdk "forgejo.org/extension-sdk"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	extensions_service "forgejo.org/services/extensions"
)

// StartRuntime loads the administrator-selected packages for web, CLI and installer
// identity operations. The caller keeps the package lock until it invokes close.
func StartRuntime(ctx context.Context) (close func() error, err error) {
	if !setting.Extensions.Enabled {
		if len(setting.Extensions.RequiredIDs) != 0 {
			return nil, errors.New("required extensions are configured but extensions are disabled")
		}
		return func() error { return nil }, nil
	}
	manager := extensions_service.NewManager(setting.Extensions.Path, setting.Extensions.RequiredIDs...)
	peers, err := extensions_service.ParseServiceBridgePeers(setting.Extensions.ServiceBridgePeers)
	if err != nil {
		return nil, err
	}
	if err := manager.SetServiceBridgePeers(peers); err != nil {
		return nil, err
	}
	// The trusted catalog is release-supplied configuration, loaded once
	// for the manager lifetime. Required bootstrap and all intent pins
	// resolve against it; it is never derived from browser input.
	if err := manager.SetCatalogPath(setting.Extensions.CatalogPath); err != nil {
		return nil, err
	}
	if err := manager.SetMaintenanceDir(setting.Extensions.MaintenanceDir); err != nil {
		return nil, err
	}
	if err := manager.SetCallbackHandlerFactory(func(instanceID string) http.Handler {
		return BackgroundMux(manager, CallbackHandlerForInstance(instanceID), instanceID, false)
	}); err != nil {
		return nil, err
	}
	// Manager-control endpoints ride the shared service callback
	// socket behind kernel peer mapping, independent of the optional
	// package's runtime or browser admission.
	if err := manager.SetServiceCallbackEndpoint(setting.Extensions.ServiceCallbackPath, ManagerControlMux(manager, BackgroundMux(manager, CallbackHandlerForService(), "", true))); err != nil {
		return nil, err
	}
	if err := manager.SetInstanceStopped(RevokeAdmissionsForInstance); err != nil {
		return nil, err
	}
	if err := manager.SetManifestValidator(func(manifest sdk.Manifest) error {
		for _, page := range manifest.Pages {
			if page.Scope != "global" {
				continue
			}
			for _, route := range page.RouteNames() {
				_, err := user_model.GetUserByName(ctx, route)
				if !user_model.IsErrUserNotExist(err) {
					return fmt.Errorf("native page route %q cannot claim an existing or unavailable account", route)
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := manager.Start(ctx); err != nil {
		return nil, err
	}
	extensions_service.SetDefault(manager)
	return func() error {
		extensions_service.SetDefault(nil)
		return manager.Close()
	}, nil
}

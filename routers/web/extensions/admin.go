// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	stdcontext "context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	extension "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
	extensions_service "forgejo.org/services/extensions"
)

// Native Extensions settings (RCX-2 F04, frozen fountain-design section
// 7). These handlers run behind the live native site-admin guard
// registered with them; they additionally require a session-selected
// site administrator and never consult browser-supplied actor headers.
// Mutations retain the existing cross-origin protection and additionally
// require the existing validAPIOrigin exact Origin check. No parallel
// CSRF-token scheme, no API/PAT/reverse-proxy identity substitution and
// no browser-selected actor headers exist here. Enable and disable act
// through RequestEnabled with caller-supplied compare-and-swap pins: no
// whole-Fountain restart and no hidden CLI step.

// adminFormLimit bounds one mutation body: at most 4 KiB of form URL
// encoding with exactly one expected_generation and one catalog_sha256.
const adminFormLimit = 4 << 10

type adminEntry struct {
	ID               string
	Name             string
	Version          string
	AvailableVersion string
	Required         bool
	Installed        bool
	State            string
	Unconfirmed      bool
	Progress         string
	DesiredEnabled   bool
	Generation       string
	Backend          string
	Effects          string
	SettingsPage     string
}

// AdminExtensions renders installed and declared-available extensions
// with their current observed status.
func AdminExtensions(ctx *context.Context) {
	if !nativeSession(ctx) {
		ctx.Error(http.StatusUnauthorized)
		return
	}
	if !isSessionAdmin(ctx) {
		ctx.Error(http.StatusForbidden)
		return
	}
	entries, catalogSHA := adminInventory(ctx)
	ctx.Data["Title"] = "Extensions"
	ctx.Data["PageIsAdminExtensions"] = true
	ctx.Data["ExtensionEntries"] = entries
	ctx.Data["ExtensionsEnabled"] = setting.Extensions.Enabled
	ctx.Data["CatalogSHA256"] = catalogSHA
	ctx.HTML(http.StatusOK, "admin/extensions")
}

// AdminEnableExtension accepts the current catalog's selected version,
// acquiring and installing it when necessary, then enables it through
// the running manager. Acceptance redirects 303; the manager owns the
// bounded work and JSON status reports progress and errors.
func AdminEnableExtension(ctx *context.Context) {
	adminMutate(ctx, true)
}

// AdminDisableExtension disables the installed target at its pinned
// identity. Required packages stay isolated and are always refused.
func AdminDisableExtension(ctx *context.Context) {
	adminMutate(ctx, false)
}

func adminMutate(ctx *context.Context, enabled bool) {
	if !nativeSession(ctx) {
		ctx.Error(http.StatusUnauthorized)
		return
	}
	if !isSessionAdmin(ctx) {
		ctx.Error(http.StatusForbidden)
		return
	}
	if !validAPIOrigin(ctx.Req) {
		ctx.Error(http.StatusForbidden, "Browser origin is not allowed")
		return
	}
	id := ctx.Params("id")
	if !extension.ValidID(id) {
		ctx.Error(http.StatusBadRequest, "Invalid extension id")
		return
	}
	if !enabled && slices.Contains(setting.Extensions.RequiredIDs, id) {
		ctx.Error(http.StatusForbidden, "Required extension cannot be disabled")
		return
	}
	generation, catalogSHA, reject := parseAdminForm(ctx)
	if reject != nil {
		ctx.Error(reject.code, reject.message)
		return
	}
	manager := extensions_service.GetManager()
	if manager == nil || !setting.Extensions.Enabled {
		ctx.Error(http.StatusServiceUnavailable, "Extension manager is unavailable")
		return
	}
	// The mutation is validated, reserved, and persisted before the
	// bounded work starts on the manager lifetime context. Acceptance
	// redirects 303 immediately; progress and errors appear in status.
	// Only validation rejections return here with no effects.
	result := manager.RequestEnabled(ctx.Req.Context(), id, generation, catalogSHA, enabled)
	operation := "enable"
	if !enabled {
		operation = "disable"
	}
	if result.Error == nil && (result.State == packages.LifecycleActivating || result.State == packages.LifecycleDeactivating) {
		ctx.Redirect(setting.AppSubURL + "/admin/extensions")
		return
	}
	if result.Error == nil && !result.Unconfirmed && (result.State == packages.LifecycleEnabled || result.State == packages.LifecycleDisabled) {
		ctx.Redirect(setting.AppSubURL + "/admin/extensions")
		return
	}
	adminMapFailure(ctx, operation, id, result)
}

// adminReject is one mutation rejection without effects.
type adminReject struct {
	code    int
	message string
}

// parseAdminForm accepts only form URL encoding, at most 4 KiB, with
// exactly one expected_generation and one catalog_sha256. Unknown or
// duplicate fields, query strings and stale-shaped pins are malformed.
// The browser-facing ID comes only from the route; URL, version and
// hash come only from the catalog or the installed receipt.
func parseAdminForm(ctx *context.Context) (generation, catalogSHA string, reject *adminReject) {
	fail := func(message string) (string, string, *adminReject) {
		return "", "", &adminReject{code: http.StatusBadRequest, message: message}
	}
	if ctx.Req.URL != nil && len(ctx.Req.URL.Query()) != 0 {
		return fail("Mutation query strings are not accepted")
	}
	contentType := ctx.Req.Header.Get("Content-Type")
	media, _, _ := strings.Cut(contentType, ";")
	if strings.TrimSpace(strings.ToLower(media)) != "application/x-www-form-urlencoded" {
		return fail("Mutation requires form encoding")
	}
	body, err := io.ReadAll(io.LimitReader(ctx.Req.Body, adminFormLimit+1))
	if err != nil || len(body) > adminFormLimit {
		return fail("Mutation body exceeds limit")
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return fail("Mutation body is malformed")
	}
	if len(values) != 2 {
		return fail("Mutation requires exactly generation and catalog pins")
	}
	generations, ok := values["expected_generation"]
	if !ok || len(generations) != 1 {
		return fail("Mutation requires exactly one expected generation")
	}
	shas, ok := values["catalog_sha256"]
	if !ok || len(shas) != 1 {
		return fail("Mutation requires exactly one catalog pin")
	}
	if _, err := packages.ParseGeneration(generations[0]); err != nil {
		return fail("Mutation generation is malformed")
	}
	if !validAdminSHA(shas[0]) {
		return fail("Mutation catalog pin is malformed")
	}
	return generations[0], shas[0], nil
}

func validAdminSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

// adminMapFailure maps a non-accepting manager result to its rejection
// status without effects, or renders an operational failure on the page
// with 409. Operational failures after acceptance appear in status;
// they never retroactively become activation success.
//
//nolint:unparam // Keep the common operation helper and callback signature.
func adminMapFailure(ctx *context.Context, operation, id string, result packages.LifecycleResult) {
	code := http.StatusConflict
	if result.Error != nil {
		switch result.Error.Code {
		case packages.LifecycleErrInvalidRequest:
			code = http.StatusBadRequest
		case packages.LifecycleErrNotInstalled:
			code = http.StatusNotFound
		case packages.LifecycleErrVersionMismatch:
			code = http.StatusConflict
		case packages.LifecycleErrStaleState:
			code = http.StatusConflict
		case packages.LifecycleErrInProgress:
			code = http.StatusConflict
		case packages.LifecycleErrRequiredPackage:
			code = http.StatusForbidden
		case packages.LifecycleErrMaintenance:
			code = http.StatusLocked
		case packages.LifecycleErrUnavailable:
			code = http.StatusServiceUnavailable
		}
	}
	if code != http.StatusConflict {
		message := "Extension " + operation + " failed"
		if result.Error != nil && result.Error.Message != "" {
			message = result.Error.Message
		}
		ctx.Error(code, message)
		return
	}
	adminFailed(ctx, result)
}

// isSessionAdmin authorizes from the session-selected actor only.
// Displayed buttons and browser-supplied actor headers are never
// authority.
func isSessionAdmin(ctx *context.Context) bool {
	return ctx.Doer != nil && ctx.Doer.IsAdmin && nativeSession(ctx)
}

func adminFailed(ctx *context.Context, result packages.LifecycleResult) {
	entries, catalogSHA := adminInventory(ctx)
	ctx.Data["Title"] = "Extensions"
	ctx.Data["PageIsAdminExtensions"] = true
	ctx.Data["ExtensionEntries"] = entries
	ctx.Data["ExtensionsEnabled"] = setting.Extensions.Enabled
	ctx.Data["CatalogSHA256"] = catalogSHA
	message := "Extension operation failed"
	if result.Error != nil && result.Error.Message != "" {
		message = "Extension operation failed: " + result.Error.Message
	}
	ctx.Data["ExtensionError"] = message
	ctx.HTML(http.StatusConflict, "admin/extensions")
}

func adminInventory(ctx *context.Context) ([]adminEntry, string) {
	manager := extensions_service.GetManager()
	catalogSHA := ""
	if manager != nil {
		catalogSHA = manager.CatalogSHA()
	}
	entries := []adminEntry{}
	installed := map[string]int{}
	root := setting.Extensions.Path
	if names, err := os.ReadDir(root); err == nil {
		for _, name := range names {
			if !name.IsDir() || strings.HasPrefix(name.Name(), ".") || !extension.ValidID(name.Name()) {
				continue
			}
			id := name.Name()
			entry := adminEntry{ID: id, Installed: true, Required: slices.Contains(setting.Extensions.RequiredIDs, id)}
			if manifest, err := extension.LoadManifest(filepath.Join(root, id)); err == nil && manifest.ID == id {
				entry.Name, entry.Version = manifest.Name, manifest.Version
				entry.SettingsPage = adminSettingsPage(manifest)
			}
			installed[id] = len(entries)
			entries = append(entries, entry)
		}
	}
	if manager != nil && setting.Extensions.Enabled {
		for _, declared := range manager.Catalog(ctx.Req.Context()) {
			if index, ok := installed[declared.ID]; ok {
				entries[index].AvailableVersion = declared.Version
				entries[index].Required = entries[index].Required || declared.Required
			} else {
				entries = append(entries, adminEntry{ID: declared.ID, AvailableVersion: declared.Version, Required: declared.Required})
			}
		}
	}
	for i := range entries {
		entry := &entries[i]
		intent, err := packages.LoadIntent(root, entry.ID)
		entry.Generation = "0"
		if err == nil {
			entry.Generation, entry.DesiredEnabled = intent.Generation, entry.Required || intent.DesiredEnabled
		}
		entry.State, entry.Unconfirmed, entry.Progress = adminLiveState(ctx, manager, root, entry.ID)
		// A never-activated catalog entry is available without a package process or
		// host observation. Accepted acquisition still reports its actual busy state.
		if !entry.Installed && err == nil && intent.Generation == "0" && entry.State == packages.LifecycleFailed {
			entry.State, entry.Unconfirmed, entry.Progress = "available", false, "Not installed"
		}
		entry.Backend, entry.Effects = adminBackendEffects(ctx.Req.Context(), manager, entry.State, entry.ID)
	}
	slices.SortFunc(entries, func(a, b adminEntry) int { return strings.Compare(a.ID, b.ID) })
	return entries, catalogSHA
}

// adminSettingsPage returns the first declared admin-scoped page with
// admin permission and an empty manifest route: the generic settings
// slot. Pages with a public route keep their own address.
func adminSettingsPage(manifest extension.Manifest) string {
	for _, page := range manifest.Pages {
		if page.Scope == "admin" && page.Permission == "admin" && page.Route == "" {
			return page.ID
		}
	}
	return ""
}

func adminLiveState(_ *context.Context, manager *extensions_service.Manager, root, id string) (state string, unconfirmed bool, progress string) {
	if manager != nil && setting.Extensions.Enabled {
		observed := manager.Observe(id)
		if observed.Unconfirmed {
			observed.State = packages.LifecycleFailed
		}
		if observed.Error != nil {
			return observed.State, observed.Unconfirmed, observed.Error.Message
		}
		return observed.State, observed.Unconfirmed, observed.Progress
	}
	// Requested state is the authoritative intent; an absent intent is
	// disabled. Malformed state is failed and never silently enabled.
	intent, err := packages.LoadIntent(root, id)
	if err != nil {
		return packages.LifecycleFailed, true, "requested state unavailable"
	}
	if !intent.DesiredEnabled {
		return packages.LifecycleDisabled, false, "manager stopped; next-start state"
	}
	manifest, err := extension.LoadManifest(filepath.Join(root, id))
	if err != nil {
		return packages.LifecycleFailed, false, "package is invalid"
	}
	return packages.LifecycleDisabled, false, "installed " + manifest.Version + "; starts with the manager"
}

// adminBackendEffects derives the frozen backend and effects vocabulary
// from the live backend snapshot and observed lifecycle state. Packages without a lifecycle
// controller have no external effects.
func adminBackendEffects(stdCtx stdcontext.Context, manager *extensions_service.Manager, state, id string) (backend, effects string) {
	backend = "unknown"
	if manager != nil {
		backend = "stopped"
		for _, running := range manager.Snapshot() {
			if running.ID == id {
				backend = "running"
				break
			}
		}
	}
	effects = "unknown"
	if controlled, controller := adminControlled(stdCtx, manager, id); !controlled || controller == "" {
		effects = "not_required"
	} else if state == packages.LifecycleEnabled {
		effects = "running"
	} else if state == packages.LifecycleDisabled || state == "available" {
		effects = "stopped"
	}
	return backend, effects
}

// adminControlled reports whether a package's external effects are
// managed by a required lifecycle controller.
func adminControlled(stdCtx stdcontext.Context, manager *extensions_service.Manager, id string) (bool, string) {
	if manager == nil {
		return false, ""
	}
	if stdCtx == nil {
		stdCtx = stdcontext.Background()
	}
	// The manager's catalog is the release's trusted package set; only
	// its lifecycle_controller binding decides control.
	for _, declared := range manager.Catalog(stdCtx) {
		if declared.ID == id {
			return declared.LifecycleController != "", declared.LifecycleController
		}
	}
	return false, ""
}

// adminStatusRow is the exact section-7 page-refresh row for one
// installed or cataloged package. Unavailable versions and errors are null, never
// empty invented state. Desired state and generation come from the
// authoritative intent; backend, effects, state and progress reuse the
// current observation vocabulary.
type adminStatusRow struct {
	ID               string            `json:"id"`
	Name             *string           `json:"name"`
	Required         bool              `json:"required"`
	AvailableVersion *string           `json:"available_version"`
	InstalledVersion *string           `json:"installed_version"`
	Installed        bool              `json:"installed"`
	DesiredEnabled   bool              `json:"desired_enabled"`
	Generation       string            `json:"generation"`
	Backend          string            `json:"backend"`
	Effects          string            `json:"effects"`
	State            string            `json:"state"`
	Progress         *string           `json:"progress"`
	Error            *adminStatusError `json:"error"`
}

// adminStatusError carries the bounded failure for one row.
type adminStatusError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// adminErrorCode maps draft lifecycle codes to the frozen browser-facing
// status codes.
func adminErrorCode(code string) string {
	switch code {
	case packages.LifecycleErrNotInstalled:
		return "not_found"
	case packages.LifecycleErrActivateFailed, packages.LifecycleErrTeardownFailed:
		return "backend_failed"
	case packages.LifecycleErrUnavailable:
		return "manager_unavailable"
	default:
		return code
	}
}

// AdminExtensionsStatus serves JSON rows for refreshing the page while
// work is pending. It performs no lifecycle effect: rows report durable
// requested state plus current observation only.
func AdminExtensionsStatus(ctx *context.Context) {
	if !nativeSession(ctx) {
		ctx.Error(http.StatusUnauthorized)
		return
	}
	if !isSessionAdmin(ctx) || !validAPIOrigin(ctx.Req) {
		ctx.Error(http.StatusForbidden)
		return
	}
	manager := extensions_service.GetManager()
	if manager == nil || !setting.Extensions.Enabled {
		ctx.Error(http.StatusServiceUnavailable, "Extension manager is unavailable")
		return
	}
	entries, _ := adminInventory(ctx)
	rows := make([]adminStatusRow, 0, len(entries))
	for _, entry := range entries {
		row := adminStatusRow{
			ID: entry.ID, Required: entry.Required, Installed: entry.Installed,
			DesiredEnabled: false, Generation: "0", Backend: entry.Backend,
			Effects: entry.Effects, State: entry.State,
		}
		if entry.Name != "" {
			name := entry.Name
			row.Name = &name
		}
		if entry.AvailableVersion != "" {
			available := entry.AvailableVersion
			row.AvailableVersion = &available
		}
		if entry.Installed && entry.Version != "" {
			installed := entry.Version
			row.InstalledVersion = &installed
		}
		if intent, err := packages.LoadIntent(setting.Extensions.Path, entry.ID); err == nil {
			row.DesiredEnabled = entry.Required || intent.DesiredEnabled
			row.Generation = intent.Generation
		} else {
			row.State = packages.LifecycleFailed
			row.Backend = "unknown"
			row.Effects = "unknown"
			row.Error = &adminStatusError{Code: "unconfirmed", Message: "requested state unavailable"}
		}
		if entry.Unconfirmed {
			row.State = packages.LifecycleFailed
			row.Effects = "unknown"
		}
		if entry.Progress != "" {
			progress := entry.Progress
			row.Progress = &progress
		}
		if row.Error == nil && entry.State == packages.LifecycleFailed {
			observed := manager.Observe(entry.ID)
			if observed.Error != nil {
				row.Error = &adminStatusError{Code: adminErrorCode(observed.Error.Code), Message: observed.Error.Message}
			} else {
				row.Error = &adminStatusError{Code: "unconfirmed", Message: entry.Progress}
			}
		}
		rows = append(rows, row)
	}
	ctx.JSON(http.StatusOK, rows)
}

// AdminExtensionSettings is the generic host-owned wrapper for an
// enabled admin-scoped page whose manifest route is empty. It resolves
// an actual live descriptor, verifies scope and permission and reuses
// native page admission with the existing page rendering and asset and
// API transport. Absent, disabled or failed packages return 404 and
// start nothing.
func AdminExtensionSettings(ctx *context.Context) {
	if !nativeSession(ctx) {
		ctx.Error(http.StatusUnauthorized)
		return
	}
	if !isSessionAdmin(ctx) || !validAPIOrigin(ctx.Req) {
		ctx.Error(http.StatusForbidden)
		return
	}
	id := ctx.Params("id")
	pageID := ctx.Params("page")
	if !extension.ValidID(id) || !extension.ValidID(pageID) {
		ctx.Error(http.StatusNotFound)
		return
	}
	manager := extensions_service.GetManager()
	if manager == nil || !setting.Extensions.Enabled {
		ctx.Error(http.StatusNotFound)
		return
	}
	manifest, err := extension.LoadManifest(filepath.Join(setting.Extensions.Path, id))
	if err != nil || manifest.ID != id {
		ctx.Error(http.StatusNotFound)
		return
	}
	intent, err := packages.LoadIntent(setting.Extensions.Path, id)
	if err != nil || !intent.DesiredEnabled || intent.PendingOperation != nil {
		ctx.Error(http.StatusNotFound)
		return
	}
	if observed := manager.Observe(id); observed.State != packages.LifecycleEnabled {
		ctx.Error(http.StatusNotFound)
		return
	}
	descriptor, transport, ok := manager.Lookup(id)
	if !ok {
		ctx.Error(http.StatusNotFound)
		return
	}
	for _, page := range descriptor.Manifest.Pages {
		if page.ID == pageID && page.Scope == "admin" && page.Permission == "admin" && page.Route == "" {
			renderPage(ctx, descriptor, transport, page, "", nil)
			return
		}
	}
	ctx.Error(http.StatusNotFound)
}

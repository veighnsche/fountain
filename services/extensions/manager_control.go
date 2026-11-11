// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	sdk "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"
)

// Manager-control identity and maintenance exclusion (RCX-2 F04, frozen
// fountain-design section 8). These read-only and cache-only operations
// ride the existing shared service callback socket with kernel peer
// mapping; they are never public HTTP and never accept per-instance
// browser callback routing. Only SERVICE_BRIDGE_PEERS UIDs mapped to a
// required installed lifecycle controller are admitted.
//
// Bounds: state 5s, snapshot 10s, reconcile 300s, cache 600s.
// State/snapshot/reconcile requests are at most 16 KiB; cache at most
// 2 MiB. Every response is at most 128 KiB. Inner operations use the
// lesser remaining caller deadline. Caller cancellation propagates;
// there is no automatic retransmission.
const (
	// ManagerControlStatePath is the read-only current-state endpoint.
	ManagerControlStatePath = "/v1/extensions/manager/state"
	// ManagerControlSnapshotPath snapshots requested state under exclusion.
	ManagerControlSnapshotPath = "/v1/extensions/manager/snapshot"
	// ManagerControlReconcilePath is the explicit post-maintenance trigger.
	ManagerControlReconcilePath = "/v1/extensions/manager/reconcile"
	// ManagerControlCachePath pre-acquires frozen release archives only.
	ManagerControlCachePath = "/v1/extensions/manager/cache"

	managerStateSeconds     = 5
	managerSnapshotSeconds  = 10
	managerReconcileSeconds = 300
	managerCacheSeconds     = 600
)

// CatalogSHA returns the SHA-256 of the manager's loaded trusted catalog.
// It is the value browsers echo as catalog_sha256 in admin mutations.
func (m *Manager) CatalogSHA() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.catalogSHA
}

// ControlPeer admits only the service peer UID explicitly mapped to a
// required lifecycle controller for one manager-control request.
// Group membership or manifest claims alone never authorize a peer.
func (m *Manager) ControlPeer(remoteAddr string) (string, bool) {
	peer, err := sdk.ParseUnixPeer(remoteAddr)
	if err != nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	controllerID := m.serviceBridgePeers[peer.UID]
	if controllerID == "" || !slices.Contains(m.requiredIDs, controllerID) {
		return "", false
	}
	return controllerID, true
}

// controlCatalog returns the manager's current catalog entries and SHA
// without holding the mutex across callbacks.
func (m *Manager) controlCatalog() ([]sdk.CatalogPackage, string) {
	return m.catalogSnapshot()
}

// controlSnapshotEntries builds the sorted snapshot rows: every required
// row with its real installation UUID and receipt pins, plus every
// optional row managed by the authenticated controller, including
// absent and disabled rows. Malformed optional intent rejects the snapshot;
// it must never be mistaken for a generation-zero absence. Missing optional
// UUID/version/digest is null.
func (m *Manager) controlSnapshotEntries(controllerID string) ([]packages.SnapshotEntry, error) {
	entries, _ := m.controlCatalog()
	byID := make(map[string]sdk.CatalogPackage, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
		if !entry.Required && entry.LifecycleController == controllerID {
			intent, err := packages.LoadIntent(m.root, entry.ID)
			if err != nil {
				return nil, err
			}
			if intent.Generation != "0" {
				if err := packages.BindIntentController(m.root, entry.ID, intent.InstallationID, controllerID); err != nil {
					return nil, err
				}
			}
		}
	}
	managed := func(id string) bool {
		if slices.Contains(m.requiredIDs, id) {
			return true
		}
		entry, ok := byID[id]
		return ok && entry.LifecycleController == controllerID
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if managed(entry.ID) {
			ids = append(ids, entry.ID)
		}
	}
	// Catalog-removed rows need their durable source attribution. Known
	// other controllers are excluded; unknown legacy rows cannot be guessed.
	if names, err := os.ReadDir(filepath.Join(m.root, ".intents")); err == nil {
		for _, name := range names {
			id, ok := strings.CutSuffix(name.Name(), ".json")
			if !ok || !sdk.ValidID(id) || managed(id) {
				continue
			}
			if _, current := byID[id]; current {
				continue
			}
			binding, known, err := packages.LoadIntentController(m.root, id)
			if err != nil {
				return nil, err
			}
			if !known {
				return nil, errors.New("catalog-removed intent controller is unknown")
			}
			if binding.ControllerID == controllerID {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	rows := make([]packages.SnapshotEntry, 0, len(ids))
	for _, id := range ids {
		row := packages.SnapshotEntry{PackageID: id, Generation: "0"}
		if slices.Contains(m.requiredIDs, id) {
			row.Required = true
			row.DesiredEnabled = true
			installation, err := packages.LoadInstallation(m.root, id)
			if err != nil || installation == "" {
				return nil, errors.New("required installation identity is unavailable")
			}
			row.InstallationID = &installation
			manifest, err := sdk.LoadManifest(filepath.Join(m.root, id))
			if err != nil || manifest.ID != id {
				return nil, errors.New("required package receipt is unavailable")
			}
			version := manifest.Version
			row.InstalledVersion = &version
			if digest, ok := receiptDigest(m.root, id); ok {
				row.PackageSHA256 = &digest
			} else {
				return nil, errors.New("required package receipt is unavailable")
			}
			rows = append(rows, row)
			continue
		}
		intent, err := packages.LoadIntent(m.root, id)
		if err != nil {
			return nil, errors.New("optional requested state is unavailable")
		}
		row.Generation = intent.Generation
		row.DesiredEnabled = intent.DesiredEnabled
		row.PendingOperation = intent.PendingOperation
		if intent.InstallationID != "" {
			installation := intent.InstallationID
			row.InstallationID = &installation
		}
		if manifest, err := sdk.LoadManifest(filepath.Join(m.root, id)); err == nil && manifest.ID == id {
			version := manifest.Version
			row.InstalledVersion = &version
		}
		if digest, ok := receiptDigest(m.root, id); ok {
			row.PackageSHA256 = &digest
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// receiptDigest returns the verified installed archive digest for one
// package directory. The receipt is generated at install time after the
// archive bytes are verified and the manifest is matched.
func receiptDigest(root, id string) (string, bool) {
	receipt, err := packages.LoadReceipt(filepath.Join(root, id))
	if err != nil || receipt.PackageID != id || receipt.ArchiveSHA256 == "" {
		return "", false
	}
	return receipt.ArchiveSHA256, true
}

// ControlError is one manager-control rejection: an HTTP status with a
// short machine-readable code. Rejections start no effects.
type ControlError struct {
	HTTPStatus int
	Code       string
	Message    string
}

func controlReject(status int, code, message string) *ControlError {
	return &ControlError{HTTPStatus: status, Code: code, Message: message}
}

// ControlStateResponse is the exact current-state fixture shape: the
// controller's readiness plus one constrained snapshot entry. Nulls are
// explicit; admission requires the complete enabled condition and no
// maintenance or drain fence. This read never calls the controller or
// the host; it uses the manager's current observations only.
type ControlStateResponse struct {
	CatalogSHA256      string                 `json:"catalog_sha256"`
	ControllerID       string                 `json:"controller_id"`
	ControllerInstance *string                `json:"controller_instance_id"`
	ControllerReady    bool                   `json:"controller_ready"`
	AdmissionOpen      bool                   `json:"admission_open"`
	Entry              packages.SnapshotEntry `json:"entry"`
}

// ControlSnapshotResponse carries the frozen requested-state generation,
// UUID and desired set plus verified receipt pins.
type ControlSnapshotResponse struct {
	CatalogSHA256 string                   `json:"catalog_sha256"`
	Entries       []packages.SnapshotEntry `json:"entries"`
}

// ControlReconcileEntry is every snapshot field plus the observed
// backend, effects, lifecycle state, admission flag and bounded error.
type ControlReconcileEntry struct {
	PackageID        string                   `json:"package_id"`
	InstallationID   *string                  `json:"installation_id"`
	Generation       string                   `json:"generation"`
	DesiredEnabled   bool                     `json:"desired_enabled"`
	PendingOperation *string                  `json:"pending_operation"`
	InstalledVersion *string                  `json:"installed_version"`
	PackageSHA256    *string                  `json:"package_sha256"`
	Required         bool                     `json:"required"`
	Backend          string                   `json:"backend"`
	Effects          string                   `json:"effects"`
	State            string                   `json:"state"`
	AdmissionOpen    bool                     `json:"admission_open"`
	Error            *packages.LifecycleError `json:"error"`
}

// ControlReconcileResponse reports one synchronous bounded pass in
// package-ID order. A per-package operational failure is a truthful
// failed row with HTTP 200, never extension readiness.
type ControlReconcileResponse struct {
	CatalogSHA256 string                  `json:"catalog_sha256"`
	Entries       []ControlReconcileEntry `json:"entries"`
}

// ControlCachedPackage reports one verified archive in the shared cache.
type ControlCachedPackage struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// ControlCacheResponse lists verified archives in package-ID order.
type ControlCacheResponse struct {
	Cached []ControlCachedPackage `json:"cached"`
}

// controlledBy reports whether a catalog entry's external effects are
// managed by the given required controller.
func controlledBy(entry sdk.CatalogPackage, controllerID string) bool {
	return entry.LifecycleController == controllerID
}

// backendOf maps the running probe to the frozen backend vocabulary.
func backendOf(running, unknown bool) string {
	if running {
		return "running"
	}
	if unknown {
		return "unknown"
	}
	return "stopped"
}

// controlRead copies the controller readiness and fence state under one
// brief lock. No HTTP, download, process wait or callback runs here.
func (m *Manager) controlRead(controllerID string) (instanceID string, ready bool, draining map[string]bool, sha string, started bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state, ok := m.controllers[controllerID]; ok {
		instanceID, ready = state.instanceID, state.ready
	}
	draining = make(map[string]bool, len(m.draining))
	for id := range m.draining {
		draining[id] = true
	}
	return instanceID, ready, draining, m.catalogSHA, m.started
}

// admissionFor computes the ordinary admission flag: the complete
// enabled condition with no maintenance or drain fence. The distinct
// lifecycle token's setup exception is evaluated on the lifecycle
// path, never here.
func admissionFor(desired bool, pending *string, enabledObserved, backendRunning, effectsRunning, controllerReady, fenced, draining bool) bool {
	if !desired || pending != nil || !enabledObserved || !backendRunning || !effectsRunning {
		return false
	}
	if !controllerReady || fenced || draining {
		return false
	}
	return true
}

// ControlState serves one constrained current-state read for a catalog
// target controlled by the authenticated controller.
func (m *Manager) ControlState(ctx context.Context, controllerID, packageID string) (ControlStateResponse, *ControlError) {
	var empty ControlStateResponse
	if err := ctx.Err(); err != nil {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension manager is not running")
	}
	entries, _ := m.controlCatalog()
	var target *sdk.CatalogPackage
	for i := range entries {
		if entries[i].ID == packageID && controlledBy(entries[i], controllerID) {
			target = &entries[i]
			break
		}
	}
	if target == nil {
		return empty, controlReject(http.StatusForbidden, "forbidden", "package is not controlled by this controller")
	}
	instanceID, ready, _, currentSHA, started := m.controlRead(controllerID)
	if !started {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension manager is not running")
	}
	rows, err := m.controlSnapshotEntries(controllerID)
	if err != nil {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension snapshot storage is invalid")
	}
	var row packages.SnapshotEntry
	found := false
	for _, candidate := range rows {
		if candidate.PackageID == packageID {
			row = candidate
			found = true
			break
		}
	}
	if !found {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension snapshot storage is invalid")
	}
	// The admission projection is the unified ordinary-admission
	// predicate: open only after durable activation, never from a
	// merely registered backend.
	admission := m.ordinaryAdmissionOpen(packageID)
	response := ControlStateResponse{
		CatalogSHA256:   currentSHA,
		ControllerID:    controllerID,
		ControllerReady: ready,
		AdmissionOpen:   admission,
		Entry:           row,
	}
	if instanceID != "" {
		response.ControllerInstance = &instanceID
	}
	return response, nil
}

// ControlSnapshot serves the frozen requested-state snapshot. The native
// updater already holds exclusion, so no shared lock is taken here.
// It fails 409 while a mutation is active or an intent carries pending
// or unknown effects, and 503 on invalid storage.
func (m *Manager) ControlSnapshot(ctx context.Context, controllerID string) (ControlSnapshotResponse, *ControlError) {
	var empty ControlSnapshotResponse
	if err := ctx.Err(); err != nil {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension manager is not running")
	}
	m.mu.Lock()
	mutation := m.mutation
	sha := m.catalogSHA
	started := m.started
	m.mu.Unlock()
	if !started {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension manager is not running")
	}
	if mutation != "" {
		return empty, controlReject(http.StatusConflict, "conflict", "another extension operation is active")
	}
	rows, err := m.controlSnapshotEntries(controllerID)
	if err != nil {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension snapshot storage is invalid")
	}
	for _, row := range rows {
		if row.PendingOperation != nil {
			return empty, controlReject(http.StatusConflict, "conflict", "an extension intent has a pending operation")
		}
	}
	entries, _ := m.controlCatalog()
	trust := packages.SourceTrust{Catalog: sdk.Catalog{Version: sdk.CatalogVersion, Packages: entries}, CatalogSHA256: sha, RequiredIDs: slices.Clone(m.requiredIDs), ControllerID: controllerID}
	if err := packages.VerifySourceSnapshot(m.root, packages.Snapshot{CatalogSHA256: sha, Entries: rows}, trust); err != nil {
		return empty, controlReject(http.StatusConflict, "stale_state", "source snapshot does not match live state")
	}
	m.mu.Lock()
	afterMutation := m.mutation
	afterSHA := m.catalogSHA
	m.mu.Unlock()
	if afterMutation != "" || afterSHA != sha {
		return empty, controlReject(http.StatusConflict, "conflict", "extension state changed during snapshot")
	}
	return ControlSnapshotResponse{CatalogSHA256: sha, Entries: rows}, nil
}

// reconcileRow builds one truthful reconcile row. Absent never-activated
// rows are available with stopped backend and effects and perform no host
// call. Complete desired-enabled rows require enabled with open
// admission; complete desired-disabled rows require available or disabled
// with stopped effects and closed admission. Anything else carries a
// non-null bounded error. Controller or socket unavailability yields
// failed rows with error unavailable.
func (m *Manager) reconcileRow(row packages.SnapshotEntry, catalogEntry *sdk.CatalogPackage, controllerReady, draining, fenced bool) ControlReconcileEntry {
	entry := ControlReconcileEntry{
		PackageID:        row.PackageID,
		InstallationID:   row.InstallationID,
		Generation:       row.Generation,
		DesiredEnabled:   row.DesiredEnabled,
		PendingOperation: row.PendingOperation,
		InstalledVersion: row.InstalledVersion,
		PackageSHA256:    row.PackageSHA256,
		Required:         row.Required,
	}
	_, installed := os.Lstat(filepath.Join(m.root, row.PackageID))
	absent := row.Generation == "0" && !row.DesiredEnabled && errors.Is(installed, os.ErrNotExist)
	if absent {
		entry.Backend = "stopped"
		entry.Effects = "stopped"
		entry.State = "available"
		return entry
	}
	observed := m.Observe(row.PackageID)
	running, unknown := m.backendRunning(row.PackageID)
	entry.Backend = backendOf(running, unknown)
	if catalogEntry != nil && catalogEntry.LifecycleController == "" && !row.Required {
		entry.Effects = "not_required"
	} else if observed.Unconfirmed {
		entry.Effects = "unknown"
	} else {
		entry.Effects = entry.Backend
	}
	entry.State = observed.State
	effectsRunning := entry.Effects == "running" || entry.Effects == "not_required"
	entry.AdmissionOpen = admissionFor(row.DesiredEnabled, row.PendingOperation,
		observed.State == packages.LifecycleEnabled, entry.Backend == "running",
		effectsRunning, controllerReady, fenced, draining)
	completeEnabled := row.DesiredEnabled && row.PendingOperation == nil &&
		observed.State == packages.LifecycleEnabled && entry.AdmissionOpen
	completeDisabled := !row.DesiredEnabled && row.PendingOperation == nil &&
		(observed.State == packages.LifecycleDisabled || entry.State == "available") &&
		(entry.Effects == "stopped" || entry.Effects == "not_required") && !entry.AdmissionOpen
	if completeEnabled || completeDisabled {
		return entry
	}
	if observed.Error != nil {
		entry.Error = observed.Error
		return entry
	}
	if row.DesiredEnabled && !controllerReady {
		entry.Error = &packages.LifecycleError{Code: packages.LifecycleErrUnavailable, Message: "lifecycle controller is unavailable"}
		return entry
	}
	entry.Error = &packages.LifecycleError{Code: packages.LifecycleErrUnconfirmed, Message: "requested and observed state disagree"}
	return entry
}

// ControlReconcile runs one synchronous bounded reconciliation pass
// over stable intent after obtaining the normal shared maintenance
// guard: the same ordinary boot reconciliation (grant adoption and
// backend admission for stable enabled rows, residual stops for stable
// disabled rows with definite running effects) with no package
// downloads, no generation rewrites, and no pending replays. It never
// clears an interrupted pending effect and never invents a user
// decision. Timeout cancels child work and returns failed or unknown
// rows within the total ceiling.
func (m *Manager) ControlReconcile(ctx context.Context, controllerID string) (ControlReconcileResponse, *ControlError) {
	var empty ControlReconcileResponse
	if err := ctx.Err(); err != nil {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "reconcile request already cancelled")
	}
	budget := time.Duration(managerReconcileSeconds) * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "reconcile deadline already elapsed")
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	guard, err := acquireMaintenanceGuard(m.maintenanceDir)
	if err != nil {
		return empty, controlReject(http.StatusLocked, "maintenance", "maintenance holds exclusive exclusion")
	}
	defer guard()
	m.mu.Lock()
	mutation := m.mutation
	sha := m.catalogSHA
	started := m.started
	m.mu.Unlock()
	if !started {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension manager is not running")
	}
	if mutation != "" {
		return empty, controlReject(http.StatusConflict, "conflict", "another extension operation is active")
	}
	rows, err := m.controlSnapshotEntries(controllerID)
	if err != nil {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension snapshot storage is invalid")
	}
	for _, row := range rows {
		if row.PendingOperation != nil {
			return empty, controlReject(http.StatusConflict, "conflict", "an extension intent has a pending operation")
		}
	}
	if err := m.acquireMutation("reconcile"); err != nil {
		return empty, controlReject(http.StatusConflict, "conflict", "another extension operation is active")
	}
	defer m.releaseMutation()
	// Ordinary boot reconciliation for stable intent runs here, inside
	// the same synchronous bounded pass: grant adoption and backend
	// admission for stable enabled rows, residual stops for stable
	// disabled rows with definite running effects. No package
	// downloads, no generation rewrites, no pending replays; a
	// maintenance-deferred target resumes once exclusion drops because
	// this pass takes the normal shared guard above.
	m.reconcileStablePass(ctx, false, nil)
	rows, err = m.controlSnapshotEntries(controllerID)
	if err != nil {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension snapshot storage is invalid")
	}
	entries, _ := m.controlCatalog()
	byID := make(map[string]*sdk.CatalogPackage, len(entries))
	for i := range entries {
		byID[entries[i].ID] = &entries[i]
	}
	_, controllerReady, draining, _, _ := m.controlRead(controllerID)
	fenced := maintenanceHeld(m.maintenanceDir)
	response := ControlReconcileResponse{CatalogSHA256: sha, Entries: make([]ControlReconcileEntry, 0, len(rows))}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			timedOut := ControlReconcileEntry{
				PackageID: row.PackageID, InstallationID: row.InstallationID,
				Generation: row.Generation, DesiredEnabled: row.DesiredEnabled,
				PendingOperation: row.PendingOperation, InstalledVersion: row.InstalledVersion,
				PackageSHA256: row.PackageSHA256, Required: row.Required,
				Backend: "unknown", Effects: "unknown", State: packages.LifecycleFailed,
				Error: &packages.LifecycleError{Code: packages.LifecycleErrUnconfirmed, Message: "reconcile pass timed out"},
			}
			response.Entries = append(response.Entries, timedOut)
			continue
		}
		response.Entries = append(response.Entries, m.reconcileRow(row, byID[row.PackageID], controllerReady, draining[row.PackageID], fenced))
	}
	return response, nil
}

// ControlCache validates the frozen source selection against live state
// and streams the already-verified target archives into the shared SHA
// cache. It alters no active catalog, installed package, intent, runtime
// admission or effects. It takes the one-operation slot but no shared
// directory lock, because the trusted updater already holds exclusive
// exclusion. Caller cancellation stops acquisition; verified cache
// entries remain reusable.
func (m *Manager) ControlCache(ctx context.Context, controllerID string, target sdk.Catalog, packageIDs []string, source packages.Snapshot) (ControlCacheResponse, *ControlError) {
	var empty ControlCacheResponse
	budget := time.Duration(managerCacheSeconds) * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "cache deadline already elapsed")
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := sdk.ValidateCatalog(target); err != nil {
		return empty, controlReject(http.StatusBadRequest, "invalid_request", "target catalog is invalid")
	}
	byID := make(map[string]sdk.CatalogPackage, len(target.Packages))
	for _, entry := range target.Packages {
		byID[entry.ID] = entry
	}
	expected := make(map[string]bool)
	for _, entry := range target.Packages {
		if entry.Required {
			expected[entry.ID] = true
		}
	}
	for _, row := range source.Entries {
		if row.DesiredEnabled && !expected[row.PackageID] {
			if entry, ok := byID[row.PackageID]; ok && !entry.Required {
				expected[row.PackageID] = true
			}
		}
	}
	if len(packageIDs) != len(expected) {
		return empty, controlReject(http.StatusBadRequest, "invalid_request", "cache selection must be required plus desired-enabled packages")
	}
	selected := make(map[string]bool, len(packageIDs))
	for _, id := range packageIDs {
		entry, ok := byID[id]
		if !ok || selected[id] || !expected[id] || !entry.Required && entry.LifecycleController != controllerID {
			return empty, controlReject(http.StatusBadRequest, "invalid_request", "cache selection must be required plus desired-enabled packages")
		}
		selected[id] = true
	}
	m.mu.Lock()
	trust := packages.SourceTrust{Catalog: sdk.Catalog{Version: sdk.CatalogVersion, Packages: append([]sdk.CatalogPackage(nil), m.catalog...)}, CatalogSHA256: m.catalogSHA, RequiredIDs: slices.Clone(m.requiredIDs), ControllerID: controllerID}
	started := m.started
	m.mu.Unlock()
	if !started {
		return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "extension manager is not running")
	}
	if err := m.acquireMutation("cache"); err != nil {
		return empty, controlReject(http.StatusConflict, "conflict", "another extension operation is active")
	}
	defer m.releaseMutation()
	if err := packages.VerifySourceSnapshot(m.root, source, trust); err != nil {
		return empty, controlReject(http.StatusConflict, "stale_state", "source snapshot does not match live state")
	}
	sorted := slices.Clone(packageIDs)
	slices.Sort(sorted)
	response := ControlCacheResponse{Cached: make([]ControlCachedPackage, 0, len(sorted))}
	for _, id := range sorted {
		entry := byID[id]
		remaining, ok := controlRemaining(ctx, time.Duration(managerReconcileSeconds)*time.Second/2)
		if !ok {
			return empty, controlReject(http.StatusServiceUnavailable, "unavailable", "cache deadline already elapsed")
		}
		downloadCtx, downloadCancel := context.WithTimeout(ctx, remaining)
		stage, err := packages.AcquireWithClient(downloadCtx, m.root, entry, nil)
		downloadCancel()
		if err != nil {
			return empty, controlReject(http.StatusBadGateway, "acquire_failed", "selected package bytes are missing or corrupt")
		}
		_ = os.RemoveAll(stage)
		response.Cached = append(response.Cached, ControlCachedPackage{
			ID: entry.ID, Version: entry.Version, SHA256: entry.SHA256, Size: entry.Size,
		})
	}
	return response, nil
}

// controlRemaining caps one download at the lesser of the per-download
// ceiling and the call's remaining time.
func controlRemaining(ctx context.Context, ceiling time.Duration) (time.Duration, bool) {
	remaining := ceiling
	if deadline, ok := ctx.Deadline(); ok {
		remaining = min(time.Until(deadline), ceiling)
	}
	if remaining <= 0 {
		return 0, false
	}
	return remaining, true
}

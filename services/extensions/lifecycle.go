// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	sdk "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"
	"forgejo.org/modules/log"
)

// ExtensionSnapshot is Fountain's authenticated snapshot of one running
// extension for maintenance selection: ID, version and installation.
type ExtensionSnapshot struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	InstallationID string `json:"installation_id"`
	Required       bool   `json:"required"`
}

// Snapshot lists running extensions with their versions and installations.
func (m *Manager) Snapshot() []ExtensionSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := make([]ExtensionSnapshot, 0, len(m.running))
	for id, item := range m.running {
		if item.client.Exited() {
			continue
		}
		snapshot = append(snapshot, ExtensionSnapshot{
			ID:             id,
			Version:        item.descriptor.Manifest.Version,
			InstallationID: item.installation,
			Required:       slices.Contains(m.requiredIDs, id),
		})
	}
	slices.SortFunc(snapshot, func(a, b ExtensionSnapshot) int { return strings.Compare(a.ID, b.ID) })
	return snapshot
}

// Observe reports the current lifecycle state without causing effects.
// It is a local non-recursive authority read: one brief mutex snapshot,
// then file reads and pure computation, never a controller or host
// callback. A running backend with stopped, unknown, or grantless
// effects is failed, never enabled. A read never changes desired state.
func (m *Manager) Observe(id string) packages.LifecycleResult {
	if !sdk.ValidID(id) {
		return packages.FailedResult(sdk.LifecycleObserve, id, packages.LifecycleErrInvalidRequest, "invalid extension id")
	}
	m.mu.Lock()
	snapshot := m.observeSnapshotLocked(id)
	m.mu.Unlock()
	intent, intentErr := packages.LoadIntent(m.root, id)
	base := observeBaseResult(m.root, id, snapshot, intent, intentErr, m.lastFailure(id))
	m.obsMu.Lock()
	effect, ok := m.lastObs[id]
	m.obsMu.Unlock()
	return mergeControlledEffect(base, id, snapshot.controlled, snapshot.controllerReady, snapshot.controllerInstance, intent, intentErr, snapshot.backendAlive, effect, ok)
}

// observeSnapshot captures everything Observe needs under one brief
// lock. No HTTP, download, process wait, or callback runs here.
type observeSnapshot struct {
	started            bool
	busyOp             string
	busy               bool
	draining           bool
	backendAlive       bool
	backendVersion     string
	controlled         bool
	controllerReady    bool
	controllerInstance string
}

// observeSnapshotLocked fills an observeSnapshot; callers hold m.mu.
func (m *Manager) observeSnapshotLocked(id string) observeSnapshot {
	snapshot := observeSnapshot{
		started:  m.started,
		draining: m.draining[id],
	}
	if operation, busy := m.busy[id]; busy {
		snapshot.busyOp, snapshot.busy = operation, true
	}
	if item := m.running[id]; item != nil && !item.client.Exited() {
		snapshot.backendAlive = true
		snapshot.backendVersion = item.descriptor.Manifest.Version
	}
	for _, entry := range m.catalog {
		if entry.ID == id && entry.LifecycleController != "" {
			snapshot.controlled = true
			if state, ok := m.controllers[entry.LifecycleController]; ok && state.ready {
				if backend := m.running[entry.LifecycleController]; backend != nil && !backend.client.Exited() {
					snapshot.controllerReady = true
					snapshot.controllerInstance = state.instanceID
				}
			}
		}
	}
	return snapshot
}

// observeBaseResult computes the backend-and-intent reading from a
// snapshot plus the loaded intent. It performs no locking and no
// controller calls.
func observeBaseResult(root, id string, snapshot observeSnapshot, intent packages.Intent, intentErr error, failure *packages.LifecycleError) packages.LifecycleResult {
	if !snapshot.started {
		return packages.FailedResult(sdk.LifecycleObserve, id, packages.LifecycleErrUnavailable, "extension manager is not running")
	}
	if snapshot.busy {
		state := packages.LifecycleActivating
		if snapshot.busyOp == sdk.LifecycleDeactivate {
			state = packages.LifecycleDeactivating
		}
		return packages.LifecycleResult{Operation: sdk.LifecycleObserve, PackageID: id, State: state, Progress: "lifecycle operation in progress"}
	}
	if snapshot.draining {
		// busy above is the live operation. Draining outlives a failed stop
		// to keep admission closed; it must not hide that completed failure.
		observed := packages.UnconfirmedResult(sdk.LifecycleObserve, id, packages.LifecycleErrUnconfirmed, "disable outcome is unconfirmed; admission closed; explicit retry is required")
		if failure != nil {
			observed.Error = failure
		}
		return observed
	}
	if snapshot.backendAlive {
		observed := packages.LifecycleResult{Operation: sdk.LifecycleObserve, PackageID: id, ObservedVersion: snapshot.backendVersion, State: packages.LifecycleEnabled, Progress: "running"}
		// A failed operation with unknown external effects stays
		// visible while the backend keeps running. Last error text
		// is advisory in-memory state; the persisted intent carries
		// only the interruption marker.
		if intentErr == nil && intent.PendingOperation != nil {
			if installation, _ := packages.LoadInstallation(root, id); installation != "" && installation == intent.InstallationID {
				observed.Unconfirmed = true
				observed.Error = failure
				observed.Progress = "running; last operation effect unconfirmed"
			}
		}
		return observed
	}
	if intentErr != nil {
		return packages.UnconfirmedResult(sdk.LifecycleObserve, id, packages.LifecycleErrUnconfirmed, "requested state unavailable; effect unknown")
	}
	if intent.PendingOperation != nil {
		if installation, _ := packages.LoadInstallation(root, id); installation != "" && installation == intent.InstallationID {
			operation := sdk.LifecycleActivate
			if !intent.DesiredEnabled {
				operation = sdk.LifecycleDeactivate
			}
			observed := packages.UnconfirmedResult(operation, id, packages.LifecycleErrUnconfirmed, "last operation effect unconfirmed")
			if failure != nil {
				observed.Error = failure
			}
			return observed
		}
	}
	// Accepted acquisition saves an interruption marker before package files
	// exist. Preserve its failed operation above before reporting mere absence.
	if _, err := os.Lstat(filepath.Join(root, id)); errors.Is(err, os.ErrNotExist) {
		if failure != nil {
			return packages.FailedResult(sdk.LifecycleObserve, id, failure.Code, failure.Message)
		}
		return packages.FailedResult(sdk.LifecycleObserve, id, packages.LifecycleErrNotInstalled, "package is not installed")
	}
	if intent.DesiredEnabled {
		return packages.UnconfirmedResult(sdk.LifecycleObserve, id, packages.LifecycleErrUnconfirmed, "requested enabled but not running; effect unknown")
	}
	return packages.LifecycleResult{Operation: sdk.LifecycleObserve, PackageID: id, State: packages.LifecycleDisabled, Progress: "disabled"}
}

// mergeControlledEffect is the pure merge core: effect carries the last
// controller-confirmed fact (or none) for the package. The effect
// journal is written by completed lifecycle calls only; this merge
// never calls the controller or host itself. Anything less than a
// current confirmation of the desired outcome fails closed as
// unconfirmed.
//
//nolint:unparam // Keep the common operation helper and callback signature.
func mergeControlledEffect(base packages.LifecycleResult, id string, controlled, controllerReady bool, controllerInstance string, intent packages.Intent, intentErr error, backendAlive bool, effect controlledEffect, ok bool) packages.LifecycleResult {
	if !controlled {
		return base
	}
	if intentErr != nil || intent.Generation == "0" {
		return base
	}
	if intent.PendingOperation != nil {
		return base
	}
	fail := func(message string) packages.LifecycleResult {
		out := base
		out.State = packages.LifecycleFailed
		out.Unconfirmed = true
		out.Error = &packages.LifecycleError{Code: packages.LifecycleErrUnconfirmed, Message: message}
		return out
	}
	if !controllerReady {
		if base.State == packages.LifecycleEnabled {
			return fail("controller is unreachable; effects unknown")
		}
		if base.State == packages.LifecycleDisabled {
			return fail("controller is unreachable; disabled effects are unobserved")
		}
		return base
	}
	current := ok && effect.generation == intent.Generation && effect.installation == intent.InstallationID && effect.instanceID == controllerInstance
	if !current {
		if base.State == packages.LifecycleEnabled {
			return fail("external effects are unconfirmed")
		}
		if base.State == packages.LifecycleDisabled {
			// An absent or stale observation is never proof of
			// stopped effects: only a current stopped fact
			// confirms disabled. Anything else stays visible
			// for an explicit disable.
			return fail("external effects are unobserved; explicit disable is required")
		}
		return base
	}
	if intent.DesiredEnabled {
		if backendAlive && effect.state == sdk.LifecycleStateRunning && effect.admission {
			return base
		}
		return fail("requested and observed state disagree")
	}
	if !backendAlive && effect.state == sdk.LifecycleStateStopped {
		// A confirmed teardown remains disabled when optional package bytes
		// have disappeared; the persisted intent and controller own cleanup.
		base.State = packages.LifecycleDisabled
		base.Unconfirmed = false
		base.Error = nil
		base.Progress = "external effects stopped"
		return base
	}
	return fail("residual effects need an explicit disable")
}

// Activate enables a package live through the unified mutation path:
// the current generation is the compare-and-swap base, requested state
// persists before effects, and controlled targets additionally consult
// their required controller before the backend starts. No
// whole-Fountain restart or administrator CLI step is part of the
// journey. A version pin that does not match refuses before effects.
func (m *Manager) Activate(ctx context.Context, request packages.LifecycleRequest) packages.LifecycleResult {
	if err := packages.ValidateLifecycleRequest(request); err != nil || request.Operation != sdk.LifecycleActivate {
		return packages.FailedResult(sdk.LifecycleActivate, request.PackageID, packages.LifecycleErrInvalidRequest, "invalid activation request")
	}
	id := request.PackageID
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return packages.FailedResult(sdk.LifecycleActivate, id, packages.LifecycleErrUnavailable, "extension manager is not running")
	}
	if _, busy := m.busy[id]; busy || m.draining[id] {
		m.mu.Unlock()
		return packages.FailedResult(sdk.LifecycleActivate, id, packages.LifecycleErrInProgress, "another lifecycle operation is in progress")
	}
	if item := m.running[id]; item != nil {
		if item.client.Exited() {
			m.stopOne(id, item, true)
		} else {
			version := item.descriptor.Manifest.Version
			m.mu.Unlock()
			if request.PackageVersion != "" && request.PackageVersion != version {
				return packages.FailedResult(sdk.LifecycleActivate, id, packages.LifecycleErrVersionMismatch, "installed version does not match request")
			}
			return packages.LifecycleResult{Operation: sdk.LifecycleActivate, PackageID: id, ObservedVersion: version, State: packages.LifecycleEnabled, Progress: "already running"}
		}
	}
	sha := m.catalogSHA
	m.mu.Unlock()
	intent, err := packages.LoadIntent(m.root, id)
	if err != nil {
		return packages.FailedResult(sdk.LifecycleActivate, id, packages.LifecycleErrUnavailable, "requested state is unavailable")
	}
	return m.requestEnabled(ctx, id, request.PackageVersion, intent.Generation, sha, true)
}

// activationRefusal marks errors raised before any effect.
type activationRefusal struct {
	versionMismatch bool
	err             error
}

func (e *activationRefusal) Error() string { return e.err.Error() }

func isActivationRefusal(err error) (notInstalled, versionMismatch bool) {
	var refusal *activationRefusal
	if !errors.As(err, &refusal) {
		return false, false
	}
	return !refusal.versionMismatch, refusal.versionMismatch
}

func refuseActivation(versionMismatch bool, err error) error {
	return &activationRefusal{versionMismatch: versionMismatch, err: err}
}

// Deactivate disables a package live through the unified mutation path:
// requested state persists before stop work, new admission closes
// first and irreversibly within the attempt, controlled targets verify
// teardown through their required controller even when the backend is
// already absent, and the backend stops only after verified effects.
// Required packages are isolated and always refused. Package bytes and
// private configuration are retained; unknown remainder stays pending
// with admission closed.
func (m *Manager) Deactivate(ctx context.Context, request packages.LifecycleRequest) packages.LifecycleResult {
	if err := packages.ValidateLifecycleRequest(request); err != nil || request.Operation != sdk.LifecycleDeactivate {
		return packages.FailedResult(sdk.LifecycleDeactivate, request.PackageID, packages.LifecycleErrInvalidRequest, "invalid deactivation request")
	}
	id := request.PackageID
	if slices.Contains(m.requiredIDs, id) {
		return packages.FailedResult(sdk.LifecycleDeactivate, id, packages.LifecycleErrRequiredPackage, "required extension cannot be disabled")
	}
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return packages.FailedResult(sdk.LifecycleDeactivate, id, packages.LifecycleErrUnavailable, "extension manager is not running")
	}
	// A draining package closed admission during an earlier attempt; an
	// explicit disable retry is still allowed while no command is
	// active. Only a live operation serializes here.
	if _, busy := m.busy[id]; busy {
		m.mu.Unlock()
		return packages.FailedResult(sdk.LifecycleDeactivate, id, packages.LifecycleErrInProgress, "another lifecycle operation is in progress")
	}
	sha := m.catalogSHA
	m.mu.Unlock()
	intent, err := packages.LoadIntent(m.root, id)
	if err != nil {
		return packages.FailedResult(sdk.LifecycleDeactivate, id, packages.LifecycleErrUnavailable, "requested state is unavailable")
	}
	return m.requestEnabled(ctx, id, "", intent.Generation, sha, false)
}

func claimedRouteConflict(running map[string]*running, manifest sdk.Manifest) string {
	claimed := make(map[string]bool)
	for _, item := range running {
		for _, page := range item.descriptor.Manifest.Pages {
			for _, route := range page.RouteNames() {
				claimed[page.Scope+"/"+route] = true
			}
			for _, sub := range page.Subroutes {
				claimed[page.Scope+"/"+page.SubrouteRoute(sub)+"/"+sub.Pattern] = true
			}
		}
	}
	for _, page := range manifest.Pages {
		for _, route := range page.RouteNames() {
			if claimed[page.Scope+"/"+route] {
				return page.Scope + "/" + route
			}
		}
	}
	return ""
}

// recordResult persists the draft operation outcome into the
// authoritative intent: desired state plus an interruption marker while
// the effect is unconfirmed or transitional, cleared once the outcome is
// definite. Generations advance once per recorded outcome under
// compare-and-swap; a lost race only logs, since the winner's newer
// generation stays authoritative. F03 owns exact operation and recovery
// ordering on top of this durable layer.
func (m *Manager) recordResult(id string, desired bool, result packages.LifecycleResult) {
	installation, err := packages.LoadInstallation(m.root, id)
	if err != nil {
		// Without an installation UUID there is no identity to bind
		// the outcome to; only malformed identity is worth logging.
		if !errors.Is(err, os.ErrNotExist) {
			log.Error("Extension %s lifecycle observation unavailable: %v", id, err)
		}
		return
	}
	current, err := packages.LoadIntent(m.root, id)
	if err != nil {
		log.Error("Extension %s lifecycle observation unavailable: %v", id, err)
		return
	}
	var pending *string
	if result.Unconfirmed || result.State == packages.LifecycleActivating || result.State == packages.LifecycleDeactivating {
		operation := packages.IntentActivate
		if !desired {
			operation = packages.IntentDeactivate
		}
		pending = &operation
	}
	if _, err := packages.StoreIntent(m.root, id, installation, desired, pending, current.Generation); err != nil {
		log.Error("Extension %s lifecycle observation unavailable: %v", id, err)
	}
	m.errMu.Lock()
	if result.Error != nil {
		failure := *result.Error
		m.lastErr[id] = &failure
	} else {
		delete(m.lastErr, id)
	}
	m.errMu.Unlock()
}

// lastFailure returns the advisory in-memory failure for one package, if any.
func (m *Manager) lastFailure(id string) *packages.LifecycleError {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	if failure := m.lastErr[id]; failure != nil {
		duplicate := *failure
		return &duplicate
	}
	return nil
}

// Catalog returns the release's trusted catalog entries loaded once at
// startup, or nil when no catalog is configured.
func (m *Manager) Catalog(ctx context.Context) []sdk.CatalogPackage {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sdk.CatalogPackage(nil), m.catalog...)
}

// InstallAndActivate acquires one declared catalog package and activates it
// live. The download verifies exact size and SHA-256 before installation;
// staging never mutates installed packages. The swap runs under manager
// serialization while the manager holds the package lock, then activation
// observes the backend before opening admission.
func (m *Manager) InstallAndActivate(ctx context.Context, entry sdk.CatalogPackage, replace bool) packages.LifecycleResult {
	if err := sdk.ValidateCatalogPackage(entry); err != nil {
		return packages.FailedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrInvalidRequest, "invalid catalog package")
	}
	stage, err := packages.Acquire(ctx, m.root, entry)
	if err != nil {
		result := packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrAcquireFailed, err.Error())
		m.recordResult(entry.ID, true, result)
		return result
	}
	defer os.RemoveAll(stage)
	m.mu.Lock()
	store := m.store
	m.mu.Unlock()
	_, err = store.InstallStaged(stage, packages.InstallOptions{Replace: replace, Entry: &entry})
	if err != nil {
		result := packages.FailedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrAcquireFailed, err.Error())
		m.recordResult(entry.ID, true, result)
		return result
	}
	return m.Activate(ctx, packages.LifecycleRequest{PackageID: entry.ID, PackageVersion: entry.Version, Operation: sdk.LifecycleActivate})
}

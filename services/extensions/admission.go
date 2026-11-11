// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"slices"

	sdk "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"
)

// Ordinary admission answers one question: may ordinary traffic reach
// this package right now. A running backend is an internal lifecycle
// participant until activation completes durably; registration alone
// never admits it.
//
// Two decision functions share one side-effect-free snapshot core:
// ordinaryAdmissionOpen is the full affirmative gate for routing and
// listing (List, Lookup, ordinary HTTP/WebSocket routes, ControlState),
// while VerifyOrdinaryToken is the bearer-token gate (runtime/service
// admissions, browser request admissions). The token gate additionally
// binds the exact issuing instance and stays permissive only while no
// lifecycle fence or live backend contradicts the token; a live
// backend always requires the full affirmative state. Reads never run
// controller effects, downloads, process waits or callbacks, and the
// manager mutex is held only for brief snapshots, never across disk
// reads, callbacks or waits.

// admissionSnapshot captures everything an admission decision needs
// under one brief lock. No disk reads run here.
type admissionSnapshot struct {
	started          bool
	busy             bool
	draining         bool
	required         bool
	hasBackend       bool
	backendAlive     bool
	backendInstance  string
	backendInstall   string
	backendVersion   string
	controlled       bool
	catalogVersion   string
	controllerID     string
	controllerInst   string
	controllerReady  bool
	busySnapshot     string
	drainingSnapshot bool
	catalogKnown     bool
	trustedCatalog   bool
}

// snapshotAdmissionLocked fills an admissionSnapshot; callers hold m.mu.
func (m *Manager) snapshotAdmissionLocked(id string) admissionSnapshot {
	snapshot := admissionSnapshot{started: m.started, trustedCatalog: m.catalogPath != "" || len(m.catalog) != 0}
	if operation, busy := m.busy[id]; busy {
		snapshot.busy, snapshot.busySnapshot = true, operation
	}
	snapshot.draining, snapshot.drainingSnapshot = m.draining[id], m.draining[id]
	snapshot.required = slices.Contains(m.requiredIDs, id)
	if item := m.running[id]; item != nil {
		snapshot.hasBackend = true
		if !item.client.Exited() {
			snapshot.backendAlive = true
			snapshot.backendInstance = item.descriptor.InstanceID
			snapshot.backendInstall = item.installation
			snapshot.backendVersion = item.descriptor.Manifest.Version
		}
	}
	for _, entry := range m.catalog {
		if entry.ID != id {
			continue
		}
		snapshot.catalogKnown = true
		if entry.Required {
			snapshot.required = true
		}
		if entry.LifecycleController != "" {
			snapshot.controlled = true
			snapshot.catalogVersion = entry.Version
			snapshot.controllerID = entry.LifecycleController
			if state, ok := m.controllers[entry.LifecycleController]; ok && state.ready {
				if backend := m.running[entry.LifecycleController]; backend != nil && !backend.client.Exited() {
					snapshot.controllerReady = true
					snapshot.controllerInst = state.instanceID
				}
			}
		} else if !snapshot.controlled {
			snapshot.catalogVersion = entry.Version
		}
	}
	return snapshot
}

// admissionRecheck confirms the snapshot is still current: the same
// backend instance with unchanged busy/drain state. Anything else fails
// closed. Callers hold m.mu.
func (m *Manager) admissionRecheck(id string, snapshot admissionSnapshot) bool {
	if operation, busy := m.busy[id]; busy != snapshot.busy || (busy && operation != snapshot.busySnapshot) {
		return false
	}
	if m.draining[id] != snapshot.drainingSnapshot {
		return false
	}
	item := m.running[id]
	if !snapshot.hasBackend {
		return item == nil
	}
	if item == nil || item.descriptor.InstanceID != snapshot.backendInstance {
		return false
	}
	if snapshot.controlled && snapshot.controllerID != "" {
		state, ok := m.controllers[snapshot.controllerID]
		if !ok || state.instanceID != snapshot.controllerInst {
			return false
		}
	}
	return true
}

// ordinaryAdmissionOpen reports whether ordinary traffic may reach id
// right now. Optional packages require current desired true, no
// pending/busy/drain/maintenance fence, a matching live backend and
// catalog identity, and, for controlled packages, the current
// controller instance with confirmed running effects and admission
// readiness. Required packages keep their existing availability: a
// live backend with no teardown fence.
func (m *Manager) ordinaryAdmissionOpen(id string) bool {
	if !sdk.ValidID(id) {
		return false
	}
	m.mu.Lock()
	snapshot := m.snapshotAdmissionLocked(id)
	m.mu.Unlock()
	if !snapshot.started {
		return false
	}
	intent, intentErr := packages.LoadIntent(m.root, id)
	if !m.attributionAllows(id, snapshot, intent, intentErr) {
		return false
	}
	fenced := maintenanceHeld(m.maintenanceDir)
	var effect controlledEffect
	var effectOK bool
	if snapshot.controlled {
		m.obsMu.Lock()
		effect, effectOK = m.lastObs[id]
		m.obsMu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.admissionRecheck(id, snapshot) {
		return false
	}
	return m.admissionDecide(snapshot, intent, intentErr, fenced, effect, effectOK)
}

// VerifyOrdinaryToken reports whether one bearer admission for the
// exact package/instance may be used right now. Any lifecycle fence
// (drain, busy, maintenance, pending or undesired intent) rejects it.
// A live backend additionally requires the full affirmative admission
// state with the exact issuing instance; without a live backend or
// any lifecycle state the registry-level checks govern, which keeps
// bearer verification usable where no lifecycle decision exists.
func (m *Manager) VerifyOrdinaryToken(packageID, instanceID string) bool {
	if !sdk.ValidID(packageID) || instanceID == "" {
		return false
	}
	m.mu.Lock()
	snapshot := m.snapshotAdmissionLocked(packageID)
	m.mu.Unlock()
	if !snapshot.started {
		return snapshotAllowsUntracked(snapshot)
	}
	intent, intentErr := packages.LoadIntent(m.root, packageID)
	if !m.attributionAllows(packageID, snapshot, intent, intentErr) {
		return false
	}
	fenced := maintenanceHeld(m.maintenanceDir)
	var effect controlledEffect
	var effectOK bool
	if snapshot.controlled && snapshot.backendAlive {
		m.obsMu.Lock()
		effect, effectOK = m.lastObs[packageID]
		m.obsMu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.admissionRecheck(packageID, snapshot) {
		return false
	}
	if snapshot.backendAlive {
		if snapshot.backendInstance != instanceID {
			return false
		}
		return m.admissionDecide(snapshot, intent, intentErr, fenced, effect, effectOK)
	}
	if snapshot.busy || snapshot.draining || fenced {
		return false
	}
	if intentErr == nil && (intent.PendingOperation != nil || !intent.DesiredEnabled) {
		return false
	}
	return true
}

// snapshotAllowsUntracked answers the token gate for a manager that
// never started lifecycle tracking: only registry checks govern.
func snapshotAllowsUntracked(snapshot admissionSnapshot) bool {
	return !snapshot.busy && !snapshot.draining
}

func (m *Manager) attributionAllows(id string, snapshot admissionSnapshot, intent packages.Intent, intentErr error) bool {
	if !snapshot.trustedCatalog || snapshot.catalogKnown {
		return true
	}
	if intentErr != nil {
		return false
	}
	if intent.Generation == "0" {
		return true
	}
	binding, known, err := packages.LoadIntentController(m.root, id)
	return err == nil && known && binding.ControllerID == ""
}

// admissionDecide evaluates the full affirmative state from a
// rechecked snapshot and its loaded intent.
func (m *Manager) admissionDecide(snapshot admissionSnapshot, intent packages.Intent, intentErr error, fenced bool, effect controlledEffect, effectOK bool) bool {
	if snapshot.required {
		return snapshot.backendAlive && !snapshot.busy && !snapshot.draining
	}
	if intentErr != nil || !intent.DesiredEnabled || intent.PendingOperation != nil {
		return false
	}
	if snapshot.busy || snapshot.draining || fenced {
		return false
	}
	if !snapshot.backendAlive || snapshot.backendInstall == "" || snapshot.backendInstall != intent.InstallationID {
		return false
	}
	if snapshot.catalogVersion != "" && snapshot.backendVersion != snapshot.catalogVersion {
		return false
	}
	if !snapshot.controlled {
		return true
	}
	if !snapshot.controllerReady || snapshot.controllerInst == "" {
		return false
	}
	return effectOK && effect.generation == intent.Generation &&
		effect.installation == intent.InstallationID &&
		effect.instanceID == snapshot.controllerInst &&
		effect.state == sdk.LifecycleStateRunning && effect.admission
}

// openOrdinaryAdmission releases the ordinary-admission fence after
// durable activation completed. Runtime and service tokens are never
// revoked by the fence: they fail closed through the admission check
// while fenced and work again once it opens, so a retained instance
// keeps its usable token across retry.
func (m *Manager) openOrdinaryAdmission(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.draining, id)
}

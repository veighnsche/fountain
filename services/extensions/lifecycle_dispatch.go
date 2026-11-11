// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	sdk "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"
)

// F03 operation budgets: the accepted enable operation runs at most 10
// minutes including the five-minute package download, accepted disable
// at most 150 seconds. Every controller call also uses the lesser
// remaining whole-operation budget: observe 10, deactivate 120, activate
// 300 seconds. Exhausted time fails and retains pending rather than
// extending the deadline. No silent retries.
const (
	enableBudgetSeconds   = 600
	disableBudgetSeconds  = 150
	observeCallSeconds    = 10
	deactivateCallSeconds = 120
	activateCallSeconds   = 300
	maxLifecycleBody      = 16 << 10
)

// AuthError is one authorize-callback failure with its HTTP mapping: 400
// malformed, 403 unauthorized/expired/revoked/mismatched, 503
// unavailable state. This path performs no requested lifecycle effect.
type AuthError struct {
	Code    string
	Message string
	HTTP    int
}

func (e *AuthError) Error() string { return e.Code + ": " + e.Message }

// catalogSnapshot returns the loaded catalog entries and their exact SHA.
func (m *Manager) catalogSnapshot() ([]sdk.CatalogPackage, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sdk.CatalogPackage(nil), m.catalog...), m.catalogSHA
}

// controllerEndpoint snapshots one required controller's private
// transport and current instance. A crashed backend reads unready; it is
// reaped by the exit observer rather than here.
func (m *Manager) controllerEndpoint(controllerID string) (http.RoundTripper, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.controllers[controllerID]
	if !ok || !state.ready {
		return nil, "", false
	}
	item := m.running[controllerID]
	if item == nil || item.client.Exited() {
		return nil, "", false
	}
	return item.transport, state.instanceID, true
}

// acquireMutation claims the single global package mutation slot. There
// is no queue: a second operation while the slot is held gets
// operation_in_progress.
func (m *Manager) acquireMutation(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mutation != "" {
		return errors.New("another extension operation is active")
	}
	m.mutation = id
	m.mutationPhase = transitionCommand
	m.mutationTokens = nil
	return nil
}

// releaseMutation frees the slot and revokes every admission its command
// issued. Cleanup-exception tokens die here too: they never survive
// command completion.
func (m *Manager) releaseMutation() {
	m.mu.Lock()
	tokens := m.mutationTokens
	m.mutation = ""
	m.mutationPhase = transitionIdle
	m.mutationTokens = nil
	m.mu.Unlock()
	for _, token := range tokens {
		m.RevokeLifecycleAdmission(token)
	}
}

// issueCallAdmission mints one opaque token for a full tuple and tracks
// it for command-end revocation.
func (m *Manager) issueCallAdmission(call sdk.LifecycleCall, desired bool, purpose lifecycleAdmissionPurpose) (string, error) {
	m.mu.Lock()
	instance := ""
	for _, state := range m.controllers {
		if state.instanceID == call.ControllerInstanceID && state.ready {
			instance = state.instanceID
		}
	}
	m.mu.Unlock()
	if instance == "" {
		return "", errControllerNotReady
	}
	token, err := m.IssueLifecycleAdmission(&BackgroundAdmission{
		Lifecycle:        true,
		InstallationID:   call.InstallationID,
		InstanceID:       call.ControllerInstanceID,
		Call:             call,
		DesiredEnabled:   desired,
		lifecyclePurpose: purpose,
	})
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	if m.mutation != "" {
		m.mutationTokens = append(m.mutationTokens, token)
	}
	m.mu.Unlock()
	return token, nil
}

// buildCall pins one operation to its full identity tuple with a deadline
// of now plus the lesser of the operation cap and the remaining whole
// operation budget.
func buildCall(controllerID, instanceID string, entry sdk.CatalogPackage, installationID, generation string, operation lifecycleCommand, budgetLeft int64) sdk.LifecycleCall {
	capSeconds := int64(observeCallSeconds)
	switch operation {
	case sdk.LifecycleActivate:
		capSeconds = activateCallSeconds
	case sdk.LifecycleDeactivate:
		capSeconds = deactivateCallSeconds
	}
	if capSeconds > budgetLeft {
		capSeconds = budgetLeft
	}
	if capSeconds < 1 {
		capSeconds = 1
	}
	return sdk.LifecycleCall{
		ControllerID:         controllerID,
		ControllerInstanceID: instanceID,
		PackageID:            entry.ID,
		PackageVersion:       entry.Version,
		PackageSHA256:        entry.SHA256,
		InstallationID:       installationID,
		Generation:           generation,
		Operation:            string(operation),
		DeadlineUnix:         time.Now().Unix() + capSeconds,
	}
}

// dispatchLifecycleCall issues one admitted call to the required
// controller over its existing per-instance private transport and
// validates the observation. A mismatched echo, malformed body, invalid
// state/error combination, timeout, non-200, or oversized response is
// unknown. Activate success needs running with a current grant;
// deactivate success needs stopped without one. The token is revoked
// after its call either way.
func (m *Manager) dispatchLifecycleCall(ctx context.Context, controllerID string, call sdk.LifecycleCall, token string) (sdk.LifecycleObservation, error) {
	defer m.RevokeLifecycleAdmission(token)
	transport, _, ready := m.controllerEndpoint(controllerID)
	if !ready {
		return sdk.LifecycleObservation{}, errControllerNotReady
	}
	if err := sdk.ValidateLifecycleCall(call, time.Now().Unix()); err != nil {
		return sdk.LifecycleObservation{}, err
	}
	body, err := json.Marshal(call)
	if err != nil {
		return sdk.LifecycleObservation{}, err
	}
	wait := max(time.Until(time.Unix(call.DeadlineUnix, 0)), time.Second)
	callCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, "http://extension"+sdk.LifecyclePath, bytes.NewReader(body))
	if err != nil {
		return sdk.LifecycleObservation{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(sdk.LifecycleAdmissionHeader, token)
	response, err := transport.RoundTrip(request)
	if err != nil {
		m.journalEffectUnknown(call)
		return sdk.LifecycleObservation{}, errors.New("lifecycle controller is unreachable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxLifecycleBody+1))
	if err != nil || len(data) > maxLifecycleBody {
		m.journalEffectUnknown(call)
		return sdk.LifecycleObservation{}, errors.New("lifecycle observation is oversized")
	}
	if response.StatusCode != http.StatusOK {
		m.journalEffectUnknown(call)
		return sdk.LifecycleObservation{}, errors.New("lifecycle controller rejected the call")
	}
	observation, err := sdk.DecodeLifecycleObservation(data)
	if err != nil {
		m.journalEffectUnknown(call)
		return sdk.LifecycleObservation{}, errors.New("lifecycle observation is malformed")
	}
	if err := sdk.ValidateLifecycleObservation(observation, call, time.Now().Unix()); err != nil {
		m.journalEffectUnknown(call)
		return sdk.LifecycleObservation{}, errors.New("lifecycle observation is unconfirmed")
	}
	m.journalEffect(call, observation)
	return observation, nil
}

// journalEffect records the latest controller-confirmed external fact
// for the call's package. State reads consult this journal only and
// never call the controller themselves.
func (m *Manager) journalEffect(call sdk.LifecycleCall, observation sdk.LifecycleObservation) {
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	if m.lastObs == nil {
		m.lastObs = make(map[string]controlledEffect)
	}
	m.lastObs[call.PackageID] = controlledEffect{
		state:        observation.State,
		admission:    observation.AdmissionReady,
		generation:   call.Generation,
		installation: call.InstallationID,
		instanceID:   call.ControllerInstanceID,
	}
}

// journalEffectUnknown marks the last controller fact unknown after a
// transport or validation failure. Reads fail closed until a later call
// re-confirms the effects.
func (m *Manager) journalEffectUnknown(call sdk.LifecycleCall) {
	m.obsMu.Lock()
	defer m.obsMu.Unlock()
	if m.lastObs == nil {
		m.lastObs = make(map[string]controlledEffect)
	}
	m.lastObs[call.PackageID] = controlledEffect{
		state:        sdk.LifecycleStateUnknown,
		generation:   call.Generation,
		installation: call.InstallationID,
		instanceID:   call.ControllerInstanceID,
	}
}

// VerifyLifecycleCall implements the exact authorize-callback validation
// for one presented token and body: token variant, deadline, live
// controller instance, catalog target-controller relation, target UUID
// and generation, current selected artifact pin, pending/desired
// operation, and complete body equality. An activate token requires
// desired true with pending activate; a deactivate token normally
// requires desired false with pending deactivate, except the one bounded
// cleanup admission stored in the issued record by the still-active
// failed-enable command. Observe accepts either current desired value
// and echoes it. Per-instance callers must present their own instance.
func (m *Manager) VerifyLifecycleCall(token string, call sdk.LifecycleCall) (sdk.LifecycleVerification, *AuthError) {
	return m.verifyLifecycleCall(token, call, controllerVerification)
}

// VerifyServiceLifecycleCall is used only after authenticating the configured
// service peer. Cleanup verifies once at the controller and once at its service.
func (m *Manager) VerifyServiceLifecycleCall(token string, call sdk.LifecycleCall) (sdk.LifecycleVerification, *AuthError) {
	return m.verifyLifecycleCall(token, call, serviceVerification)
}

func (m *Manager) verifyLifecycleCall(token string, call sdk.LifecycleCall, hop lifecycleVerificationHop) (sdk.LifecycleVerification, *AuthError) {
	now := time.Now().Unix()
	admission, ok := m.VerifyLifecycleAdmission(token)
	if !ok {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "lifecycle admission is unknown or revoked", HTTP: 403}
	}
	if admission.Call.DeadlineUnix <= now {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "lifecycle admission expired", HTTP: 403}
	}
	if call != admission.Call {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "lifecycle call mismatches its admission", HTTP: 403}
	}
	if err := sdk.ValidateLifecycleCall(call, now); err != nil {
		return sdk.LifecycleVerification{}, &AuthError{Code: "invalid_request", Message: "lifecycle call is malformed", HTTP: 400}
	}
	m.mu.Lock()
	entries := append([]sdk.CatalogPackage(nil), m.catalog...)
	state, controllerKnown := m.controllers[call.ControllerID]
	phase := m.mutationPhase
	mutation := m.mutation
	m.mu.Unlock()
	if !controllerKnown || !state.ready || state.instanceID != call.ControllerInstanceID {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "controller instance mismatches", HTTP: 403}
	}
	var entry *sdk.CatalogPackage
	for i := range entries {
		if entries[i].ID == call.PackageID {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "target has no catalog entry", HTTP: 403}
	}
	if entry.LifecycleController != call.ControllerID {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "target is not controlled by this controller", HTTP: 403}
	}
	if entry.Version != call.PackageVersion || entry.SHA256 != call.PackageSHA256 {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "artifact pin mismatches catalog", HTTP: 403}
	}
	intent, err := packages.LoadIntent(m.root, call.PackageID)
	if err != nil {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unavailable", Message: "requested state is unavailable", HTTP: 503}
	}
	if intent.InstallationID != call.InstallationID || intent.Generation != call.Generation {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "installation or generation mismatches", HTTP: 403}
	}
	pending := ""
	if intent.PendingOperation != nil {
		pending = *intent.PendingOperation
	}
	if admission.lifecyclePurpose == purposeFailedEnableCleanup && call.Operation != sdk.LifecycleDeactivate {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "cleanup admission cannot authorize " + call.Operation, HTTP: 403}
	}
	switch call.Operation {
	case sdk.LifecycleObserve:
		return m.completeLifecycleVerification(token, admission, sdk.LifecycleVerification{Call: call, DesiredEnabled: intent.DesiredEnabled}, hop)
	case sdk.LifecycleActivate:
		if !intent.DesiredEnabled || pending != packages.IntentActivate || !admission.DesiredEnabled {
			return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "activate mismatches requested state", HTTP: 403}
		}
		return m.completeLifecycleVerification(token, admission, sdk.LifecycleVerification{Call: call, DesiredEnabled: true}, hop)
	case sdk.LifecycleDeactivate:
		if admission.lifecyclePurpose == purposeFailedEnableCleanup {
			if mutation != call.PackageID || phase != transitionFailedEnableCleanup {
				return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "cleanup admission is not in its command", HTTP: 403}
			}
			if !intent.DesiredEnabled || pending != packages.IntentActivate || !admission.DesiredEnabled {
				return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "cleanup mismatches failed-enable state", HTTP: 403}
			}
			return m.completeLifecycleVerification(token, admission, sdk.LifecycleVerification{Call: call, DesiredEnabled: true}, hop)
		}
		if intent.DesiredEnabled || pending != packages.IntentDeactivate || admission.DesiredEnabled {
			return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "deactivate mismatches requested state", HTTP: 403}
		}
		return m.completeLifecycleVerification(token, admission, sdk.LifecycleVerification{Call: call, DesiredEnabled: false}, hop)
	}
	return sdk.LifecycleVerification{}, &AuthError{Code: "invalid_request", Message: "unknown lifecycle operation", HTTP: 400}
}

// observeControlledOnce issues one admitted read-only observe call. It
// changes no desired state.
func (m *Manager) observeControlledOnce(ctx context.Context, controllerID string, entry sdk.CatalogPackage, intent packages.Intent, budgetLeft int64) (sdk.LifecycleObservation, error) {
	transport, instance, ready := m.controllerEndpoint(controllerID)
	_, _ = transport, instance
	if !ready {
		return sdk.LifecycleObservation{}, errControllerNotReady
	}
	call := buildCall(controllerID, instance, entry, intent.InstallationID, intent.Generation, lifecycleObserve, budgetLeft)
	token, err := m.issueCallAdmission(call, intent.DesiredEnabled, purposeLifecycle)
	if err != nil {
		return sdk.LifecycleObservation{}, err
	}
	return m.dispatchLifecycleCall(ctx, controllerID, call, token)
}

// activateControlledOnce issues one idempotent activate call for known
// stopped or known owned-running state: missing effects are created or
// existing owned effects adopted with a refreshed grant, never restarted
// or duplicated.
func (m *Manager) activateControlledOnce(ctx context.Context, controllerID string, entry sdk.CatalogPackage, intent packages.Intent, budgetLeft int64) (sdk.LifecycleObservation, error) {
	_, instance, ready := m.controllerEndpoint(controllerID)
	if !ready {
		return sdk.LifecycleObservation{}, errControllerNotReady
	}
	call := buildCall(controllerID, instance, entry, intent.InstallationID, intent.Generation, lifecycleActivate, budgetLeft)
	token, err := m.issueCallAdmission(call, intent.DesiredEnabled, purposeLifecycle)
	if err != nil {
		return sdk.LifecycleObservation{}, err
	}
	return m.dispatchLifecycleCall(ctx, controllerID, call, token)
}

// deactivateControlledOnce issues one idempotent deactivate call. It
// closes the operational grant first and stops only resources whose
// ownership the controller verifies; foreign or uncertain resources
// never become authorized by the call.
func (m *Manager) deactivateControlledOnce(ctx context.Context, controllerID string, entry sdk.CatalogPackage, intent packages.Intent, budgetLeft int64) (sdk.LifecycleObservation, error) {
	_, instance, ready := m.controllerEndpoint(controllerID)
	if !ready {
		return sdk.LifecycleObservation{}, errControllerNotReady
	}
	call := buildCall(controllerID, instance, entry, intent.InstallationID, intent.Generation, lifecycleDeactivate, budgetLeft)
	token, err := m.issueCallAdmission(call, intent.DesiredEnabled, purposeLifecycle)
	if err != nil {
		return sdk.LifecycleObservation{}, err
	}
	return m.dispatchLifecycleCall(ctx, controllerID, call, token)
}

// startControlledBackend starts or adopts the ordinary backend for a
// controlled target: a live backend is adopted after the version pin
// check, a crashed one is reaped and started fresh, and route collisions
// refuse before admission, exactly like the uncontrolled path.
func (m *Manager) startControlledBackend(id, pinnedVersion string) (sdk.Manifest, error) {
	m.mu.Lock()
	if item := m.running[id]; item != nil {
		if item.client.Exited() {
			m.stopOne(id, item, true)
		} else {
			version := item.descriptor.Manifest.Version
			manifest := item.descriptor.Manifest
			m.mu.Unlock()
			if pinnedVersion != "" && pinnedVersion != version {
				return sdk.Manifest{}, refuseActivation(true, errors.New("installed version does not match request"))
			}
			return manifest, nil
		}
	}
	m.mu.Unlock()
	packageDir := filepath.Join(m.root, id)
	manifest, err := sdk.LoadManifest(packageDir)
	if err != nil {
		return sdk.Manifest{}, refuseActivation(false, errors.New("package is not installed"))
	}
	if manifest.ID != id {
		return sdk.Manifest{}, refuseActivation(false, errors.New("installed directory does not match manifest id"))
	}
	if pinnedVersion != "" && manifest.Version != pinnedVersion {
		return sdk.Manifest{}, refuseActivation(true, errors.New("installed version does not match request"))
	}
	// Boot and reconcile paths reach backend start without an
	// adoption: resolve the manifest's declared controller against
	// the catalog IDs here too, before any process starts. A
	// dangling claim refuses with its precise text; adopted catalog
	// entries resolve and pass through unchanged.
	if err := m.checkControllerBinding(id, manifest.LifecycleController); err != nil {
		return sdk.Manifest{}, err
	}
	item, err := m.startOne(packageDir, id)
	if err != nil {
		return sdk.Manifest{}, errors.New("backend failed to start")
	}
	m.mu.Lock()
	if conflict := claimedRouteConflict(m.running, item.descriptor.Manifest); conflict != "" {
		m.stopOne(id, item, false)
		m.mu.Unlock()
		return sdk.Manifest{}, errors.New("native page route is already claimed")
	}
	m.running[id] = item
	m.noteControllerStarted(item.descriptor)
	// Registration publishes the backend to ordinary routing. Fence
	// admission now, before this function returns: only durable
	// activation completion opens it, and every failure after this
	// point retains the closed fence. The revocation callback runs
	// without holding the manager mutex.
	revoke, instanceID := m.instanceStopped, item.descriptor.InstanceID
	m.draining[id] = true
	m.mu.Unlock()
	if revoke != nil && instanceID != "" {
		revoke(instanceID)
	}
	go m.observeExit(id, item)
	return manifest, nil
}

// stopControlledBackend stops the ordinary backend and verifies its
// process cleanup. An already-absent backend verifies as stopped.
// Cancellation aborts the wait with the remainder unconfirmed, so a
// manager close never blocks on process cleanup.
func (m *Manager) stopControlledBackend(ctx context.Context, id string) error {
	m.mu.Lock()
	item := m.running[id]
	if item == nil {
		m.mu.Unlock()
		return nil
	}
	m.stopOne(id, item, false)
	client := item.client
	m.mu.Unlock()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if client.Exited() {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("backend stop cancelled; cleanup is unconfirmed")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if client.Exited() {
		return nil
	}
	return errors.New("backend process cleanup is unconfirmed")
}

// backendRunning reports the ordinary backend presence without effects.
func (m *Manager) backendRunning(id string) (running, unknown bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.running[id]
	if item == nil {
		return false, false
	}
	if item.client.Exited() {
		return false, true
	}
	return true, false
}

// findCatalogEntry returns the loaded catalog entry for id, if any.
func (m *Manager) findCatalogEntry(id string) (sdk.CatalogPackage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.catalog {
		if entry.ID == id {
			return entry, true
		}
	}
	return sdk.CatalogPackage{}, false
}

// danglingBindingError reports an installed manifest that declares a
// lifecycle controller without the trusted catalog relationship: the
// configured catalog omits the target, so no controller may act for
// it and no backend-only handling may substitute. Restoring the
// catalog entry and explicitly retrying makes the required controller
// path available again.
type danglingBindingError struct {
	id         string
	controller string
}

func (e *danglingBindingError) Error() string {
	return "package " + e.id + " declares lifecycle controller " + e.controller + " without a catalog entry; restore the catalog entry and retry"
}

// checkControllerBinding resolves one installed manifest's declared
// lifecycle controller (its owned-by label) against the trusted
// catalog IDs. It returns a danglingBindingError when a catalog is
// configured and omits the target: the claim never authorizes
// backend-only handling and the controller tuple is never
// reconstructed from manifest or receipt data. Without a configured
// catalog, or without a declared controller, or with a catalog entry
// present, there is nothing to refuse.
func (m *Manager) checkControllerBinding(id, controller string) error {
	if controller == "" {
		return nil
	}
	m.mu.Lock()
	configured := m.catalogPath != "" || len(m.catalog) > 0
	resolved := false
	for _, entry := range m.catalog {
		if entry.ID == id {
			resolved = true
			break
		}
	}
	m.mu.Unlock()
	if configured && !resolved {
		return &danglingBindingError{id: id, controller: controller}
	}
	return nil
}

// recordFailure stores advisory in-memory failure text. It is never
// another persisted authority.
func (m *Manager) recordFailure(id, code, message string) {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	m.lastErr[id] = &packages.LifecycleError{Code: code, Message: message}
}

// clearFailure drops the advisory failure after a definite outcome.
func (m *Manager) clearFailure(id string) {
	m.errMu.Lock()
	defer m.errMu.Unlock()
	delete(m.lastErr, id)
}

// RequestEnabled validates, reserves, and persists one enable or
// disable operation, then returns immediately: the accepted operation
// reports activating or deactivating, and the actual bounded work runs
// on the manager lifetime context, never the browser connection. There
// is no queue: a second operation while the slot is held gets
// operation_in_progress. Browser disconnect never cancels accepted
// work; Close cancels and joins the active task before the store
// unlocks. An explicit retry with the same desired boolean is allowed
// when no command is active, even with pending from an earlier attempt:
// it mandates the current generation, increments exactly once, and
// re-observes before deciding whether an idempotent effect is needed.
// Repeated delivery with an old generation is stale_state, never
// another effect.
func (m *Manager) RequestEnabled(ctx context.Context, id, expectedGeneration, catalogSHA string, enabled bool) packages.LifecycleResult {
	_ = ctx
	operation := sdk.LifecycleActivate
	state := packages.LifecycleActivating
	if !enabled {
		operation = sdk.LifecycleDeactivate
		state = packages.LifecycleDeactivating
	}
	if !m.acceptMu.TryLock() {
		return packages.FailedResult(operation, id, packages.LifecycleErrInProgress, "another extension operation is active")
	}
	defer m.acceptMu.Unlock()
	adopted, failure := m.adoptMutation(id, "", expectedGeneration, catalogSHA, commandForEnabled(enabled))
	if failure != nil {
		return *failure
	}
	taskCtx, cancel := context.WithTimeout(m.lifeCtx, time.Duration(adopted.budgetSeconds)*time.Second)
	op := &activeOperation{id: adopted.id, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	if !m.started || m.activeOp != nil {
		m.mu.Unlock()
		cancel()
		m.finishAdopted(adopted, nil)
		if !m.isStarted() {
			return packages.FailedResult(operation, id, packages.LifecycleErrUnavailable, "extension manager is not running")
		}
		return packages.FailedResult(operation, id, packages.LifecycleErrInProgress, "another extension operation is active")
	}
	m.activeOp = op
	m.mu.Unlock()
	go m.runAdoptedMutation(taskCtx, adopted, op)
	return packages.LifecycleResult{Operation: operation, PackageID: id, State: state, Progress: "extension operation accepted; progress in status"}
}

// runAdoptedMutation executes one accepted mutation on the manager
// lifetime context. A manager stop before execution runs no effects;
// the persisted interruption marker keeps the outcome truthful until
// an explicit retry.
func (m *Manager) runAdoptedMutation(ctx context.Context, adopted *adoptedMutation, op *activeOperation) {
	defer m.finishAdopted(adopted, op)
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if !started || ctx.Err() != nil {
		m.recordFailure(adopted.id, packages.LifecycleErrUnavailable, "extension manager stopped before the operation ran")
		return
	}
	_ = m.executeMutation(ctx, adopted)
}

// isStarted reports the running flag under one brief lock.
func (m *Manager) isStarted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started
}

// adoptedMutation carries a validated, reserved, and durably persisted
// operation from adoption to execution. The interruption marker is on
// disk before any external effect runs.
type adoptedMutation struct {
	id            string
	operation     lifecycleCommand
	pending       string
	budgetSeconds int64
	entry         sdk.CatalogPackage
	found         bool
	intent        packages.Intent
	pinnedVersion string
	opDeadline    int64
	releaseGuard  func()
}

// adoptMutation validates one operation, reserves the maintenance guard
// and the single mutation slot, marks the package busy, and persists
// the next generation with the interruption marker before any external
// effect. Failure returns a terminal result with no effects and no
// held reservations.
func (m *Manager) adoptMutation(id, pinnedVersion, expectedGeneration, catalogSHA string, operation lifecycleCommand) (*adoptedMutation, *packages.LifecycleResult) {
	enabled := operation == lifecycleActivate
	pending := packages.IntentActivate
	budget := int64(enableBudgetSeconds)
	if !enabled {
		pending = packages.IntentDeactivate
		budget = int64(disableBudgetSeconds)
	}
	failed := func(code, message string) *packages.LifecycleResult {
		result := packages.FailedResult(string(operation), id, code, message)
		return &result
	}
	if operation != lifecycleActivate && operation != lifecycleDeactivate {
		return nil, failed(packages.LifecycleErrInvalidRequest, "invalid lifecycle command")
	}
	if !sdk.ValidID(id) {
		return nil, failed(packages.LifecycleErrInvalidRequest, "invalid extension id")
	}
	if _, err := packages.ParseGeneration(expectedGeneration); err != nil {
		return nil, failed(packages.LifecycleErrInvalidRequest, err.Error())
	}
	m.mu.Lock()
	started := m.started
	sha := m.catalogSHA
	m.mu.Unlock()
	if !started {
		return nil, failed(packages.LifecycleErrUnavailable, "extension manager is not running")
	}
	if catalogSHA != sha {
		return nil, failed(packages.LifecycleErrStaleState, "catalog changed; reload and retry")
	}
	entry, found := m.findCatalogEntry(id)
	if found && entry.Required {
		return nil, failed(packages.LifecycleErrRequiredPackage, "required extension cannot change state here")
	}
	if pinnedVersion != "" && found && pinnedVersion != entry.Version {
		return nil, failed(packages.LifecycleErrVersionMismatch, "catalog version does not match request")
	}
	if !found {
		manifest, err := sdk.LoadManifest(filepath.Join(m.root, id))
		if err != nil || manifest.ID != id {
			return nil, failed(packages.LifecycleErrNotInstalled, "package is not installed")
		}
		if pinnedVersion != "" && manifest.Version != pinnedVersion {
			return nil, failed(packages.LifecycleErrVersionMismatch, "installed version does not match request")
		}
		// A manifest declaring a lifecycle controller without the
		// trusted catalog relationship always fails closed here,
		// before the maintenance guard, the mutation slot, the
		// installation identity, or any persisted intent. The claim
		// alone never authorizes backend-only handling for enable
		// or disable, on either the managed or the direct path.
		if err := m.checkControllerBinding(id, manifest.LifecycleController); err != nil {
			return nil, failed(packages.LifecycleErrCatalogBinding, err.Error())
		}
	}
	releaseGuard, err := acquireMaintenanceGuard(m.maintenanceDir)
	if err != nil {
		return nil, failed(packages.LifecycleErrMaintenance, err.Error())
	}
	if err := m.acquireMutation(id); err != nil {
		releaseGuard()
		return nil, failed(packages.LifecycleErrInProgress, "another extension operation is active")
	}
	m.mu.Lock()
	m.busy[id] = string(operation)
	m.mu.Unlock()
	unwind := func() {
		m.mu.Lock()
		delete(m.busy, id)
		m.mu.Unlock()
		m.releaseMutation()
		releaseGuard()
	}
	installation, err := packages.EnsureInstallation(m.root, id)
	if err != nil {
		unwind()
		return nil, failed(packages.LifecycleErrUnavailable, "installation identity is unavailable")
	}
	if found {
		if err := packages.BindIntentController(m.root, id, installation, entry.LifecycleController); err != nil {
			unwind()
			return nil, failed(packages.LifecycleErrCatalogBinding, "controller attribution conflicts with trusted catalog")
		}
	} else {
		current, err := packages.LoadIntent(m.root, id)
		m.mu.Lock()
		snapshot := m.snapshotAdmissionLocked(id)
		m.mu.Unlock()
		if !m.attributionAllows(id, snapshot, current, err) {
			unwind()
			return nil, failed(packages.LifecycleErrCatalogBinding, "controller attribution is unavailable; restore the catalog entry and retry")
		}
		if snapshot.trustedCatalog && current.Generation == "0" {
			if err := packages.BindIntentController(m.root, id, installation, ""); err != nil {
				unwind()
				return nil, failed(packages.LifecycleErrCatalogBinding, "controller attribution conflicts with trusted catalog")
			}
		}
	}
	intent, err := packages.StoreIntent(m.root, id, installation, enabled, &pending, expectedGeneration)
	if err != nil {
		unwind()
		var intentErr *packages.IntentError
		if errors.As(err, &intentErr) && intentErr.Kind == packages.IntentStale {
			return nil, failed(packages.LifecycleErrStaleState, "intent changed; reload and retry with the current generation")
		}
		return nil, failed(packages.LifecycleErrUnavailable, "requested state is unavailable")
	}
	m.RevokeLifecycleForInstallation(installation)
	return &adoptedMutation{
		id: id, operation: operation, pending: pending, budgetSeconds: budget,
		entry: entry, found: found, intent: intent, pinnedVersion: pinnedVersion,
		opDeadline: time.Now().Unix() + budget, releaseGuard: releaseGuard,
	}, nil
}

// finishAdopted ends one adopted mutation: it clears the busy mark,
// frees the single slot with revocation of the command's admissions,
// releases the maintenance guard, and closes the task's done channel
// when it owns one. No effects run here.
func (m *Manager) finishAdopted(adopted *adoptedMutation, op *activeOperation) {
	m.mu.Lock()
	delete(m.busy, adopted.id)
	tokens := m.mutationTokens
	m.mutation = ""
	m.mutationPhase = transitionIdle
	m.mutationTokens = nil
	if op != nil && m.activeOp == op {
		m.activeOp = nil
	}
	m.mu.Unlock()
	for _, token := range tokens {
		m.RevokeLifecycleAdmission(token)
	}
	adopted.releaseGuard()
	if op != nil {
		close(op.done)
	}
}

// executeMutation runs the bounded effect work for one adopted mutation
// on the given context. Completion and interruption settle through the
// durable intent; the caller owns reservation release.
func (m *Manager) executeMutation(ctx context.Context, adopted *adoptedMutation) (result packages.LifecycleResult) {
	defer func() {
		if result.Error != nil {
			m.recordFailure(adopted.id, result.Error.Code, result.Error.Message)
		}
	}()
	if adopted.found && adopted.entry.LifecycleController != "" {
		if adopted.operation == sdk.LifecycleActivate {
			return m.controlledEnable(ctx, adopted.entry, adopted.intent, adopted.pinnedVersion, adopted.opDeadline, true)
		}
		return m.controlledDisable(ctx, adopted.entry, adopted.intent, adopted.opDeadline)
	}
	if adopted.operation == sdk.LifecycleActivate {
		if adopted.found {
			if _, failure := m.ensurePackageBytes(ctx, adopted.entry, adopted.intent, adopted.opDeadline, true); failure != nil {
				return *failure
			}
		}
		return m.backendEnable(ctx, adopted.id, adopted.pinnedVersion, adopted.intent, adopted.opDeadline)
	}
	return m.backendDisable(ctx, adopted.id, adopted.intent, adopted.opDeadline)
}

func (m *Manager) requestEnabled(ctx context.Context, id, pinnedVersion, expectedGeneration, catalogSHA string, enabled bool) packages.LifecycleResult {
	adopted, failure := m.adoptMutation(id, pinnedVersion, expectedGeneration, catalogSHA, commandForEnabled(enabled))
	if failure != nil {
		return *failure
	}
	defer m.finishAdopted(adopted, nil)
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(adopted.budgetSeconds)*time.Second)
	defer cancel()
	return m.executeMutation(work, adopted)
}

// abortPending clears the interruption marker after a definite
// pre-effect refusal, retaining the desired flag and generation. A crash
// or unknown external outcome keeps the marker set.
//
//nolint:unparam // Keep the common operation helper and callback signature.
func (m *Manager) abortPending(id string, desired bool, generation string) {
	_, _ = packages.StorePendingSameGeneration(m.root, id, desired, nil, generation)
}

// ensurePackageBytes reuses validated installed bytes, otherwise
// downloads and publishes the catalog-selected artifact when
// allowAcquire is set. Reconcile and boot paths pass false: installed
// bytes must already match, and they fail without any acquisition.
// No controller activate call precedes validation. Download failure is
// definite with no external effect: pending clears while desired and
// generation are retained. Old bytes at a different revision are
// replaced only after the controller observes stopped effects with the
// ordinary backend absent.
func (m *Manager) ensurePackageBytes(ctx context.Context, entry sdk.CatalogPackage, intent packages.Intent, opDeadline int64, allowAcquire bool) (sdk.Manifest, *packages.LifecycleResult) {
	packageDir := filepath.Join(m.root, entry.ID)
	_, targetErr := os.Lstat(packageDir)
	if errors.Is(targetErr, os.ErrNotExist) {
		targetErr = packages.RecoverPackage(m.root, entry.ID)
		if targetErr == nil {
			_, targetErr = os.Lstat(packageDir)
		}
	}
	if targetErr == nil {
		targetErr = packages.ValidateKnownPackageOrigin(m.root, entry.ID)
	}
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		result := packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrUnavailable, "installed package origin is unavailable; explicit repair is required")
		m.recordFailure(entry.ID, result.Error.Code, result.Error.Message)
		return sdk.Manifest{}, &result
	}
	if err := packages.ValidateInstalled(m.root, entry); err == nil {
		manifest, err := sdk.LoadManifest(packageDir)
		if err != nil {
			result := packages.FailedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrNotInstalled, "installed manifest is unavailable")
			return sdk.Manifest{}, &result
		}
		return manifest, nil
	}
	if !allowAcquire {
		// Definite pre-effect refusal, like a download failure: no
		// call was made, so the marker clears while desired and
		// generation are retained for an explicit enable.
		m.abortPending(entry.ID, true, intent.Generation)
		if _, err := os.Lstat(packageDir); errors.Is(err, os.ErrNotExist) {
			result := packages.FailedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrNotInstalled, "installed package does not match catalog; explicit enable is required")
			return sdk.Manifest{}, &result
		}
		result := packages.FailedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrVersionMismatch, "installed package does not match catalog; explicit enable is required")
		return sdk.Manifest{}, &result
	}
	if _, err := os.Lstat(packageDir); err == nil {
		running, unknown := m.backendRunning(entry.ID)
		if running || unknown {
			result := packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrUnconfirmed, "running backend blocks replacement")
			m.recordFailure(entry.ID, result.Error.Code, result.Error.Message)
			return sdk.Manifest{}, &result
		}
		if entry.LifecycleController != "" {
			observation, err := m.observeControlledOnce(ctx, entry.LifecycleController, entry, intent, opDeadline-time.Now().Unix())
			if err != nil || observation.State != sdk.LifecycleStateStopped {
				result := packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrUnconfirmed, "residual effects block replacement")
				m.recordFailure(entry.ID, result.Error.Code, result.Error.Message)
				return sdk.Manifest{}, &result
			}
		}
	}
	if m.store == nil {
		result := packages.FailedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrUnavailable, "package store is unavailable")
		return sdk.Manifest{}, &result
	}
	stage, err := packages.Acquire(ctx, m.root, entry)
	if err != nil {
		m.abortPending(entry.ID, true, intent.Generation)
		result := packages.FailedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrAcquireFailed, "package acquisition failed")
		return sdk.Manifest{}, &result
	}
	defer os.RemoveAll(stage)
	_, exists := os.Lstat(packageDir)
	manifest, err := m.store.InstallStaged(stage, packages.InstallOptions{Replace: exists == nil, Entry: &entry})
	if err != nil {
		m.abortPending(entry.ID, true, intent.Generation)
		result := packages.FailedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrAcquireFailed, "package installation failed")
		return sdk.Manifest{}, &result
	}
	return manifest, nil
}

// revalidateForEffect rechecks controller readiness and that the intent
// generation is still the operation's own before any external effect.
func (m *Manager) revalidateForEffect(entry sdk.CatalogPackage, intent packages.Intent) error {
	_, _, ready := m.controllerEndpoint(entry.LifecycleController)
	if !ready {
		return errControllerNotReady
	}
	current, err := packages.LoadIntent(m.root, entry.ID)
	if err != nil {
		return errors.New("requested state is unavailable")
	}
	if current.Generation != intent.Generation {
		return errors.New("intent changed during the operation")
	}
	return nil
}

// activateSequenceResult is one shared first-enable effect run: either
// a completed activation or a failure that keeps the interruption
// marker. backendFailed reports a post-activation backend or
// re-observation failure; the caller decides between bounded cleanup
// and retaining pending. blocked reports failure solely from
// controller or socket unavailability, which alone merits a scheduled
// retry; every other failure is definite for its attempt.
type activateSequenceResult struct {
	manifest      sdk.Manifest
	completed     packages.LifecycleResult
	failure       packages.LifecycleResult
	backendFailed bool
	blocked       bool
	ok            bool
}

// transportBlocked reports explicit pre-effect controller or socket
// unavailability: only this failure class merits a scheduled retry.
var errControllerNotReady = errors.New("lifecycle controller is not ready")

func transportBlocked(err error) bool {
	return errors.Is(err, errControllerNotReady)
}

// runActivateSequence runs the shared first-enable effect ordering for
// a controlled target: validated bytes before any activate call;
// observe, then idempotent activate for known stopped or owned-running
// state; backend start with collision check; re-observation; pending
// clears durably only after both checks succeed. Unknown or mismatched
// observations stop with pending retained and are never replayed. It
// performs no cleanup itself; post-activation backend failures return
// backendFailed for the caller to handle.
func (m *Manager) runActivateSequence(ctx context.Context, entry sdk.CatalogPackage, intent packages.Intent, pinnedVersion string, opDeadline int64, allowAcquire bool) activateSequenceResult {
	unconfirmed := func(code, message string) activateSequenceResult {
		m.recordFailure(entry.ID, code, message)
		return activateSequenceResult{failure: packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, code, message)}
	}
	blocked := func(code, message string) activateSequenceResult {
		result := unconfirmed(code, message)
		result.blocked = true
		return result
	}
	manifest, failure := m.ensurePackageBytes(ctx, entry, intent, opDeadline, allowAcquire)
	if failure != nil {
		return activateSequenceResult{failure: *failure}
	}
	if err := m.revalidateForEffect(entry, intent); err != nil {
		if _, _, ready := m.controllerEndpoint(entry.LifecycleController); !ready {
			return blocked(packages.LifecycleErrUnavailable, err.Error())
		}
		return unconfirmed(packages.LifecycleErrUnconfirmed, err.Error())
	}
	observation, err := m.observeControlledOnce(ctx, entry.LifecycleController, entry, intent, opDeadline-time.Now().Unix())
	if err != nil {
		if transportBlocked(err) {
			return blocked(packages.LifecycleErrUnavailable, "external effects are unreachable")
		}
		return unconfirmed(packages.LifecycleErrUnavailable, "external effects are unreachable")
	}
	if observation.State == sdk.LifecycleStateUnknown || observation.Error != nil {
		return unconfirmed(packages.LifecycleErrUnconfirmed, "external effects are unknown; explicit retry is required")
	}
	activated, err := m.activateControlledOnce(ctx, entry.LifecycleController, entry, intent, opDeadline-time.Now().Unix())
	if err != nil {
		if transportBlocked(err) {
			return blocked(packages.LifecycleErrUnavailable, "activation call failed")
		}
		return unconfirmed(packages.LifecycleErrUnavailable, "activation call failed")
	}
	if activated.State != sdk.LifecycleStateRunning || !activated.AdmissionReady || activated.Error != nil {
		return unconfirmed(packages.LifecycleErrUnconfirmed, "activation is unconfirmed")
	}
	if _, err := m.startControlledBackend(entry.ID, pinnedVersion); err != nil {
		return activateSequenceResult{backendFailed: true}
	}
	confirmed, err := m.observeControlledOnce(ctx, entry.LifecycleController, entry, intent, opDeadline-time.Now().Unix())
	if err != nil {
		return activateSequenceResult{backendFailed: true}
	}
	if confirmed.State != sdk.LifecycleStateRunning || !confirmed.AdmissionReady || confirmed.Error != nil {
		return activateSequenceResult{backendFailed: true}
	}
	if _, err := packages.StorePendingSameGeneration(m.root, entry.ID, true, nil, intent.Generation); err != nil {
		return unconfirmed(packages.LifecycleErrUnavailable, "completion cannot persist")
	}
	m.clearFailure(entry.ID)
	m.openOrdinaryAdmission(entry.ID)
	return activateSequenceResult{manifest: manifest, completed: packages.LifecycleResult{Operation: sdk.LifecycleActivate, PackageID: entry.ID, ObservedVersion: manifest.Version, State: packages.LifecycleEnabled, Progress: "backend started and effects confirmed"}, ok: true}
}

// controlledEnable runs first-enable ordering for a controlled target
// through the shared sequence. If backend startup fails after external
// activation it keeps admission closed and attempts one bounded
// deactivate cleanup; anything earlier stops with pending retained.
func (m *Manager) controlledEnable(ctx context.Context, entry sdk.CatalogPackage, intent packages.Intent, pinnedVersion string, opDeadline int64, allowAcquire bool) packages.LifecycleResult {
	result := m.runActivateSequence(ctx, entry, intent, pinnedVersion, opDeadline, allowAcquire)
	if result.ok {
		return result.completed
	}
	if result.backendFailed {
		return m.cleanupFailedEnable(ctx, entry, intent, opDeadline)
	}
	return result.failure
}

// cleanupFailedEnable keeps ordinary admission closed and attempts one
// bounded deactivate through the required controller under the internal
// failed-enable admission exception. Desired and generation are
// unchanged; pending activate is retained even when cleanup confirms
// stopped, so a later explicit retry authorizes another activation.
func (m *Manager) cleanupFailedEnable(ctx context.Context, entry sdk.CatalogPackage, intent packages.Intent, opDeadline int64) packages.LifecycleResult {
	m.mu.Lock()
	m.mutationPhase = transitionFailedEnableCleanup
	m.mu.Unlock()
	remaining := opDeadline - time.Now().Unix()
	if remaining < 1 {
		m.recordFailure(entry.ID, packages.LifecycleErrUnconfirmed, "cleanup budget is exhausted")
		return packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrActivateFailed, "backend failed; cleanup budget is exhausted")
	}
	_, instance, ready := m.controllerEndpoint(entry.LifecycleController)
	if !ready {
		m.recordFailure(entry.ID, packages.LifecycleErrUnconfirmed, "controller is not ready for cleanup")
		return packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrActivateFailed, "backend failed; cleanup is unconfirmed")
	}
	call := buildCall(entry.LifecycleController, instance, entry, intent.InstallationID, intent.Generation, lifecycleDeactivate, remaining)
	token, err := m.issueCallAdmission(call, true, purposeFailedEnableCleanup)
	if err != nil {
		m.recordFailure(entry.ID, packages.LifecycleErrUnconfirmed, "cleanup admission is unavailable")
		return packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrActivateFailed, "backend failed; cleanup is unconfirmed")
	}
	observation, err := m.dispatchLifecycleCall(ctx, entry.LifecycleController, call, token)
	if err != nil || observation.State != sdk.LifecycleStateStopped {
		m.recordFailure(entry.ID, packages.LifecycleErrUnconfirmed, "failed activation left unknown effects; explicit retry is required")
		return packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrActivateFailed, "backend failed; cleanup is unconfirmed")
	}
	m.recordFailure(entry.ID, packages.LifecycleErrActivateFailed, "failed activation cleaned up; explicit retry is required")
	return packages.UnconfirmedResult(sdk.LifecycleActivate, entry.ID, packages.LifecycleErrActivateFailed, "backend failed; failed activation cleaned up; explicit retry is required")
}

// controlledDisable persists first, then closes admission irreversibly,
// observes even when the backend is absent, issues one idempotent
// deactivate even when observation is unknown, verifies stopped without
// a grant, stops the backend, and only then clears pending. Package
// bytes and private configuration are retained; unknown remainder stays
// pending with admission closed.
func (m *Manager) controlledDisable(ctx context.Context, entry sdk.CatalogPackage, intent packages.Intent, opDeadline int64) packages.LifecycleResult {
	unconfirmed := func(code, message string) packages.LifecycleResult {
		m.recordFailure(entry.ID, code, message)
		return packages.UnconfirmedResult(sdk.LifecycleDeactivate, entry.ID, code, message)
	}
	m.mu.Lock()
	m.draining[entry.ID] = true
	m.mu.Unlock()
	observation, err := m.observeControlledOnce(ctx, entry.LifecycleController, entry, intent, opDeadline-time.Now().Unix())
	if err != nil {
		observation = sdk.LifecycleObservation{State: sdk.LifecycleStateUnknown}
	}
	_ = observation
	stopped, err := m.deactivateControlledOnce(ctx, entry.LifecycleController, entry, intent, opDeadline-time.Now().Unix())
	if err != nil {
		return unconfirmed(packages.LifecycleErrUnavailable, "deactivation call failed; effects stay pending")
	}
	if stopped.State != sdk.LifecycleStateStopped || stopped.AdmissionReady || stopped.Error != nil {
		return unconfirmed(packages.LifecycleErrUnconfirmed, "teardown is unconfirmed; effects stay pending")
	}
	if err := m.stopControlledBackend(ctx, entry.ID); err != nil {
		return unconfirmed(packages.LifecycleErrUnconfirmed, "backend stop is unconfirmed; admission stays closed")
	}
	if _, err := packages.StorePendingSameGeneration(m.root, entry.ID, false, nil, intent.Generation); err != nil {
		return unconfirmed(packages.LifecycleErrUnavailable, "completion cannot persist")
	}
	m.clearFailure(entry.ID)
	m.mu.Lock()
	delete(m.draining, entry.ID)
	m.mu.Unlock()
	manifest, _ := sdk.LoadManifest(filepath.Join(m.root, entry.ID))
	return packages.LifecycleResult{Operation: sdk.LifecycleDeactivate, PackageID: entry.ID, ObservedVersion: manifest.Version, State: packages.LifecycleDisabled, Progress: "teardown verified and backend stopped"}
}

// backendEnable runs the existing backend start path for packages
// without an external lifecycle controller.
//
//nolint:unparam // Keep the common operation helper and callback signature.
func (m *Manager) backendEnable(ctx context.Context, id, pinnedVersion string, intent packages.Intent, opDeadline int64) packages.LifecycleResult {
	_ = opDeadline
	// Registration below fences ordinary admission; only durable
	// completion opens it. A start failure registers nothing, so the
	// retained pending marker alone keeps admission closed.
	manifest, err := m.startControlledBackend(id, pinnedVersion)
	if err != nil {
		if notInstalled, versionMismatch := isActivationRefusal(err); notInstalled || versionMismatch {
			m.abortPending(id, true, intent.Generation)
			code := packages.LifecycleErrNotInstalled
			if versionMismatch {
				code = packages.LifecycleErrVersionMismatch
			}
			return packages.FailedResult(sdk.LifecycleActivate, id, code, err.Error())
		}
		m.recordFailure(id, packages.LifecycleErrActivateFailed, err.Error())
		return packages.UnconfirmedResult(sdk.LifecycleActivate, id, packages.LifecycleErrActivateFailed, err.Error())
	}
	if _, err := packages.StorePendingSameGeneration(m.root, id, true, nil, intent.Generation); err != nil {
		m.recordFailure(id, packages.LifecycleErrUnavailable, "completion cannot persist")
		return packages.UnconfirmedResult(sdk.LifecycleActivate, id, packages.LifecycleErrUnavailable, "completion cannot persist")
	}
	m.clearFailure(id)
	m.openOrdinaryAdmission(id)
	return packages.LifecycleResult{Operation: sdk.LifecycleActivate, PackageID: id, ObservedVersion: manifest.Version, State: packages.LifecycleEnabled, Progress: "backend started and observed"}
}

// backendDisable runs the existing backend stop path for packages
// without an external lifecycle controller: admission closes first,
// then the backend stops, then pending clears.
func (m *Manager) backendDisable(ctx context.Context, id string, intent packages.Intent, opDeadline int64) packages.LifecycleResult {
	_ = ctx
	_ = opDeadline
	m.mu.Lock()
	item := m.running[id]
	if item == nil {
		m.mu.Unlock()
		if _, err := packages.StorePendingSameGeneration(m.root, id, false, nil, intent.Generation); err != nil {
			m.recordFailure(id, packages.LifecycleErrUnavailable, "completion cannot persist")
			return packages.UnconfirmedResult(sdk.LifecycleDeactivate, id, packages.LifecycleErrUnavailable, "completion cannot persist")
		}
		m.clearFailure(id)
		return packages.LifecycleResult{Operation: sdk.LifecycleDeactivate, PackageID: id, State: packages.LifecycleDisabled, Progress: "already stopped"}
	}
	if item.client.Exited() {
		m.stopOne(id, item, true)
		m.mu.Unlock()
		if _, err := packages.StorePendingSameGeneration(m.root, id, false, nil, intent.Generation); err != nil {
			m.recordFailure(id, packages.LifecycleErrUnavailable, "completion cannot persist")
			return packages.UnconfirmedResult(sdk.LifecycleDeactivate, id, packages.LifecycleErrUnavailable, "completion cannot persist")
		}
		m.clearFailure(id)
		return packages.LifecycleResult{Operation: sdk.LifecycleDeactivate, PackageID: id, State: packages.LifecycleDisabled, Progress: "backend had already exited"}
	}
	descriptor := cloneDescriptor(item.descriptor)
	m.draining[id] = true
	m.mu.Unlock()
	m.stopOneAndForget(id, item)
	if _, err := packages.StorePendingSameGeneration(m.root, id, false, nil, intent.Generation); err != nil {
		m.recordFailure(id, packages.LifecycleErrUnavailable, "completion cannot persist")
		return packages.UnconfirmedResult(sdk.LifecycleDeactivate, id, packages.LifecycleErrUnavailable, "completion cannot persist")
	}
	m.clearFailure(id)
	m.mu.Lock()
	delete(m.draining, id)
	m.mu.Unlock()
	return packages.LifecycleResult{Operation: sdk.LifecycleDeactivate, PackageID: id, ObservedVersion: descriptor.Manifest.Version, State: packages.LifecycleDisabled, Progress: "backend stopped"}
}

// stopOneAndForget stops a backend under lock discipline: snapshot the
// item, release the mutex, then stop.
func (m *Manager) stopOneAndForget(id string, item *running) {
	m.mu.Lock()
	current, ok := m.running[id]
	if !ok || current != item {
		m.mu.Unlock()
		return
	}
	m.stopOne(id, item, false)
	m.mu.Unlock()
}

// Frozen startup retry schedule: absolute offsets in seconds from the
// pass start, with a total startup-reconcile deadline. Each attempt is
// capped separately; elapsed offsets are never waited for and the
// overall deadline is never extended.
var startupReconcileOffsets = []int64{0, 1, 3, 7, 15, 30, 60}

const (
	startupReconcileTotalSeconds = 90
	startupAttemptSeconds        = 10
)

// opDeadlineFromContext bounds one effect call by the lesser of the
// per-call cap and the context's remaining budget.
func opDeadlineFromContext(ctx context.Context, capSeconds int64) int64 {
	budgetLeft := capSeconds
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := int64(time.Until(deadline) / time.Second); remaining < budgetLeft {
			budgetLeft = remaining
		}
	}
	if budgetLeft < 1 {
		budgetLeft = 1
	}
	return time.Now().Unix() + budgetLeft
}

// runStartupOptionalPass reconciles optional effects on the frozen
// bounded schedule after Start has returned core readiness. It retries
// only explicit pre-effect controller or socket unavailability; any
// other outcome resolves its row for this boot. A configured
// maintenance marker suppresses the whole pass without consuming the
// budget: the post-exclusion reconcile resumes deferred targets. After
// the final failure core stays ready and the optional row stays
// failed and unavailable. There is no persistent poll loop.
func (m *Manager) runStartupOptionalPass(lifeCtx context.Context, done chan struct{}) {
	defer close(done)
	start := time.Now()
	resolved := make(map[string]bool)
	ctx, cancel := context.WithTimeout(lifeCtx, time.Duration(startupReconcileTotalSeconds)*time.Second)
	defer cancel()
	for _, offset := range startupReconcileOffsets {
		if wait := time.Until(start.Add(time.Duration(offset) * time.Second)); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, time.Duration(startupAttemptSeconds)*time.Second)
		terminal := m.reconcileStableAttempt(attemptCtx, resolved)
		attemptCancel()
		if ctx.Err() != nil {
			return
		}
		if terminal {
			return
		}
	}
}

// startupEntries includes installed standalone packages as well as catalog
// targets. Only the trusted catalog can assign a lifecycle controller.
func (m *Manager) startupEntries() []sdk.CatalogPackage {
	entries, _ := m.controlCatalog()
	if m.catalogPath != "" {
		return entries
	}
	known := make(map[string]bool, len(entries))
	for _, entry := range entries {
		known[entry.ID] = true
	}
	dirs, _ := os.ReadDir(m.root)
	for _, dir := range dirs {
		id := dir.Name()
		if dir.IsDir() && sdk.ValidID(id) && !known[id] {
			entries = append(entries, sdk.CatalogPackage{ID: id})
		}
	}
	return entries
}

// reconcileStableAttempt runs one startup attempt under the single
// mutation slot: it is skipped entirely while another operation owns
// the slot, so accepted admin work never queues behind reconciliation.
func (m *Manager) reconcileStableAttempt(ctx context.Context, resolved map[string]bool) bool {
	// A never-activated catalog has no boot work and must not briefly
	// reserve the mutation slot against its first admin request.
	entries := m.startupEntries()
	hasIntent := false
	for _, entry := range entries {
		if entry.Required {
			continue
		}
		intent, err := packages.LoadIntent(m.root, entry.ID)
		if err != nil || intent.Generation != "0" {
			hasIntent = true
			break
		}
	}
	if !hasIntent {
		return true
	}
	release, err := acquireMaintenanceGuard(m.maintenanceDir)
	if err != nil {
		return true // Explicit post-maintenance reconciliation owns resumption.
	}
	defer release()
	if err := m.acquireMutation("startup"); err != nil {
		return false
	}
	defer m.releaseMutation()
	return m.reconcileStablePass(ctx, true, resolved)
}

// reconcileStablePass runs ordinary boot reconciliation over stable
// optional intent: never-activated rows are skipped entirely with no
// download and no host call; already-complete rows need no work;
// stable enabled rows observe and refresh their grant with backend
// admission; stable disabled rows stop definite running effects under
// the same generation. It acquires no packages, increments no
// generations, and never replays an interrupted pending effect: with
// settlePending (startup only) a pending row is observed once and
// finalized only when its desired outcome is already established with
// zero new effects, otherwise it stays pending for explicit retry. It
// returns true when every row reached a definite outcome and only
// unavailability-blocked rows merit another attempt. The caller holds
// the single mutation slot.
func (m *Manager) reconcileStablePass(ctx context.Context, settlePending bool, resolved map[string]bool) bool {
	terminal := true
	if maintenanceHeld(m.maintenanceDir) {
		return true
	}
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if !started {
		return true
	}
	entries := m.startupEntries()
	for _, entry := range entries {
		if entry.Required || resolved[entry.ID] {
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		if m.reconcileStableRow(ctx, entry, settlePending) {
			terminal = false
		} else if resolved != nil {
			resolved[entry.ID] = true
		}
	}
	return terminal
}

// reconcileStableRow reconciles one optional row. It returns true when
// the row is blocked solely on controller or socket unavailability.
func (m *Manager) reconcileStableRow(ctx context.Context, entry sdk.CatalogPackage, settlePending bool) bool {
	id := entry.ID
	if m.recoveryIsolated(id) {
		// An unrepairable optional package never executes its
		// bytes on reconcile paths; the row keeps its startup
		// failure for an explicit retry after repair.
		return false
	}
	intent, err := packages.LoadIntent(m.root, id)
	if err != nil {
		// Malformed requested state is never default-enabled and
		// never repaired here; rows render it failed.
		return false
	}
	if intent.Generation == "0" {
		// Never-activated packages are skipped entirely: no download,
		// no backend start, no host call.
		return false
	}
	installation, err := packages.LoadInstallation(m.root, id)
	if err != nil || installation != intent.InstallationID {
		return false
	}
	if intent.PendingOperation != nil {
		if !settlePending {
			return false
		}
		if entry.LifecycleController == "" {
			// Interrupted operations are observed, never replayed. Only
			// an already established outcome may clear pending, without
			// manufacturing a new administrator decision/generation.
			// Durable completion also releases the admission fence.
			alive, unknown := m.backendRunning(id)
			if !unknown && alive == intent.DesiredEnabled {
				if _, err := packages.StorePendingSameGeneration(m.root, id, intent.DesiredEnabled, nil, intent.Generation); err == nil {
					m.openOrdinaryAdmission(id)
				}
			}
			return false
		}
		return m.settlePendingRow(ctx, entry, intent)
	}
	if entry.LifecycleController == "" {
		return m.reconcileUncontrolledRow(ctx, entry, intent)
	}
	if intent.DesiredEnabled {
		return m.bootEnableRow(ctx, entry, intent)
	}
	return m.bootDisableRow(ctx, entry, intent)
}

// reconcileUncontrolledRow restores one stable optional backend
// without external effects: a desired backend that is absent starts,
// an undesired backend that is present stops. Complete rows need no
// work; failures stay visible for an explicit retry.
func (m *Manager) reconcileUncontrolledRow(ctx context.Context, entry sdk.CatalogPackage, intent packages.Intent) bool {
	id := entry.ID
	alive, _ := m.backendRunning(id)
	if intent.DesiredEnabled && !alive {
		if _, err := m.startControlledBackend(id, ""); err != nil {
			message := "backend failed; explicit retry is required"
			var dangling *danglingBindingError
			if errors.As(err, &dangling) {
				message = err.Error()
			}
			m.recordFailure(id, packages.LifecycleErrActivateFailed, message)
		} else {
			// Registration fenced admission; a stable desired
			// backend with no pending marker is established, so
			// durable completion opens it at once.
			m.openOrdinaryAdmission(id)
			m.clearFailure(id)
		}
		return false
	}
	if !intent.DesiredEnabled && alive {
		// Never stop a dangling backend-only row into a
		// successful disable: the declared controller was never
		// consulted, so live external effects would survive
		// behind a disabled report. Stay failed and visible for
		// an explicit retry after the catalog is restored.
		if manifest, err := sdk.LoadManifest(filepath.Join(m.root, id)); err == nil && manifest.ID == id {
			if bindingErr := m.checkControllerBinding(id, manifest.LifecycleController); bindingErr != nil {
				m.recordFailure(id, packages.LifecycleErrCatalogBinding, bindingErr.Error())
				return false
			}
		}
		result := m.backendDisable(ctx, id, intent, opDeadlineFromContext(ctx, deactivateCallSeconds))
		if result.State != packages.LifecycleDisabled || result.Unconfirmed {
			m.recordFailure(id, packages.LifecycleErrTeardownFailed, "backend stop is unconfirmed; explicit retry is required")
		}
		return false
	}
	return false
}

// rowComplete reports whether a stable row already matches its desired
// outcome in current local observations: no effect call is needed.
func (m *Manager) rowComplete(id string, intent packages.Intent) bool {
	observed := m.Observe(id)
	return observed.State == packages.LifecycleEnabled && !observed.Unconfirmed && intent.DesiredEnabled
}

// bootEnableRow performs ordinary boot reconciliation for one stable
// enabled row: installed bytes must already match the current catalog
// (never acquired here); persist pending before the actual activate
// call and clear after observed completion; the generation is never
// rewritten.
func (m *Manager) bootEnableRow(ctx context.Context, entry sdk.CatalogPackage, intent packages.Intent) bool {
	id := entry.ID
	if err := packages.ValidateInstalled(m.root, entry); err != nil {
		return false
	}
	if m.rowComplete(id, intent) {
		return false
	}
	if _, _, ready := m.controllerEndpoint(entry.LifecycleController); !ready {
		return true
	}
	activate := packages.IntentActivate
	if _, err := packages.StorePendingSameGeneration(m.root, id, true, &activate, intent.Generation); err != nil {
		return false
	}
	m.RevokeLifecycleForInstallation(intent.InstallationID)
	result := m.runActivateSequence(ctx, entry, intent, "", opDeadlineFromContext(ctx, activateCallSeconds), false)
	if result.ok {
		return false
	}
	if result.backendFailed {
		// No bounded cleanup on reconcile paths: admission stays
		// closed with pending retained, and an explicit retry owns
		// the verified teardown.
		m.recordFailure(id, packages.LifecycleErrActivateFailed, "backend failed; explicit retry is required")
		return false
	}
	if result.blocked {
		// Only definite pre-effect unavailability is retried. Restore
		// stable intent under the same generation; transport failures
		// after dispatch remain uncertain and retain pending instead.
		_, err := packages.StorePendingSameGeneration(m.root, id, true, nil, intent.Generation)
		return err == nil
	}
	return false
}

// bootDisableRow performs ordinary boot reconciliation for one stable
// disabled row. A cold manager journals nothing, so the absent journal
// is never proof of stopped effects: the row obtains one bounded
// authenticated observation through its required controller instead.
// A confirmed stopped observation with the ordinary backend absent
// needs no work and no intent rewrite. A running observation, or a
// live backend that still needs termination, persists pending
// deactivate at the same generation and runs the existing
// controlled-disable sequence. Unknown or mismatched effects stay
// unconfirmed for explicit retry; they never authorize an automatic
// effect and never read as disabled. Controller or socket
// unavailability alone stays blocked for the bounded startup retry
// schedule.
func (m *Manager) bootDisableRow(ctx context.Context, entry sdk.CatalogPackage, intent packages.Intent) bool {
	id := entry.ID
	if _, _, ready := m.controllerEndpoint(entry.LifecycleController); !ready {
		return true
	}
	alive, _ := m.backendRunning(id)
	observation, err := m.observeControlledOnce(ctx, entry.LifecycleController, entry, intent, opDeadlineFromContext(ctx, observeCallSeconds)-time.Now().Unix())
	if err != nil {
		// Transport and validation failures already journaled the
		// row unknown; only definite pre-effect unavailability
		// merits another startup attempt.
		return transportBlocked(err)
	}
	if observation.State == sdk.LifecycleStateStopped && observation.Error == nil {
		if !alive {
			m.clearFailure(id)
			return false
		}
		// Confirmed stopped effects with a still-running ordinary
		// backend: fall through to the generic teardown below,
		// which stops the backend after the verified deactivate.
		// No new running authority is invented here.
	} else if observation.State != sdk.LifecycleStateRunning || observation.Error != nil {
		// Unknown or mismatched effects: no automatic effect, no
		// intent rewrite. The journaled unknown keeps status
		// unconfirmed with admission closed.
		return false
	}
	deactivate := packages.IntentDeactivate
	if _, err := packages.StorePendingSameGeneration(m.root, id, false, &deactivate, intent.Generation); err != nil {
		return false
	}
	m.RevokeLifecycleForInstallation(intent.InstallationID)
	result := m.controlledDisable(ctx, entry, intent, opDeadlineFromContext(ctx, deactivateCallSeconds))
	if result.State == packages.LifecycleDisabled && !result.Unconfirmed {
		return false
	}
	if result.Error != nil && result.Error.Code == packages.LifecycleErrUnavailable {
		if _, _, ready := m.controllerEndpoint(entry.LifecycleController); !ready {
			return true
		}
	}
	return false
}

// settlePendingRow observes one interrupted controlled operation once
// and finalizes it only when the desired outcome is already
// established with zero new effects: an enabled backend with a current
// running grant, or an absent backend with confirmed stopped effects.
// Anything else stays pending for explicit retry; uncertain external
// effects are never replayed.
func (m *Manager) settlePendingRow(ctx context.Context, entry sdk.CatalogPackage, intent packages.Intent) bool {
	id := entry.ID
	if _, _, ready := m.controllerEndpoint(entry.LifecycleController); !ready {
		return true
	}
	observation, err := m.observeControlledOnce(ctx, entry.LifecycleController, entry, intent, opDeadlineFromContext(ctx, observeCallSeconds)-time.Now().Unix())
	if err != nil {
		return transportBlocked(err)
	}
	alive, _ := m.backendRunning(id)
	if intent.DesiredEnabled {
		if alive && observation.State == sdk.LifecycleStateRunning && observation.AdmissionReady {
			manifest, err := sdk.LoadManifest(filepath.Join(m.root, id))
			if err != nil || manifest.ID != id || manifest.Version != entry.Version {
				return false
			}
			if _, err := packages.StorePendingSameGeneration(m.root, id, true, nil, intent.Generation); err != nil {
				return false
			}
			// The desired outcome is established with zero new
			// effects and durably recorded: release the fence.
			m.openOrdinaryAdmission(id)
			m.clearFailure(id)
		}
		return false
	}
	if !alive && observation.State == sdk.LifecycleStateStopped && !observation.AdmissionReady {
		if _, err := packages.StorePendingSameGeneration(m.root, id, false, nil, intent.Generation); err != nil {
			return false
		}
		m.clearFailure(id)
	}
	return false
}

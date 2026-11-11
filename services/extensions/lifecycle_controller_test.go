// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	sdk "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"

	"github.com/stretchr/testify/require"
)

// holdTestLock takes LOCK_EX on a directory inode, prints READY, and
// holds until stdin closes: the maintenance-exclusion second process.
func holdTestLock(dir string) int {
	file, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_, _ = fmt.Fprintln(os.Stdout, "READY")
	_, _ = io.ReadAll(os.Stdin)
	return 0
}

type fixtureOwner struct {
	InstallationID   string `json:"installation_id"`
	EffectGeneration string `json:"effect_generation"`
}

type fixtureGrant struct {
	ControllerInstanceID string `json:"controller_instance_id"`
	PackageVersion       string `json:"package_version"`
	PackageSHA256        string `json:"package_sha256"`
	InstallationID       string `json:"installation_id"`
	Generation           string `json:"generation"`
}

func fixtureStateDir() string {
	dir := filepath.Join(os.Getenv("HOME"), "state")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

func readFixtureOwner(stateDir string) (fixtureOwner, bool) {
	data, err := os.ReadFile(filepath.Join(stateDir, "owner.json"))
	if err != nil {
		return fixtureOwner{}, false
	}
	var owner fixtureOwner
	if err := json.Unmarshal(data, &owner); err != nil || owner.InstallationID == "" {
		return fixtureOwner{}, false
	}
	if _, err := os.Lstat(filepath.Join(stateDir, "effect.up")); err != nil {
		return fixtureOwner{}, false
	}
	return owner, true
}

func runControllerHelper(knobs testFixtureKnobs) int {
	stateDir := fixtureStateDir()
	application := sdk.Application{
		HTTP: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "controller")
		}),
		Lifecycle: func(ctx context.Context, authority sdk.LifecycleAuthority, call sdk.LifecycleCall) (sdk.LifecycleObservation, error) {
			verification, err := authority.Verify(ctx, call)
			if err != nil {
				return sdk.LifecycleObservation{}, err
			}
			return controllerFixtureCall(ctx, stateDir, knobs, call, verification)
		},
	}
	if err := sdk.Serve(application); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func controllerFixtureCall(ctx context.Context, stateDir string, knobs testFixtureKnobs, call sdk.LifecycleCall, verification sdk.LifecycleVerification) (sdk.LifecycleObservation, error) {
	fail := func(err error) (sdk.LifecycleObservation, error) { return sdk.LifecycleObservation{}, err }
	switch call.Operation {
	case sdk.LifecycleObserve:
		return fixtureObserveCall(stateDir, call, knobs)
	case sdk.LifecycleActivate:
		if !verification.DesiredEnabled {
			return fail(errors.New("activation is not desired"))
		}
		if knobs.GateActivate {
			if err := os.WriteFile(filepath.Join(stateDir, "activate.entered"), nil, 0o600); err != nil {
				return fail(err)
			}
			for {
				if _, err := os.Stat(filepath.Join(stateDir, "activate.release")); err == nil {
					break
				} else if !os.IsNotExist(err) {
					return fail(err)
				}
				select {
				case <-ctx.Done():
					return fail(ctx.Err())
				case <-time.After(5 * time.Millisecond):
				}
			}
		}
		if knobs.SlowActivateMs > 0 {
			time.Sleep(time.Duration(knobs.SlowActivateMs) * time.Millisecond)
		}
		bumpActivateCount(stateDir)
		if knobs.FailActivate {
			return fail(errors.New("activation effect failed"))
		}
		return fixtureAdoptOrCreate(stateDir, call)
	case sdk.LifecycleDeactivate:
		return fixtureDeactivateCall(stateDir, call)
	default:
		return fail(errors.New("unknown lifecycle operation"))
	}
}

func bumpObserveCount(stateDir string) {
	path := filepath.Join(stateDir, "observe.count")
	count := 0
	if data, err := os.ReadFile(path); err == nil {
		_, _ = fmt.Sscanf(string(data), "%d", &count)
	}
	_ = os.WriteFile(path, fmt.Appendf(nil, "%d", count+1), 0o600)
}

func readObserveCount(stateDir string) int {
	data, err := os.ReadFile(filepath.Join(stateDir, "observe.count"))
	if err != nil {
		return 0
	}
	count := 0
	_, _ = fmt.Sscanf(string(data), "%d", &count)
	return count
}

func fixtureObserveCall(stateDir string, call sdk.LifecycleCall, knobs testFixtureKnobs) (sdk.LifecycleObservation, error) {
	bumpObserveCount(stateDir)
	// A slow final observation holds the activation open after backend
	// registration so tests can assert the closed fence mid-flight.
	// Marker files armed after registration steer exactly that call:
	// observe.unknown and observe.unavailable fail only the final
	// confirmation, never the pre-activation observation.
	if knobs.SlowObserveMs > 0 {
		time.Sleep(time.Duration(knobs.SlowObserveMs) * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "observe.unavailable")); err == nil {
		return sdk.LifecycleObservation{}, errors.New("external effects are unavailable")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "observe.unknown")); err == nil {
		return sdk.LifecycleObservation{LifecycleCall: call, State: sdk.LifecycleStateUnknown, Detail: "External effects are unknown."}, nil
	}
	owner, owned := readFixtureOwner(stateDir)
	if !owned {
		return sdk.LifecycleObservation{LifecycleCall: call, State: sdk.LifecycleStateStopped, Detail: "No owned runtime exists."}, nil
	}
	grantData, err := os.ReadFile(filepath.Join(stateDir, "grant.json"))
	ready := false
	if err == nil {
		var grant fixtureGrant
		if json.Unmarshal(grantData, &grant) == nil &&
			grant.ControllerInstanceID == call.ControllerInstanceID &&
			grant.PackageVersion == call.PackageVersion && grant.PackageSHA256 == call.PackageSHA256 &&
			grant.InstallationID == call.InstallationID && grant.Generation == call.Generation {
			ready = true
		}
	}
	generation := owner.EffectGeneration
	detail := "Owned runtime is running without a current grant."
	if ready {
		detail = "Owned runtime is running with a current operational grant."
	}
	return sdk.LifecycleObservation{LifecycleCall: call, State: sdk.LifecycleStateRunning, EffectGeneration: &generation, AdmissionReady: ready, Detail: detail}, nil
}

func fixtureAdoptOrCreate(stateDir string, call sdk.LifecycleCall) (sdk.LifecycleObservation, error) {
	fail := func(err error) (sdk.LifecycleObservation, error) { return sdk.LifecycleObservation{}, err }
	writeGrant := func() error {
		grant := fixtureGrant{ControllerInstanceID: call.ControllerInstanceID, PackageVersion: call.PackageVersion, PackageSHA256: call.PackageSHA256, InstallationID: call.InstallationID, Generation: call.Generation}
		data, err := json.Marshal(grant)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(stateDir, "grant.json"), data, 0o600)
	}
	if owner, owned := readFixtureOwner(stateDir); owned {
		if owner.InstallationID != call.InstallationID {
			return fail(errors.New("effect owner mismatches"))
		}
		if err := writeGrant(); err != nil {
			return fail(err)
		}
		generation := owner.EffectGeneration
		return sdk.LifecycleObservation{LifecycleCall: call, State: sdk.LifecycleStateRunning, EffectGeneration: &generation, AdmissionReady: true, Detail: "Existing owned runtime adopted without restart or reenrollment."}, nil
	}
	owner := fixtureOwner{InstallationID: call.InstallationID, EffectGeneration: call.Generation}
	data, err := json.Marshal(owner)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "owner.json"), data, 0o600); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "effect.up"), []byte(call.Generation), 0o600); err != nil {
		return fail(err)
	}
	if err := writeGrant(); err != nil {
		return fail(err)
	}
	generation := call.Generation
	return sdk.LifecycleObservation{LifecycleCall: call, State: sdk.LifecycleStateRunning, EffectGeneration: &generation, AdmissionReady: true, Detail: "Owned runtime started with a current operational grant."}, nil
}

func bumpDeactivateCount(stateDir string) {
	path := filepath.Join(stateDir, "deactivate.count")
	count := 0
	if data, err := os.ReadFile(path); err == nil {
		_, _ = fmt.Sscanf(string(data), "%d", &count)
	}
	_ = os.WriteFile(path, fmt.Appendf(nil, "%d", count+1), 0o600)
}

func readDeactivateCount(stateDir string) int {
	data, err := os.ReadFile(filepath.Join(stateDir, "deactivate.count"))
	if err != nil {
		return 0
	}
	count := 0
	_, _ = fmt.Sscanf(string(data), "%d", &count)
	return count
}

func fixtureDeactivateCall(stateDir string, call sdk.LifecycleCall) (sdk.LifecycleObservation, error) {
	bumpDeactivateCount(stateDir)
	_ = os.Remove(filepath.Join(stateDir, "grant.json"))
	owner, owned := readFixtureOwner(stateDir)
	if !owned {
		return sdk.LifecycleObservation{LifecycleCall: call, State: sdk.LifecycleStateStopped, Detail: "No owned runtime exists; operational admission is closed."}, nil
	}
	if owner.InstallationID != call.InstallationID {
		return sdk.LifecycleObservation{}, errors.New("foreign runtime is untouched")
	}
	_ = os.Remove(filepath.Join(stateDir, "effect.up"))
	_ = os.Remove(filepath.Join(stateDir, "owner.json"))
	return sdk.LifecycleObservation{LifecycleCall: call, State: sdk.LifecycleStateStopped, Detail: "Owned runtime stopped; operational admission is closed."}, nil
}

func bumpActivateCount(stateDir string) {
	path := filepath.Join(stateDir, "activate.count")
	count := 0
	if data, err := os.ReadFile(path); err == nil {
		_, _ = fmt.Sscanf(string(data), "%d", &count)
	}
	_ = os.WriteFile(path, fmt.Appendf(nil, "%d", count+1), 0o600)
}

func readActivateCount(stateDir string) int {
	data, err := os.ReadFile(filepath.Join(stateDir, "activate.count"))
	if err != nil {
		return 0
	}
	count := 0
	_, _ = fmt.Sscanf(string(data), "%d", &count)
	return count
}

// testAuthorizeMux serves the authorize callback for one controller
// instance in services tests. The production multiplexer lives in the
// routers package (importing it here would cycle); the mapping there is
// covered by its own test.
func testAuthorizeMux(m *Manager, instanceID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != sdk.LifecycleAuthorizePath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost || r.URL.RawQuery != "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		token := r.Header.Get(sdk.LifecycleAdmissionHeader)
		if token == "" {
			http.Error(w, "admission required", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, sdk.MaximumLifecycleBodyBytes+1))
		if err != nil || len(body) > sdk.MaximumLifecycleBodyBytes {
			http.Error(w, "body exceeds limit", http.StatusBadRequest)
			return
		}
		call, err := sdk.DecodeLifecycleCall(body)
		if err != nil {
			http.Error(w, "malformed call", http.StatusBadRequest)
			return
		}
		if call.ControllerInstanceID != instanceID {
			http.Error(w, "admission rejected", http.StatusForbidden)
			return
		}
		verification, authErr := m.VerifyLifecycleCall(token, call)
		if authErr != nil {
			http.Error(w, authErr.Message, authErr.HTTP)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(verification)
	})
}

func installLifecyclePackage(t *testing.T, root, id, manifest string, knobs testFixtureKnobs) {
	t.Helper()
	packageDir := filepath.Join(root, id)
	require.NoError(t, os.MkdirAll(packageDir, 0o700))
	executable, err := os.Executable()
	require.NoError(t, err)
	data, err := os.ReadFile(executable)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension"), data, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), []byte(manifest), 0o600))
	dataDir := filepath.Join(root, ".data", id)
	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	knobData, err := json.Marshal(knobs)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "fixture.json"), knobData, 0o600))
}

const (
	controllerManifest = `{"protocol":1,"id":"controller","name":"Test","version":"1.0.0","executable":"extension","capabilities":["native.extension.lifecycle"]}`
	sampleManifest     = `{"protocol":1,"id":"sample","name":"Test","version":"1.0.0","executable":"extension","capabilities":["native.actor.read"],"lifecycle_controller":"controller"}`
)

type controllerHarness struct {
	root            string
	m               *Manager
	sha             string
	controllerEntry sdk.CatalogPackage
	sampleEntry     sdk.CatalogPackage
}

func startControllerHarness(t *testing.T, maintenanceDir string, sampleKnobs, controllerKnobs testFixtureKnobs, preinstallSample bool) *controllerHarness {
	t.Helper()
	root := shortTestRoot(t)
	installLifecyclePackage(t, root, "controller", controllerManifest, controllerKnobs)
	installLifecyclePackage(t, root, "sample", sampleManifest, sampleKnobs)
	controllerEntry := testCatalogEntry("controller", "1.0.0", true, "")
	controllerEntry.Capabilities = []string{"native.extension.lifecycle"}
	sampleEntry := testCatalogEntry("sample", "1.0.0", false, "controller")
	sampleEntry.Capabilities = []string{"native.actor.read"}
	// Required bootstrap reuses only exact installed receipts: the
	// fixture completes the required install the way production
	// installs do. The optional receipt ships only when the scenario
	// needs preinstalled bytes.
	require.NoError(t, packages.WriteReceipt(filepath.Join(root, "controller"), controllerEntry))
	if preinstallSample {
		require.NoError(t, packages.WriteReceipt(filepath.Join(root, "sample"), sampleEntry))
	}
	m := NewManager(root, "controller")
	m.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
	configureTestCatalog(t, m, root, controllerEntry, sampleEntry)
	if maintenanceDir != "" {
		require.NoError(t, m.SetMaintenanceDir(maintenanceDir))
	}
	require.NoError(t, m.SetCallbackHandlerFactory(func(instanceID string) http.Handler {
		return testAuthorizeMux(m, instanceID)
	}))
	require.NoError(t, m.Start(context.Background()))
	t.Cleanup(func() { _ = m.Close() })
	return &controllerHarness{root: root, m: m, sha: m.catalogSHA, controllerEntry: controllerEntry, sampleEntry: sampleEntry}
}

func controllerStateDir(h *controllerHarness) string {
	return filepath.Join(h.root, ".data", "controller", "state")
}

func enabledSample(t *testing.T, h *controllerHarness) packages.Intent {
	t.Helper()
	requestEnabledAccept(t, h.m, "sample", "0", h.sha, true)
	waitActiveOpDone(t, h.m)
	observed := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleEnabled, observed.State)
	require.False(t, observed.Unconfirmed)
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "1", intent.Generation)
	require.True(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	return intent
}

// The required controller registers only when its manifest capability
// and handler registration agree; a declared capability without a
// handler fails the handshake instead of becoming a silent no-op.
func TestControllerRegistrationMismatch(t *testing.T) {
	root := shortTestRoot(t)
	installLifecyclePackage(t, root, "controller", controllerManifest, testFixtureKnobs{})
	controllerEntry := testCatalogEntry("controller", "1.0.0", true, "")
	controllerEntry.Capabilities = []string{"native.extension.lifecycle"}
	// The receipt matches so bootstrap reaches the handshake it must
	// reject; the failure stays a registration refusal, not a fetch.
	require.NoError(t, packages.WriteReceipt(filepath.Join(root, "controller"), controllerEntry))
	m := NewManager(root, "controller")
	m.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
	configureTestCatalog(t, m, root, controllerEntry)
	require.NoError(t, m.SetCallbackHandlerFactory(func(instanceID string) http.Handler {
		return testAuthorizeMux(m, instanceID)
	}))
	err := m.Start(context.Background())
	// The handshake rejects the mismatched registration instead of
	// running a silent no-op controller; the phase wrapper carries the
	// refusal.
	require.ErrorContains(t, err, "runtime registration failed")
}

// Full enable/disable over real private sockets with live fixture
// processes: the manager persists intent before effects, dispatches
// through the required controller with distinct admissions validated on
// the authorize callback, and clears pending durably only after both
// backend and effects confirm. Generations advance once per accepted
// mutation and never on completion.
func TestControlledEnableDisableLiveWire(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
	enabled := enabledSample(t, h)
	_ = enabled

	_, _, ok := h.m.Lookup("sample")
	require.True(t, ok, "enabled package must open ordinary admission")
	observed := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleEnabled, observed.State)
	require.False(t, observed.Unconfirmed)
	require.Equal(t, 1, readActivateCount(controllerStateDir(h)))

	requestEnabledAccept(t, h.m, "sample", "1", h.sha, false)
	waitActiveOpDone(t, h.m)
	disabled := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleDisabled, disabled.State)
	require.False(t, disabled.Unconfirmed)
	_, _, ok = h.m.Lookup("sample")
	require.False(t, ok, "disable must close ordinary admission")
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation)
	require.False(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	_, err = os.Lstat(filepath.Join(controllerStateDir(h), "effect.up"))
	require.True(t, os.IsNotExist(err), "disable must stop owned effects")
	require.NoError(t, packages.ValidateInstalled(h.root, h.sampleEntry), "disable retains package bytes")

	stale := h.m.RequestEnabled(context.Background(), "sample", "1", h.sha, false)
	require.Equal(t, packages.LifecycleFailed, stale.State)
	require.Equal(t, packages.LifecycleErrStaleState, stale.Error.Code)
}

// Two concurrent activations serialize on the single mutation slot:
// exactly one reaches the controller, the other fails without an
// effect, and the survivor completes.
func TestConcurrentEnableSerializes(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller", SlowActivateMs: 2000}, true)
	barrier := make(chan struct{})
	results := make(chan packages.LifecycleResult, 2)
	for range 2 {
		go func() {
			<-barrier
			results <- h.m.RequestEnabled(context.Background(), "sample", "0", h.sha, true)
		}()
	}
	close(barrier)
	first := <-results
	second := <-results
	var winner, loser packages.LifecycleResult
	accepted := func(result packages.LifecycleResult) bool {
		return result.Error == nil && result.State == packages.LifecycleActivating
	}
	if accepted(first) {
		winner, loser = first, second
	} else {
		winner, loser = second, first
	}
	lifecycleResult(t, winner)
	require.True(t, accepted(winner), "exactly one enable is accepted, got %+v", winner)
	require.Equal(t, packages.LifecycleFailed, loser.State)
	require.Contains(t, []string{packages.LifecycleErrInProgress, packages.LifecycleErrStaleState}, loser.Error.Code)
	waitActiveOpDone(t, h.m)
	observed := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleEnabled, observed.State)
	require.False(t, observed.Unconfirmed)
	require.Equal(t, 1, readActivateCount(controllerStateDir(h)))
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "1", intent.Generation)
	require.True(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
}

// A backend that fails after external activation keeps admission
// closed and runs one bounded deactivate under the failed-enable
// exception. Desired and generation are unchanged and pending activate
// is retained, so an explicit retry authorizes another activation. The
// exception grants only that deactivate inside its command: it can
// never authorize activate and dies with the command.
func TestFailedEnableCleanupConfinement(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{Role: "plain-exit"}, testFixtureKnobs{Role: "controller"}, true)
	requestEnabledAccept(t, h.m, "sample", "0", h.sha, true)
	waitActiveOpDone(t, h.m)
	observed := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleFailed, observed.State)
	require.True(t, observed.Unconfirmed)
	require.NotNil(t, observed.Error)
	require.Equal(t, packages.LifecycleErrActivateFailed, observed.Error.Code)
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "1", intent.Generation)
	require.True(t, intent.DesiredEnabled)
	require.NotNil(t, intent.PendingOperation)
	require.Equal(t, packages.IntentActivate, *intent.PendingOperation)
	_, _, ok := h.m.Lookup("sample")
	require.False(t, ok, "failed enable must keep admission closed")
	_, err = os.Lstat(filepath.Join(controllerStateDir(h), "effect.up"))
	require.True(t, os.IsNotExist(err), "failed-enable cleanup must stop effects")

	installation := intent.InstallationID
	m := h.m
	m.mu.Lock()
	instance := m.controllers["controller"].instanceID
	m.mu.Unlock()
	activateCall := buildCall("controller", instance, h.sampleEntry, installation, "1", sdk.LifecycleActivate, 60)
	exceptionActivate, err := m.IssueLifecycleAdmission(&BackgroundAdmission{Lifecycle: true, InstallationID: installation, InstanceID: instance, Call: activateCall, DesiredEnabled: true, CleanupException: true})
	require.NoError(t, err)
	defer m.RevokeLifecycleAdmission(exceptionActivate)
	_, authErr := m.VerifyLifecycleCall(exceptionActivate, activateCall)
	require.Error(t, authErr, "cleanup admission must never authorize activate")
	require.Equal(t, 403, authErr.HTTP)

	deactivateCall := buildCall("controller", instance, h.sampleEntry, installation, "1", sdk.LifecycleDeactivate, 60)
	exceptionDeactivate, err := m.IssueLifecycleAdmission(&BackgroundAdmission{Lifecycle: true, InstallationID: installation, InstanceID: instance, Call: deactivateCall, DesiredEnabled: true, CleanupException: true})
	require.NoError(t, err)
	defer m.RevokeLifecycleAdmission(exceptionDeactivate)
	_, authErr = m.VerifyLifecycleCall(exceptionDeactivate, deactivateCall)
	require.Error(t, authErr, "cleanup admission must not work outside its command")
	m.mu.Lock()
	m.mutation = "sample"
	m.mutationPhase = "cleanup"
	m.mu.Unlock()
	verification, authErr := m.VerifyLifecycleCall(exceptionDeactivate, deactivateCall)
	require.Nil(t, authErr)
	require.True(t, verification.DesiredEnabled)
	m.mu.Lock()
	m.mutation = ""
	m.mutationPhase = ""
	m.mu.Unlock()
	m.RevokeLifecycleAdmission(exceptionDeactivate)
	_, authErr = m.VerifyLifecycleCall(exceptionDeactivate, deactivateCall)
	require.Error(t, authErr, "cleanup admission must die with its command")

	ordinary, err := m.IssueRuntimeAdmission("sample", installation, instance, []string{"native.actor.read"})
	require.NoError(t, err)
	_, authErr = m.VerifyLifecycleCall(ordinary, deactivateCall)
	require.Error(t, authErr, "ordinary admissions never verify as lifecycle")
	_, ok = m.VerifyBackgroundAdmission(exceptionActivate)
	require.False(t, ok, "lifecycle admissions never verify as background")
}

// Disable still cleans up after the optional backend is lost, and even
// when its package bytes and receipt are gone: observe and deactivate
// need the persisted installation UUID and the controller relation,
// never the optional bytes.
func TestDisableAfterControlledBackendLoss(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
	enabledSample(t, h)
	h.m.mu.Lock()
	item := h.m.running["sample"]
	require.NotNil(t, item)
	item.client.Kill()
	h.m.mu.Unlock()
	_, _, ok := h.m.Lookup("sample")
	require.False(t, ok, "crashed backend must lose admission")
	require.NoError(t, os.RemoveAll(filepath.Join(h.root, "sample")))
	requestEnabledAccept(t, h.m, "sample", "1", h.sha, false)
	waitActiveOpDone(t, h.m)
	disabled := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleDisabled, disabled.State)
	require.False(t, disabled.Unconfirmed)
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation)
	require.False(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	_, err = os.Lstat(filepath.Join(controllerStateDir(h), "effect.up"))
	require.True(t, os.IsNotExist(err))
}

// Restart re-observation: a running effect without a current grant is
// failed, never enabled. An explicit retry with the same desired flag
// increments exactly once and adopts the verified owned effect without
// restart, refreshing the grant.
func TestRestartReobservationAdopts(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
	enabledSample(t, h)
	before, err := os.ReadFile(filepath.Join(controllerStateDir(h), "effect.up"))
	require.NoError(t, err)
	require.Equal(t, "1", string(before))
	require.NoError(t, os.Remove(filepath.Join(controllerStateDir(h), "grant.json")))
	require.NoError(t, h.m.Close())

	m := NewManager(h.root, "controller")
	m.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
	require.NoError(t, m.SetCatalogPath(filepath.Join(h.root, ".test-catalog.json")))
	require.NoError(t, m.SetCallbackHandlerFactory(func(instanceID string) http.Handler {
		return testAuthorizeMux(m, instanceID)
	}))
	// Keep automatic boot adoption deferred while checking the closed state.
	require.NoError(t, m.acquireMutation("test-guard"))
	require.NoError(t, m.Start(context.Background()))
	t.Cleanup(func() { _ = m.Close() })
	h.m = m
	observed := lifecycleResult(t, m.Observe("sample"))
	require.Equal(t, packages.LifecycleFailed, observed.State)
	require.True(t, observed.Unconfirmed)
	_, _, ok := m.Lookup("sample")
	require.False(t, ok, "optional backend waits for admitted activation")
	m.releaseMutation()

	requestEnabledAccept(t, m, "sample", "1", h.sha, true)
	waitActiveOpDone(t, m)
	retried := lifecycleResult(t, m.Observe("sample"))
	require.Equal(t, packages.LifecycleEnabled, retried.State)
	require.False(t, retried.Unconfirmed)
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation)
	require.True(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	after, err := os.ReadFile(filepath.Join(controllerStateDir(h), "effect.up"))
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "adopt must not restart owned effects")
	ownerData, err := os.ReadFile(filepath.Join(controllerStateDir(h), "owner.json"))
	require.NoError(t, err)
	var owner fixtureOwner
	require.NoError(t, json.Unmarshal(ownerData, &owner))
	require.Equal(t, "1", owner.EffectGeneration, "prior effect generation stays valid evidence")
	observed = lifecycleResult(t, m.Observe("sample"))
	require.Equal(t, packages.LifecycleEnabled, observed.State)
	require.False(t, observed.Unconfirmed)
}

// restartControllerHarness stops Fountain while on-disk effect state
// survives, then starts a fresh manager over the same root and waits
// for the bounded startup pass: the offline disable/restart path.
func restartControllerHarness(t *testing.T, h *controllerHarness) {
	t.Helper()
	require.NoError(t, h.m.Close())
	m := NewManager(h.root, "controller")
	m.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
	require.NoError(t, m.SetCatalogPath(filepath.Join(h.root, ".test-catalog.json")))
	require.NoError(t, m.SetCallbackHandlerFactory(func(instanceID string) http.Handler {
		return testAuthorizeMux(m, instanceID)
	}))
	require.NoError(t, m.Start(context.Background()))
	t.Cleanup(func() { _ = m.Close() })
	h.m = m
	h.sha = m.catalogSHA
	waitStartupFinished(t, m)
}

// Cold disabled rows must be observed through the required
// controller after restart, not trusted from the empty journal: with
// surviving external effects and no backend, the offline disable
// below stops residual effects at the same generation without
// starting the optional backend, and a later enable works again.
func TestColdDisabledBootStopsResidualEffects(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
	enabledSample(t, h)
	stateDir := controllerStateDir(h)
	_, err := os.Lstat(filepath.Join(stateDir, "effect.up"))
	require.NoError(t, err, "enabled sample must own external effects")
	activations := readActivateCount(stateDir)
	require.NoError(t, h.m.Close())
	// The supported offline command writes stable disabled intent
	// without stopping external resources.
	setDesiredState(t, h.root, "sample", false)
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation)
	observes := readObserveCount(stateDir)
	restartControllerHarness(t, h)
	require.Greater(t, readObserveCount(stateDir), observes, "boot must observe through the required controller")
	_, err = os.Lstat(filepath.Join(stateDir, "effect.up"))
	require.True(t, os.IsNotExist(err), "boot must stop residual effects")
	require.Equal(t, 1, readDeactivateCount(stateDir), "exactly the boot cleanup deactivates")
	require.Equal(t, activations, readActivateCount(stateDir), "boot must not reactivate")
	_, _, ok := h.m.Lookup("sample")
	require.False(t, ok, "optional backend stays unstarted")
	h.m.mu.Lock()
	_, started := h.m.running["sample"]
	h.m.mu.Unlock()
	require.False(t, started, "optional backend stays unstarted")
	intent, err = packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation, "boot must not advance the generation")
	require.False(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	disabled := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleDisabled, disabled.State)
	require.False(t, disabled.Unconfirmed)
	_, _, ok = h.m.Lookup("controller")
	require.True(t, ok, "core stays available")
	require.NoError(t, packages.ValidateInstalled(h.root, h.sampleEntry), "cleanup retains package bytes")
	// Enable-while-disabled still works after the cold cleanup.
	requestEnabledAccept(t, h.m, "sample", "2", h.sha, true)
	waitActiveOpDone(t, h.m)
	reenabled := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleEnabled, reenabled.State)
	require.False(t, reenabled.Unconfirmed)
}

// A cold disabled row with already-stopped effects and no backend
// needs no work and no intent rewrite: no deactivate call, no
// generation change, and a confirmed disabled status.
func TestColdDisabledBootLeavesStoppedRowUnchanged(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
	enabledSample(t, h)
	requestEnabledAccept(t, h.m, "sample", "1", h.sha, false)
	waitActiveOpDone(t, h.m)
	stateDir := controllerStateDir(h)
	require.Equal(t, 1, readDeactivateCount(stateDir))
	observes := readObserveCount(stateDir)
	restartControllerHarness(t, h)
	require.Greater(t, readObserveCount(stateDir), observes, "boot must still observe the disabled row")
	require.Equal(t, 1, readDeactivateCount(stateDir), "stopped effects need no deactivate")
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation)
	require.False(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	disabled := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleDisabled, disabled.State)
	require.False(t, disabled.Unconfirmed)
	_, _, ok := h.m.Lookup("sample")
	require.False(t, ok)
}

// A disabled row with confirmed stopped effects but a still-running
// ordinary backend must clean the backend too: the registered
// reconciliation route runs the generic teardown after the confirmed
// stopped observation, keeping admission closed and preserving
// identity, generation, bytes and private data.
func TestDisabledReconcileStopsLiveBackendAfterStoppedEffects(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
	enabledSample(t, h)
	beforeInstall, err := packages.LoadInstallation(h.root, "sample")
	require.NoError(t, err)
	stateDir := controllerStateDir(h)
	activations := readActivateCount(stateDir)
	// External effects stop out-of-band while the ordinary backend
	// keeps running: the next observation confirms stopped effects
	// with a live backend.
	require.NoError(t, os.Remove(filepath.Join(stateDir, "effect.up")))
	require.NoError(t, os.Remove(filepath.Join(stateDir, "owner.json")))
	require.NoError(t, os.Remove(filepath.Join(stateDir, "grant.json")))
	observes := readObserveCount(stateDir)
	deactivations := readDeactivateCount(stateDir)
	// Stable disabled intent, byte-identical to what the supported
	// offline command writes: same installation, next generation,
	// no pending. The package store is locked while the manager
	// runs, so the test performs the same file-level
	// compare-and-swap the offline command performs.
	require.NoError(t, packages.ValidateInstalled(h.root, h.sampleEntry), "offline command would accept the installed bytes")
	installation, err := packages.EnsureInstallation(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, beforeInstall, installation)
	intent, err := packages.StoreIntent(h.root, "sample", installation, false, nil, "1")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation)
	require.False(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	// Drive the registered reconciliation route, holding the single
	// mutation slot the way the startup attempt does.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := h.m.acquireMutation("test"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mutation slot never freed for reconcile")
		}
		time.Sleep(50 * time.Millisecond)
	}
	terminal := h.m.reconcileStablePass(context.Background(), true, make(map[string]bool))
	h.m.releaseMutation()
	require.True(t, terminal, "stopped effects with a live backend must reach a definite outcome")
	require.Greater(t, readObserveCount(stateDir), observes, "reconcile must observe through the required controller")
	require.Equal(t, deactivations+1, readDeactivateCount(stateDir), "exactly the reconcile teardown deactivates")
	require.Equal(t, activations, readActivateCount(stateDir), "reconcile must not reactivate")
	_, err = os.Lstat(filepath.Join(stateDir, "effect.up"))
	require.True(t, os.IsNotExist(err), "effects stay stopped")
	h.m.mu.Lock()
	_, backendStarted := h.m.running["sample"]
	h.m.mu.Unlock()
	require.False(t, backendStarted, "reconcile must stop the ordinary backend")
	_, _, ok := h.m.Lookup("sample")
	require.False(t, ok, "optional backend serves nothing after teardown")
	require.False(t, h.m.ordinaryAdmissionOpen("sample"), "admission stays closed after teardown")
	intent, err = packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation, "reconcile must not advance the generation")
	require.False(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	afterInstall, err := packages.LoadInstallation(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, beforeInstall, afterInstall, "reconcile preserves installation identity")
	require.NoError(t, packages.ValidateInstalled(h.root, h.sampleEntry), "teardown retains package bytes")
	require.FileExists(t, filepath.Join(h.root, ".data", "sample", "fixture.json"), "teardown retains private data")
	disabled := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleDisabled, disabled.State)
	require.False(t, disabled.Unconfirmed)
}

// Unknown cold effects are failed and unconfirmed, never confirmed
// disabled and never an automatic effect: the owned runtime survives
// boot untouched, and an explicit administrator disable still owns
// the bounded cleanup.
func TestColdDisabledBootUnknownStaysUnconfirmed(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
	enabledSample(t, h)
	stateDir := controllerStateDir(h)
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "observe.unknown"), nil, 0o600))
	require.NoError(t, h.m.Close())
	setDesiredState(t, h.root, "sample", false)
	restartControllerHarness(t, h)
	require.Equal(t, 0, readDeactivateCount(stateDir), "unknown effects must not authorize a boot deactivate")
	_, err := os.Lstat(filepath.Join(stateDir, "effect.up"))
	require.NoError(t, err, "unknown effects survive boot untouched")
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation)
	require.False(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	observed := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleFailed, observed.State)
	require.True(t, observed.Unconfirmed)
	require.NotNil(t, observed.Error)
	_, _, ok := h.m.Lookup("sample")
	require.False(t, ok, "admission stays closed while effects are unknown")
	// Explicit disable remains available for the bounded cleanup.
	requestEnabledAccept(t, h.m, "sample", "2", h.sha, false)
	waitActiveOpDone(t, h.m)
	disabled := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleDisabled, disabled.State)
	require.False(t, disabled.Unconfirmed)
	_, err = os.Lstat(filepath.Join(stateDir, "effect.up"))
	require.True(t, os.IsNotExist(err), "explicit disable stops the effects")
}

// Missing optional bytes, receipt and backend never bypass required
// cleanup: with the package directory gone, boot still observes
// through the controller and stops residual effects.
func TestColdDisabledBootWithoutBytesStillCleans(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
	enabledSample(t, h)
	stateDir := controllerStateDir(h)
	activations := readActivateCount(stateDir)
	require.NoError(t, h.m.Close())
	setDesiredState(t, h.root, "sample", false)
	require.NoError(t, os.RemoveAll(filepath.Join(h.root, "sample")))
	restartControllerHarness(t, h)
	_, err := os.Lstat(filepath.Join(stateDir, "effect.up"))
	require.True(t, os.IsNotExist(err), "boot must stop residual effects without package bytes")
	require.Equal(t, 1, readDeactivateCount(stateDir))
	require.Equal(t, activations, readActivateCount(stateDir))
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "2", intent.Generation)
	require.False(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
	disabled := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleDisabled, disabled.State)
	require.False(t, disabled.Unconfirmed)
}

// An explicit disable with no backend and stopped effects verifies
// teardown through the required controller and converges: a no-op
// backend stop still reports disabled.
func TestDisabledTeardownWithoutBackendVerifies(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
	enabledSample(t, h)
	stateDir := controllerStateDir(h)
	requestEnabledAccept(t, h.m, "sample", "1", h.sha, false)
	waitActiveOpDone(t, h.m)
	disabled := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleDisabled, disabled.State)
	require.False(t, disabled.Unconfirmed)
	deactivations := readDeactivateCount(stateDir)
	requestEnabledAccept(t, h.m, "sample", "2", h.sha, false)
	waitActiveOpDone(t, h.m)
	again := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleDisabled, again.State)
	require.False(t, again.Unconfirmed)
	require.Greater(t, readDeactivateCount(stateDir), deactivations, "teardown reverifies through the controller")
	_, err := os.Lstat(filepath.Join(stateDir, "effect.up"))
	require.True(t, os.IsNotExist(err))
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "3", intent.Generation)
	require.False(t, intent.DesiredEnabled)
	require.Nil(t, intent.PendingOperation)
}

// Absent or stale controller facts never confirm disabled: only a
// current stopped observation does. Never-activated and uncontrolled
// rows keep their existing readings.
func TestMergeControlledEffectNeedsCurrentObservation(t *testing.T) {
	disabledBase := packages.LifecycleResult{Operation: sdk.LifecycleObserve, PackageID: "sample", State: packages.LifecycleDisabled}
	intent := packages.Intent{Schema: 1, PackageID: "sample", InstallationID: "installation-a", Generation: "1"}
	current := controlledEffect{state: sdk.LifecycleStateStopped, generation: "1", installation: "installation-a", instanceID: "instance-a"}

	// Lost fact: no observation at all.
	lost := mergeControlledEffect(disabledBase, "sample", true, true, "instance-a", intent, nil, false, controlledEffect{}, false)
	require.Equal(t, packages.LifecycleFailed, lost.State)
	require.True(t, lost.Unconfirmed)
	require.NotNil(t, lost.Error)

	// Stale last-known-good: generation, installation and instance
	// bindings each fail closed.
	staleGeneration := current
	staleGeneration.generation = "0"
	stale := mergeControlledEffect(disabledBase, "sample", true, true, "instance-a", intent, nil, false, staleGeneration, true)
	require.Equal(t, packages.LifecycleFailed, stale.State)
	require.True(t, stale.Unconfirmed)
	staleInstallation := current
	staleInstallation.installation = "installation-b"
	stale = mergeControlledEffect(disabledBase, "sample", true, true, "instance-a", intent, nil, false, staleInstallation, true)
	require.Equal(t, packages.LifecycleFailed, stale.State)
	require.True(t, stale.Unconfirmed)
	staleInstance := current
	staleInstance.instanceID = "instance-b"
	stale = mergeControlledEffect(disabledBase, "sample", true, true, "instance-a", intent, nil, false, staleInstance, true)
	require.Equal(t, packages.LifecycleFailed, stale.State)
	require.True(t, stale.Unconfirmed)

	// Unreachable controller with a disabled base is unobserved, not disabled.
	unreachable := mergeControlledEffect(disabledBase, "sample", true, false, "", intent, nil, false, controlledEffect{}, false)
	require.Equal(t, packages.LifecycleFailed, unreachable.State)
	require.True(t, unreachable.Unconfirmed)

	// The current stopped fact still confirms disabled.
	confirmed := mergeControlledEffect(disabledBase, "sample", true, true, "instance-a", intent, nil, false, current, true)
	require.Equal(t, packages.LifecycleDisabled, confirmed.State)
	require.False(t, confirmed.Unconfirmed)

	// Never-activated rows and uncontrolled rows keep their readings.
	neverIntent := packages.Intent{Schema: 1, PackageID: "sample", Generation: "0"}
	never := mergeControlledEffect(disabledBase, "sample", true, true, "instance-a", neverIntent, nil, false, controlledEffect{}, false)
	require.Equal(t, packages.LifecycleDisabled, never.State)
	plain := mergeControlledEffect(disabledBase, "sample", false, false, "", intent, nil, false, controlledEffect{}, false)
	require.Equal(t, packages.LifecycleDisabled, plain.State)
}

// The maintenance directory inode excludes mutations while a second
// process holds it exclusively or the marker persists, without
// deadlocking reads that hold no lock. The fixture directory is not
// root-owned, so production fail-closed refusal carries these cases
// here; the root-owned success path is covered where the contract can
// be built. Recovery after marker removal runs on a fresh manager
// without the fixture guard.
func TestMaintenanceGuardBlocksMutation(t *testing.T) {
	maintenanceDir := t.TempDir()
	require.NoError(t, os.Chmod(maintenanceDir, 0o755))
	h := startControllerHarness(t, maintenanceDir, testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)

	executable, err := os.Executable()
	require.NoError(t, err)
	holder := exec.Command(executable)
	holder.Env = append(os.Environ(), "FORGEJO_EXTENSION_TEST_HELPER=1", "FORGEJO_EXTENSION_TEST_HOLDDIR="+maintenanceDir)
	stdin, err := holder.StdinPipe()
	require.NoError(t, err)
	stdout, err := holder.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, holder.Start())
	ready := make([]byte, 16)
	_, err = io.ReadFull(stdout, ready[:6])
	require.NoError(t, err, "holder must print READY")

	blocked := h.m.RequestEnabled(context.Background(), "sample", "0", h.sha, true)
	require.Equal(t, packages.LifecycleFailed, blocked.State)
	require.Equal(t, packages.LifecycleErrMaintenance, blocked.Error.Code)
	snapshot := h.m.Snapshot()
	require.NotNil(t, snapshot, "reads must not deadlock under exclusive exclusion")
	require.NoError(t, stdin.Close())
	require.NoError(t, holder.Wait())

	require.NoError(t, os.WriteFile(filepath.Join(maintenanceDir, "operation.json"), []byte(`{}`), 0o600))
	marked := h.m.RequestEnabled(context.Background(), "sample", "0", h.sha, true)
	require.Equal(t, packages.LifecycleFailed, marked.State)
	require.Equal(t, packages.LifecycleErrMaintenance, marked.Error.Code)
	require.NoError(t, os.Remove(filepath.Join(maintenanceDir, "operation.json")))

	require.NoError(t, h.m.Close())
	recoveredManager := NewManager(h.root, "controller")
	recoveredManager.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
	configureTestCatalog(t, recoveredManager, h.root, h.controllerEntry, h.sampleEntry)
	require.NoError(t, recoveredManager.SetCallbackHandlerFactory(func(instanceID string) http.Handler {
		return testAuthorizeMux(recoveredManager, instanceID)
	}))
	require.NoError(t, recoveredManager.Start(context.Background()))
	t.Cleanup(func() { _ = recoveredManager.Close() })
	requestEnabledAccept(t, recoveredManager, "sample", "0", recoveredManager.catalogSHA, true)
	waitActiveOpDone(t, recoveredManager)
	recovered := lifecycleResult(t, recoveredManager.Observe("sample"))
	require.Equal(t, packages.LifecycleEnabled, recovered.State)
	require.False(t, recovered.Unconfirmed)
}

// Enable downloads and validates the catalog-selected archive when no
// installed bytes satisfy the pin. Trust anchors come from the
// environment; no test client bypasses the bounded production fetcher.
func TestEnableDownloadsCatalogArtifact(t *testing.T) {
	for _, controller := range []string{"controller", ""} {
		t.Run("controller="+controller, func(t *testing.T) {
			root := shortTestRoot(t)
			installLifecyclePackage(t, root, "controller", controllerManifest, testFixtureKnobs{Role: "controller"})
			installLifecyclePackage(t, root, "sample", sampleManifest, testFixtureKnobs{})
			stage := filepath.Join(root, ".initial-sample")
			require.NoError(t, os.Rename(filepath.Join(root, "sample"), stage))
			store, err := packages.OpenPackageStore(root)
			require.NoError(t, err)
			_, err = store.InstallStaged(stage, packages.InstallOptions{})
			require.NoError(t, err)
			require.NoError(t, store.Close())
			body := buildSampleArchive(t, controller)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(body)
			}))
			t.Cleanup(server.Close)
			sum := sha256.Sum256(body)
			sampleEntry := testCatalogEntry("sample", "1.0.0", false, controller)
			sampleEntry.Capabilities = []string{"native.actor.read"}
			sampleEntry.URL = server.URL + "/sample.tar.gz"
			sampleEntry.Size = int64(len(body))
			sampleEntry.SHA256 = hex.EncodeToString(sum[:])
			controllerEntry := testCatalogEntry("controller", "1.0.0", true, "")
			controllerEntry.Capabilities = []string{"native.extension.lifecycle"}
			// The required controller ships an exact installed receipt, as
			// production installs do; only the optional bytes download.
			require.NoError(t, packages.WriteReceipt(filepath.Join(root, "controller"), controllerEntry))
			certPath := filepath.Join(root, "test-ca.pem")
			require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
			t.Setenv("SSL_CERT_FILE", certPath)

			m := NewManager(root, "controller")
			m.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
			configureTestCatalog(t, m, root, controllerEntry, sampleEntry)
			require.NoError(t, m.SetCallbackHandlerFactory(func(instanceID string) http.Handler {
				return testAuthorizeMux(m, instanceID)
			}))
			require.NoError(t, m.Start(context.Background()))
			t.Cleanup(func() { _ = m.Close() })
			sha := m.catalogSHA
			requestEnabledAccept(t, m, "sample", "0", sha, true)
			waitActiveOpDone(t, m)
			downloaded := lifecycleResult(t, m.Observe("sample"))
			require.Equal(t, packages.LifecycleEnabled, downloaded.State)
			require.False(t, downloaded.Unconfirmed)
			require.NoError(t, packages.ValidateInstalled(root, sampleEntry))
		})
	}
}

func buildSampleArchive(t *testing.T, controller string) []byte {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	backend, err := os.ReadFile(executable)
	require.NoError(t, err)
	manifest := fmt.Sprintf(`{"protocol":1,"id":"sample","name":"Test","version":"1.0.0","executable":"backend","capabilities":["native.actor.read"],"lifecycle_controller":%q}`, controller)
	files := map[string][]byte{
		"extension.json": []byte(manifest),
		"backend":        backend,
		"LICENSE":        []byte("test fixture"),
	}
	var archive bytes.Buffer
	unzip, err := gzip.NewWriterLevel(&archive, gzip.BestSpeed)
	require.NoError(t, err)
	writer := tar.NewWriter(unzip)
	for name, body := range files {
		mode := int64(0o644)
		if name == "backend" {
			mode = 0o755
		}
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Format: tar.FormatUSTAR}))
		_, err := writer.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.NoError(t, unzip.Close())
	return archive.Bytes()
}

// The authorize matrix pins callback validation without processes: token
// variant, deadline, instance, pin, UUID, generation, pending/desired,
// and body equality each fail closed with the mapped HTTP status.
func TestVerifyLifecycleCallMatrix(t *testing.T) {
	root := shortTestRoot(t)
	m := NewManager(root)
	controllerEntry := testCatalogEntry("controller", "1.0.0", true, "")
	sampleEntry := testCatalogEntry("sample", "1.0.0", false, "controller")
	m.catalog = []sdk.CatalogPackage{controllerEntry, sampleEntry}
	m.controllers = map[string]controllerState{"controller": {instanceID: "instance-a", ready: true}}
	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	activate := packages.IntentActivate
	_, err = packages.StoreIntent(root, "sample", installation, true, &activate, "0")
	require.NoError(t, err)

	call := sdk.LifecycleCall{ControllerID: "controller", ControllerInstanceID: "instance-a", PackageID: "sample", PackageVersion: "1.0.0", PackageSHA256: sampleEntry.SHA256, InstallationID: installation, Generation: "1", Operation: sdk.LifecycleObserve, DeadlineUnix: time.Now().Unix() + 60}
	issue := func(call sdk.LifecycleCall, desired, cleanup bool) string {
		token, err := m.IssueLifecycleAdmission(&BackgroundAdmission{Lifecycle: true, InstallationID: installation, InstanceID: "instance-a", Call: call, DesiredEnabled: desired, CleanupException: cleanup})
		require.NoError(t, err)
		return token
	}
	verification, authErr := m.VerifyLifecycleCall(issue(call, true, false), call)
	require.Nil(t, authErr)
	require.True(t, verification.DesiredEnabled)

	_, authErr = m.VerifyLifecycleCall("bogus", call)
	require.Error(t, authErr)
	require.Equal(t, 403, authErr.HTTP)

	expired := call
	expired.DeadlineUnix = time.Now().Unix() - 1
	_, authErr = m.VerifyLifecycleCall(issue(expired, true, false), expired)
	require.Error(t, authErr)
	require.Equal(t, 403, authErr.HTTP)

	mismatched := call
	mismatched.Generation = "99"
	_, authErr = m.VerifyLifecycleCall(issue(call, true, false), mismatched)
	require.Error(t, authErr)
	require.Equal(t, 403, authErr.HTTP)

	activateCall := call
	activateCall.Operation = sdk.LifecycleActivate
	_, authErr = m.VerifyLifecycleCall(issue(activateCall, true, false), activateCall)
	require.Nil(t, authErr)

	deactivateCall := call
	deactivateCall.Operation = sdk.LifecycleDeactivate
	_, authErr = m.VerifyLifecycleCall(issue(deactivateCall, false, false), deactivateCall)
	require.Error(t, authErr, "deactivate must not verify while desired is true")
	require.Equal(t, 403, authErr.HTTP)
}

// waitBackendRegistered polls until the backend is published into
// m.running: registration happened, but activation may still be
// unconfirmed.
func waitBackendRegistered(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		m.mu.Lock()
		item := m.running[id]
		m.mu.Unlock()
		if item != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend %s never registered", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// registeredInstance returns the live backend's instance and
// installation identity for token-binding assertions.
func registeredInstance(t *testing.T, m *Manager, id string) (instanceID, installation string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.running[id]
	require.NotNil(t, item, "backend %s must stay registered", id)
	return item.descriptor.InstanceID, item.installation
}

func requireNavigationClosed(t *testing.T, m *Manager, id string) {
	t.Helper()
	for _, listed := range m.List() {
		require.NotEqual(t, id, listed.Manifest.ID, "registered backend must stay out of navigation")
	}
}

// A registered backend is an internal lifecycle participant until
// durable activation completes: no navigation, no ordinary
// proxy/callback admission, no usable background token, and a closed
// ControlState projection. Durable completion opens every ordinary
// path once, and the retained token works without redelivery.
func TestEnableFencesAdmissionUntilDurableCompletion(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller", SlowObserveMs: 2000}, true)
	requestEnabledAccept(t, h.m, "sample", "0", h.sha, true)
	// The final observation is still sleeping: the backend is
	// registered but activation is not durably complete.
	waitBackendRegistered(t, h.m, "sample")
	instanceID, installation := registeredInstance(t, h.m, "sample")
	_, _, ok := h.m.Lookup("sample")
	require.False(t, ok, "registered backend must not route before durable completion")
	requireNavigationClosed(t, h.m, "sample")
	response, reject := h.m.ControlState(context.Background(), "controller", "sample")
	require.Nil(t, reject)
	require.False(t, response.AdmissionOpen, "ControlState must project the closed fence")
	token, err := h.m.IssueRuntimeAdmission("sample", installation, instanceID, []string{"native.actor.read"})
	require.NoError(t, err)
	_, ok = h.m.VerifyBackgroundAdmission(token)
	require.False(t, ok, "previously issued tokens cannot bypass the fence")
	waitActiveOpDone(t, h.m)
	_, _, ok = h.m.Lookup("sample")
	require.True(t, ok, "durable completion must open routing")
	_, ok = h.m.VerifyBackgroundAdmission(token)
	require.True(t, ok, "retained token must work after durable completion")
	response, reject = h.m.ControlState(context.Background(), "controller", "sample")
	require.Nil(t, reject)
	require.True(t, response.AdmissionOpen, "durable completion must open the projection")
	observed := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleEnabled, observed.State)
	require.False(t, observed.Unconfirmed)
}

// A failed final observation keeps the fence after the operation
// returns: no routing, no usable tokens, original generation and
// desired intent preserved, effects cleaned up, and nothing replayed
// automatically. Explicit disable stays usable, and a later retry
// completes and reopens admission.
func TestFailedFinalObserveKeepsAdmissionClosed(t *testing.T) {
	for _, marker := range []string{"observe.unknown", "observe.unavailable"} {
		t.Run(marker, func(t *testing.T) {
			h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller", SlowObserveMs: 2000}, true)
			requestEnabledAccept(t, h.m, "sample", "0", h.sha, true)
			waitBackendRegistered(t, h.m, "sample")
			instanceID, installation := registeredInstance(t, h.m, "sample")
			// Arm the failure only now: the pre-activation
			// observation already passed.
			require.NoError(t, os.WriteFile(filepath.Join(controllerStateDir(h), marker), nil, 0o600))
			token, err := h.m.IssueRuntimeAdmission("sample", installation, instanceID, []string{"native.actor.read"})
			require.NoError(t, err)
			waitActiveOpDone(t, h.m)
			observed := lifecycleResult(t, h.m.Observe("sample"))
			require.Equal(t, packages.LifecycleFailed, observed.State)
			require.True(t, observed.Unconfirmed)
			require.NotNil(t, observed.Error)
			require.Equal(t, packages.LifecycleErrActivateFailed, observed.Error.Code)
			_, _, ok := h.m.Lookup("sample")
			require.False(t, ok, "failed enable must keep admission closed")
			requireNavigationClosed(t, h.m, "sample")
			_, ok = h.m.VerifyBackgroundAdmission(token)
			require.False(t, ok, "fenced tokens must not verify")
			intent, err := packages.LoadIntent(h.root, "sample")
			require.NoError(t, err)
			require.Equal(t, "1", intent.Generation)
			require.True(t, intent.DesiredEnabled)
			require.NotNil(t, intent.PendingOperation)
			require.Equal(t, packages.IntentActivate, *intent.PendingOperation)
			_, err = os.Lstat(filepath.Join(controllerStateDir(h), "effect.up"))
			require.True(t, os.IsNotExist(err), "failed-enable cleanup must stop effects")
			require.Equal(t, 1, readActivateCount(controllerStateDir(h)), "uncertain activation is never replayed")
			// Controller cleanup stays usable: explicit disable works.
			requestEnabledAccept(t, h.m, "sample", "1", h.sha, false)
			waitActiveOpDone(t, h.m)
			disabled := lifecycleResult(t, h.m.Observe("sample"))
			require.Equal(t, packages.LifecycleDisabled, disabled.State)
			require.False(t, disabled.Unconfirmed)
			// A later explicit retry completes and reopens admission.
			require.NoError(t, os.Remove(filepath.Join(controllerStateDir(h), marker)))
			requestEnabledAccept(t, h.m, "sample", "2", h.sha, true)
			waitActiveOpDone(t, h.m)
			retried := lifecycleResult(t, h.m.Observe("sample"))
			require.Equal(t, packages.LifecycleEnabled, retried.State)
			require.False(t, retried.Unconfirmed)
			_, _, ok = h.m.Lookup("sample")
			require.True(t, ok, "retry must reopen admission")
		})
	}
}

// A completion-write failure stays pending and unconfirmed, never
// masquerading as completed: the retained live backend stays fenced
// with its token rejected, and an explicit retry after storage
// recovers completes on the same instance with the same token.
func TestPersistFailureKeepsAdmissionClosed(t *testing.T) {
	h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller", SlowObserveMs: 2000}, true)
	requestEnabledAccept(t, h.m, "sample", "0", h.sha, true)
	waitBackendRegistered(t, h.m, "sample")
	instanceID, installation := registeredInstance(t, h.m, "sample")
	token, err := h.m.IssueRuntimeAdmission("sample", installation, instanceID, []string{"native.actor.read"})
	require.NoError(t, err)
	// Break the completion write while the final observation sleeps:
	// replace the intent file with a directory so the durable write
	// cannot land.
	intentPath := filepath.Join(h.root, ".intents", "sample.json")
	intentData, err := os.ReadFile(intentPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(intentPath))
	require.NoError(t, os.Mkdir(intentPath, 0o700))
	restore := func() {
		_ = os.RemoveAll(intentPath)
		_ = os.WriteFile(intentPath, intentData, 0o600)
	}
	t.Cleanup(restore)
	waitActiveOpDone(t, h.m)
	observed := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleFailed, observed.State)
	require.True(t, observed.Unconfirmed)
	require.NotNil(t, observed.Error)
	require.Equal(t, packages.LifecycleErrUnavailable, observed.Error.Code)
	_, _, ok := h.m.Lookup("sample")
	require.False(t, ok, "unpersisted completion must keep admission closed")
	requireNavigationClosed(t, h.m, "sample")
	_, ok = h.m.VerifyBackgroundAdmission(token)
	require.False(t, ok, "fenced tokens must not verify")
	// The backend is retained live but fenced; nothing replayed.
	_, retainedInstallation := registeredInstance(t, h.m, "sample")
	require.Equal(t, installation, retainedInstallation)
	require.Equal(t, 1, readActivateCount(controllerStateDir(h)), "uncertain activation is never replayed")
	// Restore storage and retry: the same instance and token complete.
	restore()
	intent, err := packages.LoadIntent(h.root, "sample")
	require.NoError(t, err)
	require.Equal(t, "1", intent.Generation)
	require.True(t, intent.DesiredEnabled)
	require.NotNil(t, intent.PendingOperation)
	requestEnabledAccept(t, h.m, "sample", "1", h.sha, true)
	waitActiveOpDone(t, h.m)
	retried := lifecycleResult(t, h.m.Observe("sample"))
	require.Equal(t, packages.LifecycleEnabled, retried.State)
	require.False(t, retried.Unconfirmed)
	_, _, ok = h.m.Lookup("sample")
	require.True(t, ok, "retry must reopen admission")
	_, ok = h.m.VerifyBackgroundAdmission(token)
	require.True(t, ok, "retained token must work after retry completes")
}

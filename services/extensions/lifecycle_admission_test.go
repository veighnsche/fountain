// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Exercise the public lifecycle verification wire unchanged.
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"

	"github.com/stretchr/testify/require"
)

func cleanupVerificationFixture(t *testing.T) (*Manager, sdk.LifecycleCall, string) {
	t.Helper()
	root := t.TempDir()
	m := NewManager(root, "controller")
	entry := testCatalogEntry("sample", "1.0.0", false, "controller")
	m.catalog = []sdk.CatalogPackage{testCatalogEntry("controller", "1.0.0", true, ""), entry}
	m.controllers = map[string]controllerState{"controller": {instanceID: "instance", ready: true}}
	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "sample", installation, true, new(packages.IntentActivate), "0")
	require.NoError(t, err)
	call := buildCall("controller", "instance", entry, installation, "1", lifecycleDeactivate, 60)
	token, err := m.IssueLifecycleAdmission(&BackgroundAdmission{Lifecycle: true, InstallationID: installation, InstanceID: "instance", Call: call, DesiredEnabled: true, lifecyclePurpose: purposeFailedEnableCleanup})
	require.NoError(t, err)
	return m, call, token
}

func TestCleanupAdmissionConsumesOnlyOneSuccessfulExactVerification(t *testing.T) {
	m, call, token := cleanupVerificationFixture(t)
	before, err := os.ReadFile(packages.IntentPath(m.root, "sample"))
	require.NoError(t, err)
	_, authErr := m.VerifyLifecycleCall(token, call)
	require.NotNil(t, authErr, "outside-command failure must not consume the valid call")
	m.mutation, m.mutationPhase = "sample", transitionFailedEnableCleanup
	wrong := call
	wrong.Generation = "2"
	_, authErr = m.VerifyLifecycleCall(token, wrong)
	require.NotNil(t, authErr)
	var successes atomic.Int32
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if _, refusal := m.VerifyLifecycleCall(token, call); refusal == nil {
				successes.Add(1)
			}
		})
	}
	group.Wait()
	require.EqualValues(t, 1, successes.Load())
	_, authErr = m.VerifyLifecycleCall(token, call)
	require.NotNil(t, authErr)
	_, authErr = m.VerifyServiceLifecycleCall(token, wrong)
	require.NotNil(t, authErr, "a malformed second hop must not burn its valid cleanup check")
	_, authErr = m.VerifyServiceLifecycleCall(token, call)
	require.Nil(t, authErr)
	_, authErr = m.VerifyServiceLifecycleCall(token, call)
	require.NotNil(t, authErr)
	after, err := os.ReadFile(packages.IntentPath(m.root, "sample"))
	require.NoError(t, err)
	require.Equal(t, before, after, "cleanup retains desired-enabled and pending activate")
}

func TestCleanupAdmissionSupportsControllerThenServiceHTTPVerification(t *testing.T) {
	m, call, token := cleanupVerificationFixture(t)
	m.mutation, m.mutationPhase = "sample", transitionFailedEnableCleanup
	// HTTP exercises the same opaque authority and strict SDK decoder on both
	// authenticated hops. Peer authentication belongs to the router multiplexer.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		presented, err := sdk.DecodeLifecycleCall(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var verification sdk.LifecycleVerification
		var refusal *AuthError
		if r.URL.Path == "/service" {
			verification, refusal = m.VerifyServiceLifecycleCall(r.Header.Get(sdk.LifecycleAdmissionHeader), presented)
		} else {
			verification, refusal = m.VerifyLifecycleCall(r.Header.Get(sdk.LifecycleAdmissionHeader), presented)
		}
		if refusal != nil {
			w.WriteHeader(refusal.HTTP)
			return
		}
		_ = json.NewEncoder(w).Encode(verification)
	}))
	t.Cleanup(server.Close)
	verify := func(path, authority string, presented sdk.LifecycleCall, expected int) {
		t.Helper()
		body, err := json.Marshal(presented)
		require.NoError(t, err)
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, bytes.NewReader(body))
		require.NoError(t, err)
		request.Header.Set(sdk.LifecycleAdmissionHeader, authority)
		response, err := server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, expected, response.StatusCode)
		body, err = io.ReadAll(response.Body)
		require.NoError(t, err)
		if expected == http.StatusOK {
			verification, err := sdk.DecodeLifecycleVerification(body)
			require.NoError(t, err)
			require.Equal(t, presented, verification.Call)
			require.True(t, verification.DesiredEnabled)
		}
	}
	verify("/service", token, call, http.StatusForbidden)
	verify("/controller", token, call, http.StatusOK)
	verify("/controller", token, call, http.StatusForbidden)
	wrong := call
	wrong.Generation = "2"
	verify("/service", token, wrong, http.StatusForbidden)
	verify("/service", token, call, http.StatusOK)
	verify("/service", token, call, http.StatusForbidden)
	_, present := m.VerifyLifecycleAdmission(token)
	require.False(t, present)
	// Normal calls retain their documented repeated Verify behavior.
	ordinary := call
	ordinary.Operation = sdk.LifecycleActivate
	normalToken, err := m.IssueLifecycleAdmission(&BackgroundAdmission{Lifecycle: true, InstallationID: call.InstallationID, InstanceID: "instance", Call: ordinary, DesiredEnabled: true})
	require.NoError(t, err)
	for range 2 {
		verify("/controller", normalToken, ordinary, http.StatusOK)
		verify("/service", normalToken, ordinary, http.StatusOK)
	}
}

func TestControllerSnapshotsExcludeOtherOwnersAndFenceUnknownLegacyIntents(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)
	m.catalog = append(m.catalog, testCatalogEntry("other", "1.0", false, "another-controller"))
	other, err := packages.EnsureInstallation(root, "other")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "other", other, true, nil, "0")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(packages.IntentPath(root, "other"), []byte(`{"schema":`), 0o600))
	rows, err := m.controlSnapshotEntries("soda")
	require.NoError(t, err)
	for _, row := range rows {
		require.NotEqual(t, "other", row.PackageID)
	}
	orphan, err := packages.EnsureInstallation(root, "orphan")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "orphan", orphan, false, nil, "0")
	require.NoError(t, err)
	before, err := os.ReadFile(packages.IntentPath(root, "orphan"))
	require.NoError(t, err)
	_, err = m.controlSnapshotEntries("soda")
	require.Error(t, err)
	require.NoError(t, packages.BindIntentController(root, "orphan", orphan, "another-controller"))
	rows, err = m.controlSnapshotEntries("soda")
	require.NoError(t, err)
	for _, row := range rows {
		require.NotEqual(t, "orphan", row.PackageID)
	}
	after, err := os.ReadFile(packages.IntentPath(root, "orphan"))
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoError(t, os.WriteFile(packages.IntentPath(root, "orphan"), []byte(`{"schema":`), 0o600))
	_, err = m.controlSnapshotEntries("soda")
	require.NoError(t, err, "known other ownership excludes even malformed removed intent bytes")
}

func TestOptionalAttributionFailureDoesNotPreventRequiredControllerStartup(t *testing.T) {
	for _, mode := range []string{"malformed intent", "conflicting attribution"} {
		t.Run(mode, func(t *testing.T) {
			h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
			require.NoError(t, h.m.Close())
			installation, err := packages.EnsureInstallation(h.root, "sample")
			require.NoError(t, err)
			_, err = packages.StoreIntent(h.root, "sample", installation, true, nil, "0")
			require.NoError(t, err)
			if mode == "malformed intent" {
				require.NoError(t, os.WriteFile(packages.IntentPath(h.root, "sample"), []byte(`{"schema":`), 0o600))
			} else {
				require.NoError(t, packages.BindIntentController(h.root, "sample", installation, "another-controller"))
			}
			before, err := os.ReadFile(packages.IntentPath(h.root, "sample"))
			require.NoError(t, err)
			require.NoError(t, h.m.Start(context.Background()))
			_, _, admitted := h.m.Lookup("controller")
			require.True(t, admitted)
			_, _, admitted = h.m.Lookup("sample")
			require.False(t, admitted)
			h.m.mu.Lock()
			blocked := h.m.recoveryBlocked["sample"]
			h.m.mu.Unlock()
			require.True(t, blocked)
			after, err := os.ReadFile(packages.IntentPath(h.root, "sample"))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestExpiredCleanupAdmissionDoesNotConsumeAnUnverifiedToken(t *testing.T) {
	m := NewManager(t.TempDir())
	call := sdk.LifecycleCall{DeadlineUnix: time.Now().Unix() - 1}
	token, err := m.IssueLifecycleAdmission(&BackgroundAdmission{Lifecycle: true, InstallationID: "installation", InstanceID: "instance", Call: call, CleanupException: true})
	require.NoError(t, err)
	_, refusal := m.VerifyLifecycleCall(token, call)
	require.NotNil(t, refusal)
	_, present := m.VerifyLifecycleAdmission(token)
	require.True(t, present)
}

func TestIntentRefusalClassificationUsesOutcomeRatherThanGenerationText(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)
	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "sample", installation, false, nil, "0")
	require.NoError(t, err)
	before, err := os.ReadFile(packages.IntentPath(root, "sample"))
	require.NoError(t, err)
	for _, item := range []struct {
		expected string
		code     string
	}{
		{"0", packages.LifecycleErrStaleState},
		{"01", packages.LifecycleErrInvalidRequest},
	} {
		result := m.RequestEnabled(t.Context(), "sample", item.expected, controlLiveSHA, false)
		require.NotNil(t, result.Error)
		require.Equal(t, item.code, result.Error.Code)
		after, err := os.ReadFile(packages.IntentPath(root, "sample"))
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
	exhausted := strings.Replace(string(before), `"generation":"1"`, `"generation":"18446744073709551615"`, 1)
	require.NotEqual(t, string(before), exhausted)
	require.NoError(t, os.WriteFile(packages.IntentPath(root, "sample"), []byte(exhausted), 0o600))
	result := m.RequestEnabled(t.Context(), "sample", "18446744073709551615", controlLiveSHA, false)
	require.NotNil(t, result.Error)
	require.Equal(t, packages.LifecycleErrUnavailable, result.Error.Code, "an exhausted generation cannot be repaired by reloading stale state")
	after, err := os.ReadFile(packages.IntentPath(root, "sample"))
	require.NoError(t, err)
	require.Equal(t, exhausted, string(after))
}

func TestRemovedPersistedIntentCannotBeAdoptedAsAnUncontrolledPackage(t *testing.T) {
	for _, owner := range []string{"unknown", "controller"} {
		t.Run(owner, func(t *testing.T) {
			root := t.TempDir()
			m := controlManager(t, root)
			packageDir := filepath.Join(root, "orphan")
			require.NoError(t, os.MkdirAll(packageDir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), controlManifest("orphan", ""), 0o600))
			installation, err := packages.EnsureInstallation(root, "orphan")
			require.NoError(t, err)
			_, err = packages.StoreIntent(root, "orphan", installation, false, nil, "0")
			require.NoError(t, err)
			if owner != "unknown" {
				require.NoError(t, packages.BindIntentController(root, "orphan", installation, owner))
			}
			before, err := os.ReadFile(packages.IntentPath(root, "orphan"))
			require.NoError(t, err)
			for _, desired := range []bool{true, false} {
				result := m.RequestEnabled(t.Context(), "orphan", "1", controlLiveSHA, desired)
				require.NotNil(t, result.Error)
				require.Equal(t, packages.LifecycleErrCatalogBinding, result.Error.Code)
			}
			after, err := os.ReadFile(packages.IntentPath(root, "orphan"))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestFreshUncataloguedAdoptionRecordsExplicitUncontrolledAuthority(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)
	packageDir := filepath.Join(root, "local")
	require.NoError(t, os.MkdirAll(packageDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), controlManifest("local", ""), 0o600))
	adopted, refusal := m.adoptMutation("local", "", "0", controlLiveSHA, lifecycleActivate)
	require.Nil(t, refusal)
	require.NotNil(t, adopted)
	m.finishAdopted(adopted, nil)
	binding, known, err := packages.LoadIntentController(root, "local")
	require.NoError(t, err)
	require.True(t, known)
	require.Empty(t, binding.ControllerID)
	require.Equal(t, adopted.intent.InstallationID, binding.InstallationID)
	// Complete the durable intent without a process fixture; the bearer gate
	// must not treat this explicit local choice as unknown legacy ownership.
	_, err = packages.StorePendingSameGeneration(root, "local", true, nil, "1")
	require.NoError(t, err)
	token, err := m.IssueRuntimeAdmission("local", binding.InstallationID, "instance", nil)
	require.NoError(t, err)
	_, admitted := m.VerifyBackgroundAdmission(token)
	require.True(t, admitted)
}

func TestRemovedFencedIntentCannotBlockRequiredStartupWithStaticClaims(t *testing.T) {
	for _, owner := range []string{"unknown", "controller"} {
		t.Run(owner, func(t *testing.T) {
			h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
			require.NoError(t, h.m.Close())
			manifest := strings.Replace(sampleManifest, `,"lifecycle_controller":"controller"`, "", 1)
			manifest = strings.Replace(manifest, `"id":"sample"`, `"id":"orphan"`, 1)
			installLifecyclePackage(t, h.root, "orphan", manifest, testFixtureKnobs{})
			installation, err := packages.EnsureInstallation(h.root, "orphan")
			require.NoError(t, err)
			_, err = packages.StoreIntent(h.root, "orphan", installation, true, nil, "0")
			require.NoError(t, err)
			if owner != "unknown" {
				require.NoError(t, packages.BindIntentController(h.root, "orphan", installation, owner))
			}
			before, err := os.ReadFile(packages.IntentPath(h.root, "orphan"))
			require.NoError(t, err)
			var rejectedClaims atomic.Int32
			require.NoError(t, h.m.SetManifestValidator(func(manifest sdk.Manifest) error {
				if manifest.ID == "orphan" {
					rejectedClaims.Add(1)
					return errors.New("removed package claims a reserved host route")
				}
				return nil
			}))
			require.NoError(t, h.m.Start(t.Context()))
			_, _, admitted := h.m.Lookup("controller")
			require.True(t, admitted)
			_, _, admitted = h.m.Lookup("orphan")
			require.False(t, admitted)
			require.Zero(t, rejectedClaims.Load())
			after, err := os.ReadFile(packages.IntentPath(h.root, "orphan"))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestControlledEnableChecksKnownOriginBeforeControllerEffects(t *testing.T) {
	for _, origin := range []string{"catalog", "prebuilt"} {
		t.Run(origin, func(t *testing.T) {
			h := startControllerHarness(t, "", testFixtureKnobs{}, testFixtureKnobs{Role: "controller"}, true)
			stage, err := os.MkdirTemp(h.root, ".proof-") //nolint:usetesting // Installation consumes a hidden stage under the package root.
			require.NoError(t, err)
			for _, name := range []string{"extension", "extension.json"} {
				from := filepath.Join(h.root, "sample", name)
				body, err := os.ReadFile(from)
				require.NoError(t, err)
				info, err := os.Stat(from)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(stage, name), body, info.Mode().Perm()))
			}
			opts := packages.InstallOptions{Replace: true}
			if origin == "catalog" {
				require.NoError(t, packages.WriteReceipt(stage, h.sampleEntry))
				opts.Entry = &h.sampleEntry
			}
			_, err = h.m.store.InstallStaged(stage, opts)
			require.NoError(t, err)
			require.NoError(t, packages.ValidateKnownPackageOrigin(h.root, "sample"))
			backend := filepath.Join(h.root, "sample", "extension")
			file, err := os.OpenFile(backend, os.O_WRONLY|os.O_APPEND, 0)
			require.NoError(t, err)
			_, err = file.WriteString("changed after trusted installation")
			require.NoError(t, err)
			require.NoError(t, file.Close())
			requestEnabledAccept(t, h.m, "sample", "0", h.sha, true)
			waitActiveOpDone(t, h.m)
			result := lifecycleResult(t, h.m.Observe("sample"))
			require.NotNil(t, result.Error)
			require.Equal(t, packages.LifecycleErrUnavailable, result.Error.Code)
			require.Zero(t, readObserveCount(controllerStateDir(h)), "no controller observation may precede known-origin validation")
			require.Zero(t, readActivateCount(controllerStateDir(h)))
			intent, err := packages.LoadIntent(h.root, "sample")
			require.NoError(t, err)
			require.True(t, intent.DesiredEnabled)
			require.Equal(t, "1", intent.Generation)
			require.NotNil(t, intent.PendingOperation)
			require.Equal(t, packages.IntentActivate, *intent.PendingOperation)
		})
	}
}

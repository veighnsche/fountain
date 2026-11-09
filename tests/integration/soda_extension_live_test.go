// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	extension "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"
	"forgejo.org/modules/setting"
	web_extensions "forgejo.org/routers/web/extensions"
	extensions_service "forgejo.org/services/extensions"
	"forgejo.org/tests"

	"github.com/stretchr/testify/require"
)

// This development check is opt-in because it runs the current Soda binaries
// from the neighboring checkout. It exercises their real private HTTP listener
// and Forgejo's admitted extension process without installing either service.
//
//nolint:usetesting // Keep Unix socket paths short or retain the external fixture root.
func TestSodaExtensionLiveBridge(t *testing.T) {
	backend, dashboard, artifactRoot := os.Getenv("SODA_C03_BACKEND"), os.Getenv("SODA_C03_DASHBOARD"), os.Getenv("SODA_C03_ARTIFACT_DIR")
	if backend == "" || dashboard == "" || artifactRoot == "" || os.Getenv("SODA_C03_DATABASE_DSN_FILE") == "" {
		t.Skip("set SODA_C03_BACKEND, SODA_C03_DASHBOARD, SODA_C03_ARTIFACT_DIR, and SODA_C03_DATABASE_DSN_FILE for the live development check")
	}
	defer tests.PrepareTestEnv(t)()
	// The in-process Fountain router and the real Soda private listener
	// share one HTTPS browser origin. Set it before login so cookie jars,
	// Fountain admission and Soda mutation validation use the same origin.
	previousAppURL := setting.AppURL
	appURL, err := url.Parse(previousAppURL)
	require.NoError(t, err)
	appURL.Scheme = "https"
	setting.AppURL = appURL.String()
	t.Cleanup(func() { setting.AppURL = previousAppURL })
	for _, binary := range []string{backend, dashboard} {
		info, err := os.Stat(binary)
		require.NoError(t, err)
		require.NotZero(t, info.Mode()&0o111)
	}
	require.NoError(t, os.MkdirAll(artifactRoot, 0o700))
	root, err := os.MkdirTemp(artifactRoot, "c-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	socket := filepath.Join(root, "s")
	factoryBinary := os.Getenv("SODA_C03_FACTORY")
	factorySocket := filepath.Join(root, "f")

	packageDir := filepath.Join(root, "p", "soda")
	require.NoError(t, os.MkdirAll(filepath.Join(packageDir, "assets"), 0o700))
	backendLog := filepath.Join(root, "backend.log")
	run := "#!/bin/sh\nexec " + shellQuoteC03(backend) + " --package-id soda --soda-socket " + shellQuoteC03(socket) + " --factory-socket " + shellQuoteC03(factorySocket) + " 2>" + shellQuoteC03(backendLog) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "run"), []byte(run), 0o700))
	manifest := extension.Manifest{
		Protocol: extension.Protocol, ID: "soda", Name: "Soda", Version: "0.1.0", Executable: "run", Capabilities: []string{extension.CapabilityActorRead, extension.CapabilityServiceBridge, extension.CapabilityContributionAuthorize, extension.CapabilityExtensionLifecycle}, Policies: []string{extension.PolicyForgejoUsername},
		Pages:  []extension.Page{{ID: "spaces", Title: "Spaces", Scope: "global", Entry: "spaces.js"}},
		Panels: []extension.Panel{{ID: "workspace", Title: "Spaces", Entry: "workspace.js"}},
	}
	if factoryBinary != "" {
		manifest.Capabilities = append(manifest.Capabilities, extension.CapabilityRepositoryRead, extension.CapabilityRuntimeControl)
		previous := setting.OAuth2.FactoryAppliancePackageID
		setting.OAuth2.FactoryAppliancePackageID = "soda"
		t.Cleanup(func() { setting.OAuth2.FactoryAppliancePackageID = previous })
	}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), encoded, 0o600))
	for _, name := range []string{"spaces.js", "workspace.js"} {
		require.NoError(t, os.WriteFile(filepath.Join(packageDir, "assets", name), []byte("export function mount() {}\n"), 0o600))
	}
	// Ordinary admission opens only after durable activation: record the
	// next-start desired state through the shared package store before
	// the manager starts, or every page lookup refuses as not found.
	packageStore, err := packages.OpenPackageStore(filepath.Join(root, "p"))
	require.NoError(t, err)
	_, err = packageStore.SetDesired("soda", true)
	require.NoError(t, err)
	require.NoError(t, packageStore.Close())
	manager := extensions_service.NewManager(filepath.Join(root, "p"))
	seedSodaExtensionOperatorID(t, filepath.Join(root, "p"))
	serviceCallback := filepath.Join(root, "h")
	require.NoError(t, manager.SetCallbackHandlerFactory(web_extensions.CallbackHandlerForInstance))
	require.NoError(t, manager.SetServiceCallbackEndpoint(serviceCallback, web_extensions.CallbackHandlerForService()))
	if err := manager.Start(context.Background()); err != nil {
		logContents, _ := os.ReadFile(backendLog)
		t.Fatalf("Soda extension process did not start: %v: %s", err, string(logContents))
	}
	previous := extensions_service.GetManager()
	extensions_service.SetDefault(manager)
	// Optional packages start on the bounded async pass after Start
	// returns core readiness: await the soda effect instead of
	// assuming synchronous startup.
	deadline := time.Now().Add(25 * time.Second)
	for {
		if _, _, ok := manager.Lookup("soda"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Logf("live bridge Observe(soda): %+v", manager.Observe("soda"))
			t.Fatalf("live bridge soda package did not start within 25s")
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Cleanup(func() { extensions_service.SetDefault(previous); require.NoError(t, manager.Close()) })

	owner := loginUser(t, "user2")
	other := loginUser(t, "user4")
	startSodaExtensionService(t, root, socket, dashboard, serviceCallback)
	if factoryBinary != "" {
		factoryRoot := filepath.Join(root, "factory")
		require.NoError(t, os.Mkdir(factoryRoot, 0o700))
		startSodaExtensionService(t, factoryRoot, factorySocket, factoryBinary, serviceCallback)
	}
	generation := func(session *TestSession) string {
		response := session.MakeRequest(t, NewRequest(t, http.MethodGet, "/-/native/session"), http.StatusNoContent)
		value := response.Header().Get(extension.SessionGenerationHeader)
		require.Len(t, value, 43)
		return value
	}
	ownerGeneration, otherGeneration := generation(owner), generation(other)
	pageBase := "/-/native/pages/soda/spaces/api/"
	panelBase := "/-/native/panels/soda/workspace/api/"
	origin, err := url.Parse(setting.AppURL)
	require.NoError(t, err)
	requestAt := func(base string, session *TestSession, method, endpoint, generation string, body any, status int) map[string]any {
		var req *RequestWrapper
		if body == nil {
			req = NewRequest(t, method, base+endpoint)
		} else {
			req = NewRequestWithJSON(t, method, base+endpoint, body)
		}
		req.Header.Set(extension.SessionGenerationHeader, generation)
		req.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		response := session.MakeRequest(t, req, status)
		require.Equal(t, status, response.Code)
		if status != http.StatusOK {
			return nil
		}
		var value map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &value))
		return value
	}
	request := func(session *TestSession, method, endpoint, generation string, body any, status int) map[string]any {
		return requestAt(pageBase, session, method, endpoint, generation, body, status)
	}
	panelRequest := func(session *TestSession, method, endpoint, generation string, body any, status int) map[string]any {
		return requestAt(panelBase, session, method, endpoint, generation, body, status)
	}
	if factoryBinary != "" {
		identity := request(owner, http.MethodPost, "factory/native-repositories/resolve", ownerGeneration, map[string]string{"repository": "2"}, http.StatusOK)
		repository := identity["repository"].(string)
		path := "repositories/" + repository + "/factory/controller"
		view := request(owner, http.MethodGet, path, ownerGeneration, nil, http.StatusOK)
		require.Equal(t, true, view["can_select"])
		placement := view["placement"].(map[string]any)
		command := map[string]any{"command_id": "19191919191919191919191919191961", "expected_revision": placement["revision"], "action": "activate", "not_after": time.Now().Unix() + 120, "installation_id": view["installation_id"], "runtime_id": view["runtime_id"]}
		receipt := request(owner, http.MethodPost, path, ownerGeneration, command, http.StatusOK)
		require.Equal(t, "applied", receipt["outcome"])
		require.Equal(t, receipt, request(owner, http.MethodPost, path, ownerGeneration, command, http.StatusOK))
		request(other, http.MethodPost, path, otherGeneration, command, http.StatusNotFound)
		request(owner, http.MethodGet, path, otherGeneration, nil, http.StatusConflict)
		for i, action := range []string{"withdraw", "retire"} {
			command["command_id"] = []string{"19191919191919191919191919191962", "19191919191919191919191919191963"}[i]
			command["action"] = action
			command["expected_revision"] = receipt["placement"].(map[string]any)["revision"]
			receipt = request(owner, http.MethodPost, path, ownerGeneration, command, http.StatusOK)
			require.Equal(t, "applied", receipt["outcome"])
		}
		require.Equal(t, "inactive", receipt["placement"].(map[string]any)["state"])
		t.Log("PASS: actual canonical Factory service and Soda bridge with native human login, repository mapping, host controller selection/exact replay, other-actor and stale-session refusal, withdrawal and retirement; development fixture, no worker or installed claim")
	}

	ownerSession := request(owner, http.MethodGet, "session", ownerGeneration, nil, http.StatusOK)
	require.Equal(t, "user2", ownerSession["user"].(map[string]any)["login"])
	require.Equal(t, ownerGeneration, ownerSession["session_generation"])
	ownerWant := map[string]any{"display_name": "C03 owner"}
	otherWant := map[string]any{"display_name": "C03 other"}
	require.Equal(t, ownerWant, request(owner, http.MethodPatch, "me/preferences", ownerGeneration, map[string]string{"display_name": "C03 owner"}, http.StatusOK))
	require.Equal(t, ownerWant, request(owner, http.MethodGet, "me/preferences", ownerGeneration, nil, http.StatusOK))
	foreignOrigin := NewRequestWithJSON(t, http.MethodPatch, pageBase+"me/preferences", map[string]string{"display_name": "foreign"})
	foreignOrigin.Header.Set(extension.SessionGenerationHeader, ownerGeneration)
	foreignOrigin.Header.Set("Origin", "https://foreign.invalid")
	owner.MakeRequest(t, foreignOrigin, http.StatusForbidden)
	require.Equal(t, ownerWant, request(owner, http.MethodGet, "me/preferences", ownerGeneration, nil, http.StatusOK))

	forged := NewRequestWithJSON(t, http.MethodPatch, pageBase+"me/preferences", map[string]string{"display_name": "C03 other"})
	forged.Header.Set(extension.SessionGenerationHeader, otherGeneration)
	forged.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
	forged.Header.Set("Sec-Fetch-Site", "same-origin")
	forged.Header.Set(extension.ContextHeader, `{"extension_id":"soda","actor":{"id":"2","username":"user2","site_admin":true},"contribution":{"id":"spaces","kind":"page","scope":"global","action":"get"}}`)
	forged.Header.Set(extension.AdmissionHeader, strings.Repeat("x", 43))
	response := other.MakeRequest(t, forged, http.StatusOK)
	require.JSONEq(t, `{"display_name":"C03 other"}`, response.Body.String())
	require.Equal(t, ownerWant, request(owner, http.MethodGet, "me/preferences", ownerGeneration, nil, http.StatusOK))
	require.Equal(t, otherWant, request(other, http.MethodGet, "me/preferences", otherGeneration, nil, http.StatusOK))
	request(other, http.MethodGet, "session", ownerGeneration, nil, http.StatusConflict)
	// Unrouted methods never reach the origin gate: the backend's route
	// allowlist refuses them before dispatch.
	request(other, http.MethodDelete, "me/preferences", otherGeneration, nil, http.StatusNotFound)
	require.Equal(t, otherWant, request(other, http.MethodGet, "me/preferences", otherGeneration, nil, http.StatusOK))

	// Even a caller that can reach the private listener cannot supply a valid
	// Forgejo admission or change the action carried by that admission.
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	privateRequest, err := http.NewRequest(http.MethodPatch, "http://soda-extension-service/api/me/preferences", strings.NewReader(`{"display_name":"forged"}`))
	require.NoError(t, err)
	privateRequest.Header.Set("Content-Type", "application/json")
	privateRequest.Header.Set(extension.ContextHeader, `{"extension_id":"soda","instance_id":"forged","session_generation":"forged","contribution":{"id":"spaces","kind":"page","scope":"global","action":"get"},"actor":{"id":"2","username":"user2","site_admin":true}}`)
	privateRequest.Header.Set(extension.AdmissionHeader, strings.Repeat("x", 43))
	privateResponse, err := client.Do(privateRequest)
	require.NoError(t, err)
	require.NoError(t, privateResponse.Body.Close())
	require.Equal(t, http.StatusForbidden, privateResponse.StatusCode)
	require.Equal(t, ownerWant, request(owner, http.MethodGet, "me/preferences", ownerGeneration, nil, http.StatusOK))

	panelSession := panelRequest(owner, http.MethodGet, "session", ownerGeneration, nil, http.StatusOK)
	require.Equal(t, "user2", panelSession["user"].(map[string]any)["login"])
	require.Equal(t, ownerGeneration, panelSession["session_generation"])
	panelWant := map[string]any{"display_name": "C03 panel"}
	require.Equal(t, panelWant, panelRequest(owner, http.MethodPatch, "me/preferences", ownerGeneration, map[string]string{"display_name": "C03 panel"}, http.StatusOK))
	ownerWant = panelWant
	require.Equal(t, ownerWant, panelRequest(owner, http.MethodGet, "me/preferences", ownerGeneration, nil, http.StatusOK))
	require.Equal(t, ownerWant, request(owner, http.MethodGet, "me/preferences", ownerGeneration, nil, http.StatusOK))
}

func startSodaExtensionService(t *testing.T, root, socket, dashboard, serviceCallback string) {
	t.Helper()
	origin, err := url.Parse(setting.AppURL)
	require.NoError(t, err)
	startSodaExtensionServiceWithHost(t, root, socket, dashboard, serviceCallback, filepath.Join(root, "unused-host.sock"), origin.Scheme+"://"+origin.Host)
}

func seedSodaExtensionOperatorID(t *testing.T, managerRoot string) {
	t.Helper()
	path := filepath.Join(managerRoot, ".data", "soda", "operator-id")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("1\n"), 0o600))
}

func startSodaExtensionServiceWithHost(t *testing.T, root, socket, dashboard, serviceCallback, hostSocket, forgejoURL string) {
	t.Helper()
	// The current dashboard contract (Soda internal/config, strict keys)
	// requires a PostgreSQL DSN file plus an absolute host socket path.
	// The obsolete sqlite `database` file and the oauth/grant-key secrets
	// no longer exist and are refused. The DSN file is a restricted
	// fixture input: its contents never touch argv, logs, or source.
	dsnFile := os.Getenv("SODA_C03_DATABASE_DSN_FILE")
	require.NotEmpty(t, dsnFile, "set SODA_C03_DATABASE_DSN_FILE to the restricted PostgreSQL DSN-file fixture input")
	require.True(t, filepath.IsAbs(dsnFile), "SODA_C03_DATABASE_DSN_FILE must be an absolute path")
	port, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listen := port.Addr().String()
	require.NoError(t, port.Close())
	config := map[string]any{
		"listen": listen, "forgejo_url": forgejoURL, "forgejo_internal_url": "http://127.0.0.1:3000",
		"database_dsn_file": dsnFile, "host_socket": hostSocket,
		"factory_publication_root": filepath.Join(root, "publication"),
		"operator_id":              1,
	}
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	configPath := filepath.Join(root, "dashboard.json")
	require.NoError(t, os.WriteFile(configPath, encoded, 0o600))
	logFile, err := os.Create(filepath.Join(root, "dashboard.log"))
	require.NoError(t, err)
	command := exec.Command(dashboard, "--config", configPath, "--extension-socket", socket)
	command.Env = append(os.Environ(), extension.ServiceCallbackEnv+"="+serviceCallback)
	command.Stdout, command.Stderr = logFile, logFile
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = logFile.Close()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		connection, dialErr := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if dialErr == nil {
			require.NoError(t, connection.Close())
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	contents, _ := os.ReadFile(filepath.Join(root, "dashboard.log"))
	t.Fatalf("Soda private listener did not start: %s", string(contents))
}

func shellQuoteC03(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"forgejo.org/models/db"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	extension "forgejo.org/extension-sdk"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/desktopgrant"
	"forgejo.org/models/extensionauth"
	native_model "forgejo.org/models/nativeoperation"
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
		manifest.Capabilities = append(manifest.Capabilities, extension.CapabilityRepositoryRead, extension.CapabilityRuntimeControl, extension.CapabilityBackgroundOperations)
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
	if factoryBinary != "" {
		require.NoError(t, manager.SetServiceBridgePeers(map[uint32]string{uint32(os.Getuid()): "soda"}))
	}
	seedSodaExtensionOperatorID(t, filepath.Join(root, "p"))
	serviceCallback := filepath.Join(root, "h")
	require.NoError(t, manager.SetCallbackHandlerFactory(web_extensions.CallbackHandlerForInstance))
	require.NoError(t, manager.SetServiceCallbackEndpoint(serviceCallback, web_extensions.BackgroundMux(manager, web_extensions.CallbackHandlerForService(), "", true)))
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
		role := &auth_model.AccessToken{UID: 2, Name: "Q19 finite role fixture", Scope: auth_model.AccessTokenScopeWriteRepository, ResourceAllRepos: true}
		require.NoError(t, auth_model.NewAccessToken(t.Context(), role))
		installation := view["installation_id"].(string)
		_, err := extensionauth.EnrollBinding(t.Context(), installation, role.ID, 2, 2, extensionauth.KindLifecycleNote)
		require.NoError(t, err)
		view = request(owner, http.MethodGet, path, ownerGeneration, nil, http.StatusOK)
		require.Len(t, view["actors"], 1)
		grantCommand := map[string]any{"command_id": "19191919191919191919191919191964", "expected_placement": receipt["placement"].(map[string]any)["revision"], "generation": receipt["placement"].(map[string]any)["generation"], "operations": []string{extensionauth.KindLifecycleNote}, "not_after": time.Now().Unix() + 120, "installation_id": installation, "runtime_id": view["runtime_id"], "actor_id": "2", "token_id": strconv.FormatInt(role.ID, 10)}
		grant := request(owner, http.MethodPost, path+"/operations", ownerGeneration, grantCommand, http.StatusOK)
		require.Equal(t, true, grant["active"])
		require.Equal(t, grant, request(owner, http.MethodPost, path+"/operations", ownerGeneration, grantCommand, http.StatusOK))
		grantJournal, err := native_model.LookupOperation(t.Context(), installation, grant["id"].(string))
		require.NoError(t, err)
		require.NotNil(t, grantJournal)
		require.EqualValues(t, 2, grantJournal.ActorID)
		require.Equal(t, "runtime.controller.authorize", grantJournal.Kind)
		require.Equal(t, grant["id"], grantJournal.ControllerGrantID)
		use := extensionauth.SubmissionRequest{InstallationID: installation, PackageID: "soda", ActorID: 2, RepositoryID: 2, TokenSecret: role.Token, Kind: extensionauth.KindLifecycleNote, ControllerGrantID: grant["id"].(string), RequireController: true}
		_, err = extensionauth.VerifySubmission(t.Context(), use)
		require.NoError(t, err)
		use.Kind = extensionauth.KindExecutionLaunch
		_, err = extensionauth.VerifySubmission(t.Context(), use)
		require.Error(t, err, "a note binding/grant must not admit worker execution")
		grantCommand["command_id"] = "19191919191919191919191919191965"
		grantCommand["operations"] = []string{extensionauth.KindExecutionLaunch}
		request(owner, http.MethodPost, path+"/operations", ownerGeneration, grantCommand, http.StatusServiceUnavailable)
		_, err = desktopgrant.GetOperationGrant(t.Context(), grantCommand["command_id"].(string))
		require.ErrorIs(t, err, desktopgrant.ErrNotFound, "refused scope must roll back the inserted grant")
		_, err = extensionauth.EnrollBinding(t.Context(), installation, role.ID, 2, 2, extensionauth.KindExecutionLaunch)
		require.NoError(t, err)
		grantCommand["command_id"] = "19191919191919191919191919191966"
		executionGrant := request(owner, http.MethodPost, path+"/operations", ownerGeneration, grantCommand, http.StatusOK)
		use.ControllerGrantID = executionGrant["id"].(string)
		_, err = extensionauth.VerifySubmission(t.Context(), use)
		require.NoError(t, err)
		background, err := extension.BootstrapServiceBackground(t.Context(), extension.ServiceBridgeOptions{SocketPath: serviceCallback, ExpectedHostUID: uint32(os.Getuid()), InstallationID: installation})
		require.NoError(t, err)
		controller, ok := background.(extension.ControllerBackground)
		require.True(t, ok)
		credential := filepath.Join(root, "role.credential")
		require.NoError(t, os.WriteFile(credential, []byte(role.Token), 0o600))
		executionID := executionGrant["id"].(string)
		meta, err := controller.ReadControllerGrant(t.Context(), executionID)
		require.NoError(t, err)
		require.True(t, meta.Active)
		claimRequest := extension.ExecutionClaimRequest{CommandID: "19191919191919191919191919191971", ManifestDigest: strings.Repeat("a", 64), NotAfter: time.Now().Unix() + 60}
		claim, err := controller.ClaimControllerExecution(t.Context(), extension.CredentialFile(credential), executionID, claimRequest)
		require.NoError(t, err)
		require.True(t, claim.Acquired)
		replayClaim, err := controller.ClaimControllerExecution(t.Context(), extension.CredentialFile(credential), executionID, claimRequest)
		require.NoError(t, err)
		require.False(t, replayClaim.Acquired)
		require.Equal(t, claim.ManifestDigest, replayClaim.ManifestDigest)
		readClaim, err := controller.ReadControllerExecution(t.Context(), executionID, claimRequest.CommandID)
		require.NoError(t, err)
		require.False(t, readClaim.Acquired)
		role.Scope = auth_model.AccessTokenScopeReadRepository
		_, err = db.GetEngine(t.Context()).ID(role.ID).Cols("scope").Update(role)
		require.NoError(t, err)
		meta, err = controller.ReadControllerGrant(t.Context(), executionID)
		require.NoError(t, err)
		require.False(t, meta.Active)
		refusedClaim := claimRequest
		refusedClaim.CommandID = "19191919191919191919191919191972"
		_, err = controller.ClaimControllerExecution(t.Context(), extension.CredentialFile(credential), executionID, refusedClaim)
		require.Error(t, err)
		_, err = desktopgrant.GetExecutionClaim(t.Context(), refusedClaim.CommandID)
		require.ErrorIs(t, err, desktopgrant.ErrNotFound)
		role.Scope = auth_model.AccessTokenScopeWriteRepository
		_, err = db.GetEngine(t.Context()).ID(role.ID).Cols("scope").Update(role)
		require.NoError(t, err)
		// Metadata-only fixture: no native dispatch method is called. These
		// owner declarations exercise API fencing, not native retirement proof.
		proof := sha256.Sum256([]byte("owned fixture sent no native work: " + claimRequest.CommandID))
		retired, err := controller.RetireControllerExecution(t.Context(), executionID, claimRequest.CommandID, extension.ExecutionRetirement{NotAfter: claimRequest.NotAfter, ManifestDigest: claimRequest.ManifestDigest, State: "not_started", ReceiptDigest: hex.EncodeToString(proof[:])})
		require.NoError(t, err)
		require.Equal(t, "not_started", retired.State)
		missing := claimRequest
		missing.CommandID = "19191919191919191919191919191973"
		missing.NotAfter = meta.NotAfter + 1
		_, err = controller.ReadControllerExecution(t.Context(), executionID, missing.CommandID)
		require.Error(t, err)
		_, err = controller.RetireControllerExecution(t.Context(), executionID, missing.CommandID, extension.ExecutionRetirement{NotAfter: missing.NotAfter, ManifestDigest: missing.ManifestDigest, State: "not_started", ReceiptDigest: hex.EncodeToString(proof[:])})
		require.NoError(t, err)
		fenced, err := controller.ClaimControllerExecution(t.Context(), extension.CredentialFile(credential), executionID, missing)
		require.NoError(t, err)
		require.False(t, fenced.Acquired)
		require.Equal(t, "not_started", fenced.State)
		actor := map[string]any{"token_id": strconv.FormatInt(role.ID, 10), "actor_id": identity["actor"]}
		policy := map[string]any{"expected_revision": 0, "enabled": false, "paused": false, "target_branch": "refs/heads/main", "roles": map[string]any{"soda-coder": map[string]any{"harness": "muse", "harness_version": "fixture-1", "model": "fixture-config"}, "soda-reviewer": map[string]any{"harness": "muse", "harness_version": "fixture-1", "model": "fixture-config"}}, "required_checks": []string{"fixture-ci"}, "merge_method": "merge", "publish_actor": actor, "create_actor": actor, "review_actor": actor, "merge_actor": actor, "intent_minutes": 0}
		policyPath := "repositories/" + repository + "/factory/policy"
		request(owner, http.MethodPut, policyPath, ownerGeneration, policy, http.StatusOK)
		status := request(owner, http.MethodGet, "repositories/"+repository+"/factory", ownerGeneration, nil, http.StatusOK)
		grantCommand["command_id"] = "19191919191919191919191919191967"
		grantCommand["policy_revision"] = status["policy"].(map[string]any)["revision"]
		selectedGrant := request(owner, http.MethodPost, path+"/operations", ownerGeneration, grantCommand, http.StatusOK)
		require.Equal(t, "applied", selectedGrant["selection_outcome"])
		selectedStatus := request(owner, http.MethodGet, "repositories/"+repository+"/factory", ownerGeneration, nil, http.StatusOK)
		selectedPolicy := selectedStatus["policy"].(map[string]any)
		require.Equal(t, selectedGrant["id"], selectedPolicy["execution_actor"].(map[string]any)["controller_grant_id"])
		request(owner, http.MethodPost, path+"/operations", ownerGeneration, grantCommand, http.StatusOK)
		replayedStatus := request(owner, http.MethodGet, "repositories/"+repository+"/factory", ownerGeneration, nil, http.StatusOK)
		require.Equal(t, selectedPolicy["revision"], replayedStatus["policy"].(map[string]any)["revision"])
		policy["expected_revision"] = selectedPolicy["revision"]
		policy["paused"] = true
		policy["execution_actor"] = map[string]any{"token_id": actor["token_id"], "actor_id": actor["actor_id"], "controller_grant_id": selectedGrant["id"]}
		request(owner, http.MethodPut, policyPath, ownerGeneration, policy, http.StatusOK)
		grantCommand["command_id"] = "19191919191919191919191919191968"
		grantCommand["policy_revision"] = selectedPolicy["revision"]
		partial := request(owner, http.MethodPost, path+"/operations", ownerGeneration, grantCommand, http.StatusOK)
		require.Equal(t, "refused", partial["selection_outcome"])
		_, err = desktopgrant.GetOperationGrant(t.Context(), partial["id"].(string))
		require.NoError(t, err, "completed host authorization must survive policy refusal")
		changed := request(owner, http.MethodGet, "repositories/"+repository+"/factory", ownerGeneration, nil, http.StatusOK)["policy"].(map[string]any)
		require.Equal(t, true, changed["paused"])
		require.Equal(t, selectedGrant["id"], changed["execution_actor"].(map[string]any)["controller_grant_id"])
		delete(grantCommand, "policy_revision")
		grantCommand["command_id"] = "19191919191919191919191919191966"
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
		_, err = extensionauth.VerifySubmission(t.Context(), use)
		require.Error(t, err, "withdrawal must fence role grant use")
		replayed := request(owner, http.MethodPost, path+"/operations", ownerGeneration, grantCommand, http.StatusOK)
		require.Equal(t, false, replayed["active"])
		require.Equal(t, executionGrant["not_after"], replayed["not_after"])
		require.NoError(t, auth_model.DeleteAccessTokenByID(t.Context(), role.ID, 2))
		replayed = request(owner, http.MethodPost, path+"/operations", ownerGeneration, grantCommand, http.StatusOK)
		require.Equal(t, false, replayed["active"])
		require.Equal(t, executionGrant["id"], replayed["id"])
		t.Log("PASS: actual canonical Factory/Soda services, native human login, controller/grant replay, private background claim acquisition/duplicate/token-scope refusal/terminal no-start fence, explicit policy selection and concurrent human policy preservation, withdrawal and retirement; metadata fixture without native dispatch, worker retirement or installed qualification")
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

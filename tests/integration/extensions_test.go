// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	packages "forgejo.org/modules/extensions"
	"forgejo.org/modules/setting"
	web_extensions "forgejo.org/routers/web/extensions"
	app_context "forgejo.org/services/context"
	extensions_service "forgejo.org/services/extensions"
	repo_service "forgejo.org/services/repository"
	"forgejo.org/tests"

	ws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/websocket"
)

func TestExtensionNativeAuthentication(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	// Native documents use the host's normal sign-in redirect, including
	// requests carrying Basic credentials instead of a browser session.
	for _, path := range []string{"/workspace", "/workspace/new", "/workspace/requests/d0123456789abcdef01234567", "/feed", "/applications", "/tailnet", "/user/settings/identity", "/admin/workspace", "/user2/repo1/workspace", "/requests", "/user/settings/development-keys", "/admin/sodaos", "/user2/repo1/requests/new", "/user2/repo1/terminals", "/user2/repo1/issues/1/factory", "/user2/repo1/settings/sodaos"} {
		t.Run(path, func(t *testing.T) {
			for _, request := range []*RequestWrapper{NewRequest(t, http.MethodGet, path), NewRequest(t, http.MethodGet, path).AddBasicAuth("user2")} {
				response := MakeRequest(t, request, http.StatusSeeOther)
				require.Equal(t, setting.AppSubURL+"/user/login", response.Header().Get("Location"))
			}
		})
	}
	// Private native session and API endpoints reject missing browser authority
	// directly, without redirecting a programmatic request to an HTML login page.
	for _, path := range []string{"/-/native/session", "/-/native/panels/pages/status/api/state", "/-/native/pages/pages/global/api/action"} {
		t.Run(path, func(t *testing.T) {
			for _, request := range []*RequestWrapper{NewRequest(t, http.MethodGet, path), NewRequest(t, http.MethodGet, path).AddBasicAuth("user2")} {
				response := MakeRequest(t, request, http.StatusUnauthorized)
				require.Empty(t, response.Header().Get("Location"))
			}
		})
	}
	session := loginUser(t, "user2")
	session.MakeRequest(t, NewRequest(t, http.MethodGet, "/workspace"), http.StatusNotFound)
	session.MakeRequest(t, NewRequest(t, http.MethodGet, "/workspace/new"), http.StatusNotFound)
	session.MakeRequest(t, NewRequest(t, http.MethodGet, "/workspace/requests/d0123456789abcdef01234567"), http.StatusNotFound)
	session.MakeRequest(t, NewRequest(t, http.MethodGet, "/admin/workspace"), http.StatusForbidden)
	// The former public extension namespace is not kept as a route alias.
	session.MakeRequest(t, NewRequest(t, http.MethodGet, "/-/extensions/pages/pages/page"), http.StatusNotFound)
	session.MakeRequest(t, NewRequest(t, http.MethodGet, "/-/extensions/workspace"), http.StatusNotFound)
	request := NewRequest(t, http.MethodPost, "/-/native/pages/pages/global/api/action")
	request.Header.Set("Origin", "https://foreign.invalid")
	session.MakeRequest(t, request, http.StatusForbidden)
}

func TestNativeSessionWebSocketThroughLoginAndLogout(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	session := loginUser(t, "user2")
	cookie := session.GetCookie(setting.SessionConfig.CookieName)
	require.NotNil(t, cookie)

	server := httptest.NewTLSServer(testWebRoutes)
	defer server.Close()
	config, err := websocket.NewConfig("wss"+strings.TrimPrefix(server.URL, "https")+"/-/native/test/session-stream", server.URL)
	require.NoError(t, err)
	config.TlsConfig = server.Client().Transport.(*http.Transport).TLSClientConfig
	config.Header.Set("Cookie", cookie.String())
	conn, err := websocket.DialConfig(config)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
	require.NoError(t, websocket.Message.Send(conn, "before-logout"))
	var echoed string
	require.NoError(t, websocket.Message.Receive(conn, &echoed))
	require.Equal(t, "before-logout", echoed)

	// Logout uses Forgejo's normal CSRF and session destruction path while the
	// upgraded request still holds its original session response lifecycle.
	session.MakeRequest(t, NewRequest(t, http.MethodPost, "/user/logout"), http.StatusOK)
	require.NoError(t, websocket.Message.Send(conn, "after-logout"))
	require.NoError(t, websocket.Message.Receive(conn, &echoed))
	require.Equal(t, "after-logout", echoed)
	require.NoError(t, conn.Close())

	request := NewRequest(t, http.MethodGet, "/-/native/test/session-stream")
	request.AddCookie(cookie)
	MakeRequest(t, request, http.StatusUnauthorized)
}

// The existing test binary is also the disposable installed SDK extension. This
// branch runs before the integration TestMain, without opening the host database.
func init() {
	if os.Getenv(extension.SocketEnv) == "" {
		return
	}
	var transportStarted, transportCanceled atomic.Bool
	if err := extension.Serve(extension.Application{Policies: testUsernamePolicies(), AuthorizeContribution: func(_ context.Context, request extension.ContributionRequest) (extension.ContributionDecision, error) {
		if request.Contribution.Kind == "panel" && request.Contribution.ID == "status" && request.ActorID == "4" {
			return extension.ContributionDecision{Allowed: false, DenialCode: "test_policy"}, nil
		}
		return extension.ContributionDecision{Allowed: true}, nil
	}, HTTP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authority, err := extension.RequestContext(r)
		if err != nil {
			http.Error(w, "authority", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/transport/") {
			actor, err := authority.Native().CurrentActor(r.Context())
			if err != nil {
				http.Error(w, "native callback", http.StatusForbidden)
				return
			}
			switch r.URL.Path {
			case "/transport/path":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path, "query": r.URL.RawQuery, "actor_id": actor.ID})
				return
			case "/transport/stalled":
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("waiting"))
				if err := http.NewResponseController(w).Flush(); err != nil {
					return
				}
				transportStarted.Store(true)
				<-r.Context().Done()
				transportCanceled.Store(true)
				return
			case "/transport/state":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]bool{"started": transportStarted.Load(), "canceled": transportCanceled.Load()})
				return
			}
		}
		if r.URL.Path == "/headers" {
			if _, err := authority.Native().CurrentActor(r.Context()); err != nil {
				http.Error(w, "native callback", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(r.Header)
			return
		}
		if r.URL.Path == "/redirect-stream" {
			http.Redirect(w, r, "/stream", http.StatusFound)
			return
		}
		if r.URL.Path == "/stream" {
			if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Sec-WebSocket-Protocol") != "" || r.Header.Get(extension.SessionGenerationHeader) != "" {
				http.Error(w, "browser credential leaked", http.StatusForbidden)
				return
			}
			if _, err := authority.Native().CurrentActor(r.Context()); err != nil {
				http.Error(w, "native callback", http.StatusForbidden)
				return
			}
			conn, err := ws.Accept(w, r, &ws.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer conn.CloseNow()
			for {
				kind, body, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				if err := conn.Write(r.Context(), kind, body); err != nil {
					return
				}
			}
		}
		if r.URL.Path == "/native" {
			actor, err := authority.Native().CurrentActor(r.Context())
			if err != nil {
				http.Error(w, "native callback", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(actor)
			return
		}
		if r.URL.Path == "/native/repository" {
			repository, err := authority.Native().Repository(r.Context(), r.URL.Query().Get("id"))
			if err != nil {
				http.Error(w, "native callback", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(repository)
			return
		}
		if r.URL.Path == "/native/repositories" {
			limit := 10
			if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
				limit, err = strconv.Atoi(rawLimit)
				if err != nil || limit < 1 || limit > 100 {
					http.Error(w, "invalid limit", http.StatusBadRequest)
					return
				}
			}
			page, err := authority.Native().SearchOwnedRepositories(r.Context(), r.URL.Query().Get("query"), r.URL.Query().Get("cursor"), limit)
			if err != nil {
				http.Error(w, "native callback", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(page)
			return
		}
		if r.URL.Path == "/native/organization-owner" {
			owner, err := authority.Native().OrganizationOwner(r.Context(), r.URL.Query().Get("name"))
			if err != nil {
				http.Error(w, "native callback", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(owner)
			return
		}
		if r.URL.Path == "/native/public-keys" {
			keys, err := authority.Native().PublicSSHKeys(r.Context())
			if err != nil {
				http.Error(w, "native callback", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(keys)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authority)
	})}); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func assertExtensionPanelsHome(t *testing.T, response *httptest.ResponseRecorder, generation string, presented bool) *HTMLDoc {
	t.Helper()
	require.Len(t, generation, 43)
	require.Equal(t, generation, response.Header().Get(extension.SessionGenerationHeader))
	doc := NewHTMLParser(t, bytes.NewBuffer(response.Body.Bytes()))
	home := doc.Find("[data-extension-panels]")
	require.Equal(t, 1, home.Length(), "one retained panels home must be rendered")
	doc.AssertAttrEqual(t, "[data-extension-panels]", "data-extension-session-generation", generation)
	require.Equal(t, !presented, home.Is("[hidden]"), "only a shared-owner page presents the panels home")
	require.Equal(t, !presented, home.Is("[inert]"), "sheltered panels must be inert")
	return doc
}

//nolint:usetesting // Keep Unix socket paths short or retain the external fixture root.
func TestExtensionNativePermissions(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	root, err := os.MkdirTemp("", "fe-")
	require.NoError(t, err)
	defer os.RemoveAll(root)
	packageDir := filepath.Join(root, "pages")
	require.NoError(t, os.MkdirAll(filepath.Join(packageDir, "assets"), 0o700))
	executable, err := os.Executable()
	require.NoError(t, err)
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "run"), []byte(script), 0o700))
	manifest := extension.Manifest{Protocol: extension.Protocol, ID: "pages", Name: "Demo", Version: "1", Executable: "run", PreferredWorkspace: true, Capabilities: []string{
		extension.CapabilityActorRead,
		extension.CapabilityRepositoryRead,
		extension.CapabilityOwnedRepositoriesSearch,
		extension.CapabilityOrganizationOwnership,
		extension.CapabilityPublicKeysRead,
		extension.CapabilityContributionAuthorize,
	}, Pages: []extension.Page{
		{ID: "global", Title: "Global", Scope: "global", Route: "workspace", Routes: []extension.PageRoute{{Route: "requests", Title: "Requests"}}, Entry: "main.js"},
		{ID: "user", Title: "User", Scope: "user", Route: "identity", Routes: []extension.PageRoute{{Route: "development-keys", Title: "Development keys"}}, Permission: "user", Entry: "main.js"},
		{ID: "site-admin", Title: "Site admin", Scope: "admin", Route: "workspace", Routes: []extension.PageRoute{{Route: "sodaos", Title: "SodaOS"}}, Permission: "admin", Entry: "main.js"},
		// One repository page owns several named routes (one page identity).
		{ID: "read", Title: "Read", Scope: "repository", Permission: "read", Entry: "main.js", Route: "feed", Routes: []extension.PageRoute{
			{Route: "requests", Title: "Requests"}, {Route: "app", Title: "App"}, {Route: "terminals", Title: "Terminals", Persist: true},
			{Route: "knowledge", Title: "Knowledge", Hidden: true}, {Route: "issue-factory", Title: "Factory status"},
		}, Subroutes: []extension.Subroute{{Route: "requests", Pattern: "new", Title: "New request"}, {Route: "requests", Pattern: "{request}", Title: "Request"}}},
		{ID: "write", Title: "Write", Scope: "repository", Permission: "write", Entry: "main.js"},
		{ID: "admin", Title: "Admin", Scope: "repository", Permission: "admin", Entry: "main.js", Route: "settings", Subroutes: []extension.Subroute{{Pattern: "factory", Title: "Factory settings"}}},
	}}
	manifest.Panels = []extension.Panel{{ID: "status", Title: "Status", Entry: "main.js"}}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), encoded, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "assets", "main.js"), []byte("export function mount() {}"), 0o600))
	enableIntegrationExtension(t, root, manifest.ID)
	manager := extensions_service.NewManager(root)
	require.NoError(t, manager.SetCallbackHandlerFactory(web_extensions.CallbackHandlerForInstance))
	require.NoError(t, manager.Start(context.Background()))
	require.Eventually(t, func() bool { return len(manager.List()) == 1 }, 10*time.Second, 10*time.Millisecond)
	previous := extensions_service.GetManager()
	extensions_service.SetDefault(manager)
	defer func() { extensions_service.SetDefault(previous); require.NoError(t, manager.Close()) }()
	owner := loginUser(t, "user2")
	reader := loginUser(t, "user4")
	repoOwner, err := user_model.GetUserByName(db.DefaultContext, "user2")
	require.NoError(t, err)
	for _, name := range []string{"pagination-01", "pagination-02", "pagination-03"} {
		_, err := repo_service.CreateRepository(db.DefaultContext, repoOwner, repoOwner, repo_service.CreateRepoOptions{Name: name})
		require.NoError(t, err)
	}
	getGeneration := func(session *TestSession) string {
		response := session.MakeRequest(t, NewRequest(t, http.MethodGet, "/-/native/session"), http.StatusNoContent)
		generation := response.Header().Get(extension.SessionGenerationHeader)
		require.Len(t, generation, 43)
		return generation
	}
	ownerGeneration := getGeneration(owner)
	readerGeneration := getGeneration(reader)
	apiRequest := func(session *TestSession, generation, method, path string, status int) *httptest.ResponseRecorder {
		request := NewRequest(t, method, path)
		request.Header.Set(extension.SessionGenerationHeader, generation)
		return session.MakeRequest(t, request, status)
	}
	for _, page := range []string{"read", "write", "admin"} {
		apiRequest(owner, ownerGeneration, http.MethodGet, "/user2/repo1/-/native/pages/pages/"+page+"/api/context", http.StatusOK)
	}
	apiRequest(reader, readerGeneration, http.MethodGet, "/user2/repo1/-/native/pages/pages/read/api/context", http.StatusOK)
	reader.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo1/-/native/pages/pages/write/api/context"), http.StatusNotFound)
	reader.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo1/-/native/pages/pages/admin/api/context"), http.StatusNotFound)
	reader.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo2/-/native/pages/pages/read/api/context"), http.StatusNotFound)
	ownerResponse := apiRequest(owner, ownerGeneration, http.MethodGet, "/user2/repo1/-/native/pages/pages/read/api/context", http.StatusOK)
	var ownerAuthority extension.Authority
	require.NoError(t, json.Unmarshal(ownerResponse.Body.Bytes(), &ownerAuthority))
	require.NotEmpty(t, ownerAuthority.InstanceID)
	require.Len(t, ownerAuthority.SessionGeneration, 43)
	request := NewRequest(t, http.MethodGet, "/user2/repo1/-/native/pages/pages/read/api/context")
	request.Header.Set(extension.ContextHeader, `{"actor":{"id":1,"site_admin":true}}`)
	request.Header.Set(extension.AdmissionHeader, "browser-forged-admission")
	request.Header.Set(extension.SessionGenerationHeader, readerGeneration)
	response := reader.MakeRequest(t, request, http.StatusOK)
	var authority extension.Authority
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &authority))
	require.Equal(t, "4", authority.Actor.ID)
	require.False(t, authority.Actor.SiteAdmin)
	require.Equal(t, "read", authority.Repository.Permission)
	require.Equal(t, ownerAuthority.InstanceID, authority.InstanceID)
	require.NotEqual(t, ownerAuthority.SessionGeneration, authority.SessionGeneration)
	readerCookie := reader.GetCookie(setting.SessionConfig.CookieName)
	require.NotNil(t, readerCookie)
	require.NotContains(t, response.Body.String(), readerCookie.Value)
	require.NotContains(t, response.Body.String(), "browser-forged-admission")
	callbackResponse := apiRequest(reader, readerGeneration, http.MethodGet, "/user2/repo1/-/native/pages/pages/read/api/native", http.StatusOK)
	var callbackActor extension.Actor
	require.NoError(t, json.Unmarshal(callbackResponse.Body.Bytes(), &callbackActor))
	require.Equal(t, extension.Actor{ID: "4", Username: "user4"}, callbackActor)
	repositoryResponse := apiRequest(reader, readerGeneration, http.MethodGet, "/user2/repo1/-/native/pages/pages/read/api/native/repository?id="+ownerAuthority.Repository.ID, http.StatusOK)
	var repository extension.Repository
	require.NoError(t, json.Unmarshal(repositoryResponse.Body.Bytes(), &repository))
	require.Equal(t, ownerAuthority.Repository.ID, repository.ID)
	require.Equal(t, ownerAuthority.Repository.Owner, repository.Owner)
	require.Equal(t, ownerAuthority.Repository.Name, repository.Name)
	require.Equal(t, "read", repository.Permission)
	repoTwoResponse := apiRequest(owner, ownerGeneration, http.MethodGet, "/user2/repo2/-/native/pages/pages/read/api/context", http.StatusOK)
	var repoTwoAuthority extension.Authority
	require.NoError(t, json.Unmarshal(repoTwoResponse.Body.Bytes(), &repoTwoAuthority))
	apiRequest(reader, readerGeneration, http.MethodGet, "/user2/repo1/-/native/pages/pages/read/api/native/repository?id="+repoTwoAuthority.Repository.ID, http.StatusForbidden)
	apiRequest(reader, ownerGeneration, http.MethodGet, "/user2/repo1/-/native/pages/pages/read/api/context", http.StatusConflict)
	reader.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo1/-/native/pages/pages/read/api/context"), http.StatusConflict)
	repositoryModel, err := repo_model.GetRepositoryByOwnerAndName(db.DefaultContext, "user2", "repo1")
	require.NoError(t, err)
	repositoryModel.IsPrivate = true
	require.NoError(t, repo_model.UpdateRepositoryCols(db.DefaultContext, repositoryModel, "is_private"))
	apiRequest(reader, readerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/repository?id="+ownerAuthority.Repository.ID, http.StatusForbidden)
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/workspace"), http.StatusOK)
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/user/settings/identity"), http.StatusOK)
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/admin/workspace"), http.StatusForbidden)
	ownerWorkspaceRequest := NewRequest(t, http.MethodGet, "/workspace")
	ownerWorkspaceRequest.Header.Set("Sec-Fetch-Dest", "document")
	ownerWorkspace := owner.MakeRequest(t, ownerWorkspaceRequest, http.StatusOK)
	ownerDocument := assertExtensionPanelsHome(t, ownerWorkspace, ownerGeneration, false)
	ownerDocument.AssertElement(t, `[data-extension-panel-id="status"]`, true)
	readerWorkspaceRequest := NewRequest(t, http.MethodGet, "/workspace")
	readerWorkspaceRequest.Header.Set("Sec-Fetch-Dest", "document")
	readerWorkspace := reader.MakeRequest(t, readerWorkspaceRequest, http.StatusOK)
	readerDocument := assertExtensionPanelsHome(t, readerWorkspace, readerGeneration, false)
	readerDocument.AssertElement(t, `[data-extension-panel-id="status"]`, false)
	// NFX-A02 region_adopt: ordinary native pages render as plain documents
	// with the hidden persistent panels home, never a parallel shell.
	plainRequest := NewRequest(t, http.MethodGet, "/explore/repos")
	plainRequest.Header.Set("Sec-Fetch-Dest", "document")
	plainResponse := owner.MakeRequest(t, plainRequest, http.StatusOK)
	plainGeneration := plainResponse.Header().Get(extension.SessionGenerationHeader)
	require.Equal(t, ownerGeneration, plainGeneration)
	plainDocument := assertExtensionPanelsHome(t, plainResponse, plainGeneration, false)
	plainDocument.AssertElement(t, `[data-extension-panel-id="status"]`, true)
	plainDocument.AssertElement(t, "[data-extension-page]", false)
	apiRequest(owner, ownerGeneration, http.MethodGet, "/-/native/panels/pages/status/api/context", http.StatusOK)
	apiRequest(reader, readerGeneration, http.MethodGet, "/-/native/panels/pages/status/api/context", http.StatusNotFound)
	siteAdmin := loginUser(t, "user1")
	siteAdmin.MakeRequest(t, NewRequest(t, http.MethodGet, "/admin/workspace"), http.StatusOK)
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/-/native/assets/pages/main.js"), http.StatusOK)
	repoWorkspace := owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo1/workspace"), http.StatusOK)
	repoModel, err := repo_model.GetRepositoryByOwnerAndName(db.DefaultContext, "user2", "repo1")
	require.NoError(t, err)
	require.Contains(t, repoWorkspace.Body.String(), `data-extension-page-id="global"`)
	require.Contains(t, repoWorkspace.Body.String(), `data-extension-repository-id="`+strconv.FormatInt(repoModel.ID, 10)+`"`)
	require.Contains(t, repoWorkspace.Body.String(), `data-extension-api-base="/-/native/pages/pages/global/api/"`)
	// Named repository routes all render the same page identity, so the
	// host readopts one retained root while moving between them.
	for path, title := range map[string]string{
		"/user2/repo1/feed": "Read", "/user2/repo1/requests": "Requests", "/user2/repo1/requests/new": "New request",
		"/user2/repo1/requests/abc": "Request", "/user2/repo1/app": "App", "/user2/repo1/terminals": "Terminals",
		"/user2/repo1/knowledge": "Knowledge", "/user2/repo1/issues/1/factory": "Factory status",
	} {
		body := owner.MakeRequest(t, NewRequest(t, http.MethodGet, path), http.StatusOK).Body.String()
		require.Contains(t, body, `data-extension-page-id="read"`, path)
		require.Contains(t, body, `data-extension-api-base="/user2/repo1/-/native/pages/pages/read/api/"`, path)
		require.Contains(t, body, `<h1 id="extension-page-title" class="ui dividing header">`+title+`</h1>`, path)
		require.Equal(t, path == "/user2/repo1/terminals", strings.Contains(body, "data-extension-persist"), path)
		require.NotContains(t, body, "repository settings extensions", path)
	}
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo1/feed/unknown"), http.StatusNotFound)
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo1/requests/a/b"), http.StatusNotFound)
	// Native issue routes are not shadowed by the issue factory slot.
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo1/issues/1"), http.StatusOK)
	repoHome := owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo1"), http.StatusOK).Body.String()
	for _, href := range []string{"/user2/repo1/feed", "/user2/repo1/requests", "/user2/repo1/terminals"} {
		require.Contains(t, repoHome, `href="`+href+`"`)
	}
	require.NotContains(t, repoHome, `href="/user2/repo1/settings/sodaos"`)
	require.NotContains(t, repoHome, `href="/user2/repo1/knowledge"`)
	require.NotContains(t, repoHome, `href="/user2/repo1/issues/{index}/factory"`)
	// The repository settings slot renders in the native settings layout.
	for path, title := range map[string]string{"/user2/repo1/settings/sodaos": "Admin", "/user2/repo1/settings/sodaos/factory": "Factory settings"} {
		body := owner.MakeRequest(t, NewRequest(t, http.MethodGet, path), http.StatusOK).Body.String()
		require.Contains(t, body, "repository settings extensions", path)
		require.Contains(t, body, `class="active item" href="/user2/repo1/settings/sodaos">Admin</a>`, path)
		require.Contains(t, body, title, path)
	}
	require.Contains(t, owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/user2/repo1/settings"), http.StatusOK).Body.String(), `href="/user2/repo1/settings/sodaos"`)
	// Global, user and admin slots.
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/requests"), http.StatusOK)
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/user/settings/development-keys"), http.StatusOK)
	owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/admin/sodaos"), http.StatusForbidden)
	siteAdmin.MakeRequest(t, NewRequest(t, http.MethodGet, "/admin/sodaos"), http.StatusOK)
	owner.MakeRequest(t, NewRequestWithValues(t, http.MethodPost, "/org/create", map[string]string{
		"org_name":                      "native_callback_owner",
		"visibility":                    "0",
		"repo_admin_change_team_access": "on",
	}), http.StatusSeeOther)
	ownerPage := apiRequest(owner, ownerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/repositories?query=repo1", http.StatusOK)
	var ownerRepositories extension.RepositoryPage
	require.NoError(t, json.Unmarshal(ownerPage.Body.Bytes(), &ownerRepositories))
	foundOwnedRepository := false
	for _, repository := range ownerRepositories.Items {
		require.Equal(t, "user2", repository.Owner)
		if repository.Name == "repo1" {
			foundOwnedRepository = true
		}
	}
	require.True(t, foundOwnedRepository)
	firstPageResponse := apiRequest(owner, ownerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/repositories?query=pagination&limit=2", http.StatusOK)
	var firstPage extension.RepositoryPage
	require.NoError(t, json.Unmarshal(firstPageResponse.Body.Bytes(), &firstPage))
	require.Len(t, firstPage.Items, 2)
	require.Equal(t, "pagination-01", firstPage.Items[0].Name)
	require.Equal(t, "pagination-02", firstPage.Items[1].Name)
	require.NotEmpty(t, firstPage.NextCursor)
	secondPageResponse := apiRequest(owner, ownerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/repositories?query=pagination&limit=2&cursor="+firstPage.NextCursor, http.StatusOK)
	var secondPage extension.RepositoryPage
	require.NoError(t, json.Unmarshal(secondPageResponse.Body.Bytes(), &secondPage))
	require.Len(t, secondPage.Items, 1)
	require.Equal(t, "pagination-03", secondPage.Items[0].Name)
	require.Empty(t, secondPage.NextCursor)
	readerPage := apiRequest(reader, readerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/repositories?query=repo1", http.StatusOK)
	var readerRepositories extension.RepositoryPage
	require.NoError(t, json.Unmarshal(readerPage.Body.Bytes(), &readerRepositories))
	require.Empty(t, readerRepositories.Items)
	ownerCheck := apiRequest(owner, ownerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/organization-owner?name=native_callback_owner", http.StatusOK)
	var ownsOrganization bool
	require.NoError(t, json.Unmarshal(ownerCheck.Body.Bytes(), &ownsOrganization))
	require.True(t, ownsOrganization)
	readerCheck := apiRequest(reader, readerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/organization-owner?name=native_callback_owner", http.StatusOK)
	require.NoError(t, json.Unmarshal(readerCheck.Body.Bytes(), &ownsOrganization))
	require.False(t, ownsOrganization)
	missingOrganization := apiRequest(reader, readerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/organization-owner?name=native_callback_absent", http.StatusOK)
	require.NoError(t, json.Unmarshal(missingOrganization.Body.Bytes(), &ownsOrganization))
	require.False(t, ownsOrganization)
	ownerKeysResponse := apiRequest(owner, ownerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/public-keys", http.StatusOK)
	var ownerKeys []extension.PublicKey
	require.NoError(t, json.Unmarshal(ownerKeysResponse.Body.Bytes(), &ownerKeys))
	require.Len(t, ownerKeys, 1)
	readerKeysResponse := apiRequest(reader, readerGeneration, http.MethodGet, "/-/native/pages/pages/global/api/native/public-keys", http.StatusOK)
	var readerKeys []extension.PublicKey
	require.NoError(t, json.Unmarshal(readerKeysResponse.Body.Bytes(), &readerKeys))
	require.Empty(t, readerKeys)
}

//nolint:usetesting // Keep Unix socket paths short or retain the external fixture root.
func TestExtensionPreferredWorkspace(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	root, err := os.MkdirTemp("", "fe-")
	require.NoError(t, err)
	defer os.RemoveAll(root)
	packageDir := filepath.Join(root, "preferred")
	require.NoError(t, os.MkdirAll(filepath.Join(packageDir, "assets"), 0o700))
	executable, err := os.Executable()
	require.NoError(t, err)
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "run"), []byte(script), 0o700))
	manifest := extension.Manifest{
		Protocol: extension.Protocol, ID: "preferred", Name: "Preferred", Version: "1", Executable: "run", PreferredWorkspace: true,
		Capabilities: []string{extension.CapabilityContributionAuthorize},
		Pages:        []extension.Page{{ID: "home", Title: "Home", Scope: "global", Route: "workspace", Entry: "main.js", Subroutes: []extension.Subroute{{Pattern: "new", Title: "New Project"}, {Pattern: "requests/{request}", Title: "Build request"}}}},
		Panels:       []extension.Panel{{ID: "status", Title: "Status", Entry: "status.js"}},
	}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), encoded, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "assets", "main.js"), []byte("export function mount() {}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "assets", "status.js"), []byte("export function mount() {}"), 0o600))
	enableIntegrationExtension(t, root, manifest.ID)
	manager := extensions_service.NewManager(root)
	require.NoError(t, manager.Start(context.Background()))
	require.Eventually(t, func() bool { return len(manager.List()) == 1 }, 10*time.Second, 10*time.Millisecond)
	previous := extensions_service.GetManager()
	extensions_service.SetDefault(manager)
	defer func() {
		extensions_service.SetDefault(previous)
		if manager != nil {
			require.NoError(t, manager.Close())
		}
	}()
	owner := loginUser(t, "user2")
	document := func(path string) *RequestWrapper {
		request := NewRequest(t, http.MethodGet, path)
		request.Header.Set("Sec-Fetch-Dest", "document")
		return request
	}
	owner.SetCookie(&http.Cookie{Name: app_context.CookieNameFlash, Value: url.Values{"success": {"Native workspace flash survives"}}.Encode(), Path: "/"})
	page := owner.MakeRequest(t, document("/workspace"), http.StatusOK)
	generation := page.Header().Get(extension.SessionGenerationHeader)
	require.Len(t, generation, 43)
	require.Contains(t, page.Body.String(), "Native workspace flash survives")
	// An independent page owns its mount; its unrelated retained panels stay
	// sheltered, including when the page occupies the workspace route.
	pageDocument := assertExtensionPanelsHome(t, page, generation, false)
	pageDocument.AssertElement(t, `[data-extension-page-id="home"]`, true)
	pageDocument.AssertElement(t, `[data-extension-panel-id="status"]`, true)
	pageDocument.AssertAttrEqual(t, "[data-extension-page]", "data-extension-contract-version", "1")
	pageDocument.AssertAttrEqual(t, "[data-extension-panel]", "data-extension-contract-version", "1")
	// NFX-A02 region_adopt: ordinary native pages render as plain documents
	// with the hidden persistent panels home even with a
	// preferred-workspace extension installed.
	plainResponse := owner.MakeRequest(t, document("/explore/repos?sort=updated"), http.StatusOK)
	plainGeneration := plainResponse.Header().Get(extension.SessionGenerationHeader)
	require.Equal(t, generation, plainGeneration)
	plainDocument := assertExtensionPanelsHome(t, plainResponse, plainGeneration, false)
	plainDocument.AssertElement(t, `[data-extension-panel-id="status"]`, true)
	plainDocument.AssertElement(t, "[data-extension-page]", false)
	require.NotContains(t, plainResponse.Body.String(), ">Extensions</")
	require.Empty(t, plainResponse.Header().Get("Location"))

	// An iframe request receives the original Forgejo page. It does not gain
	// the panels home merely because its URL is eligible.
	iframeRequest := NewRequest(t, http.MethodGet, "/explore/repos?sort=updated")
	iframeRequest.Header.Set("Sec-Fetch-Dest", "iframe")
	iframeResponse := owner.MakeRequest(t, iframeRequest, http.StatusOK)
	require.NotContains(t, iframeResponse.Body.String(), `data-extension-panels`)

	// Account-state pages retain their own document even when the panels
	// home is preferred for ordinary browsing.
	securityResponse := owner.MakeRequest(t, document("/user/settings/security"), http.StatusOK)
	require.NotContains(t, securityResponse.Body.String(), `data-extension-panels`)
	require.Empty(t, securityResponse.Header().Get(extension.SessionGenerationHeader))
	unauthenticatedSettings := NewRequest(t, http.MethodGet, "/user/settings/security")
	unauthenticatedSettings.Header.Set("Sec-Fetch-Dest", "document")
	MakeRequest(t, unauthenticatedSettings, http.StatusSeeOther)

	for _, path := range []string{"/workspace?view=projects", "/user2/repo1/workspace?view=projects", "/workspace/new", "/workspace/requests/d0123456789abcdef01234567"} {
		direct := owner.MakeRequest(t, document(path), http.StatusOK)
		directDocument := assertExtensionPanelsHome(t, direct, generation, false)
		directDocument.AssertElement(t, "[data-extension-page]", true)
		directDocument.AssertElement(t, "iframe", false)
		directDocument.AssertElement(t, "nav#navbar", true)
		directDocument.AssertElement(t, "#navbar-logo", true)
		require.Positive(t, directDocument.Find(`a[href^="/user/settings"]`).Length())
		directDocument.AssertElement(t, `[data-extension-panel-id="status"]`, true)
		require.Equal(t, generation, direct.Header().Get(extension.SessionGenerationHeader))
		fetchRequest := NewRequest(t, http.MethodGet, path)
		fetchRequest.Header.Set("Sec-Fetch-Dest", "empty")
		fetchRequest.Header.Set("Accept", "text/html")
		fetchRequest.Header.Set("X-Extension-Workspace-Navigation", "1")
		fetched := owner.MakeRequest(t, fetchRequest, http.StatusOK)
		fetchedDocument := assertExtensionPanelsHome(t, fetched, generation, false)
		fetchedDocument.AssertElement(t, "iframe", false)
		require.Equal(t, generation, fetched.Header().Get(extension.SessionGenerationHeader))
		if strings.HasPrefix(path, "/user2/") {
			directDocument.AssertElement(t, ".repo-header", true)
			directDocument.AssertAttrEqual(t, "[data-extension-panels]", "data-extension-repository-id", "1")
		}
	}
	for path, title := range map[string]string{"/workspace/new": "New Project", "/workspace/requests/d0123456789abcdef01234567": "Build request"} {
		aliased := owner.MakeRequest(t, document(path), http.StatusOK)
		require.Contains(t, aliased.Body.String(), title)
		require.Equal(t, generation, aliased.Header().Get(extension.SessionGenerationHeader))
	}
	// The host admits declared parameters opaquely (NFX-C04/H-ROUTE);
	// product-ID validation belongs to the consumer backend, so this
	// generic page renders for a value Soda would later reject.
	owner.MakeRequest(t, document("/workspace/requests/not-an-id"), http.StatusOK)
	owner.MakeRequest(t, document("/workspace/unknown"), http.StatusNotFound)
	// Blocker 2: a matched route rendering the HTML error template shares
	// the footer but must not mount contributions.
	html404Request := document("/workspace/unknown")
	html404Request.Header.Set("Accept", "text/html")
	html404 := owner.MakeRequest(t, html404Request, http.StatusNotFound)
	require.Contains(t, html404.Body.String(), `class="error-code"`)
	require.NotContains(t, html404.Body.String(), `data-extension-panels`)
	require.NotContains(t, html404.Body.String(), `data-extension-page`)
	require.NotContains(t, html404.Body.String(), `data-extension-contract-version`)
	// The platform strips trailing slashes before routing, so the slashed
	// alias serves the same canonical page.
	slashed := owner.MakeRequest(t, document("/workspace/new/"), http.StatusOK)
	assertExtensionPanelsHome(t, slashed, generation, false)
	require.Contains(t, slashed.Body.String(), "New Project")
	owner.MakeRequest(t, document("/workspace/requests/"), http.StatusNotFound)
	// The same segments under a repository path stay product-controlled: they
	// do not resolve to the global workspace contribution. The plain 404
	// carries no HTML home or page root, but Navigation middleware ran so
	// the generation envelope is present.
	product := owner.MakeRequest(t, document("/user2/repo1/workspace/new"), http.StatusNotFound)
	require.NotContains(t, product.Body.String(), `data-extension-panels`)
	require.NotContains(t, product.Body.String(), `data-extension-page`)
	require.Equal(t, generation, product.Header().Get(extension.SessionGenerationHeader))
	check := owner.MakeRequest(t, NewRequest(t, http.MethodGet, "/-/native/session"), http.StatusNoContent)
	require.Equal(t, generation, check.Header().Get(extension.SessionGenerationHeader))
	require.Empty(t, check.Body.String())
	other := loginUser(t, "user4")
	otherCheck := other.MakeRequest(t, NewRequest(t, http.MethodGet, "/-/native/session"), http.StatusNoContent)
	require.NotEqual(t, generation, otherCheck.Header().Get(extension.SessionGenerationHeader))
	MakeRequest(t, NewRequest(t, http.MethodGet, "/-/native/session"), http.StatusUnauthorized)
	require.NoError(t, manager.Close())
	manager = nil
	store, err := packages.OpenPackageStore(root)
	require.NoError(t, err)
	_, err = store.SetDesired(manifest.ID, false)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	disabledManager := extensions_service.NewManager(root)
	require.NoError(t, disabledManager.Start(context.Background()))
	manager = disabledManager
	extensions_service.SetDefault(disabledManager)
	disabledRequest := NewRequest(t, http.MethodGet, "/explore/repos")
	disabledRequest.Header.Set("Sec-Fetch-Dest", "document")
	require.NotContains(t, owner.MakeRequest(t, disabledRequest, http.StatusOK).Body.String(), `data-extension-panels`)
}

// NFX-A04/H-C: a declared shared owner composes through the native shell
// as one retained panel instance. The page slot emits no vacant competing
// mount on global or repository workspace documents, while native chrome
// (navbar, repository header, notices) stays in the document.
//
//nolint:usetesting // Keep Unix socket paths short or retain the external fixture root.
func TestExtensionSharedOwnerComposition(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	root, err := os.MkdirTemp("", "fe-")
	require.NoError(t, err)
	defer os.RemoveAll(root)
	packageDir := filepath.Join(root, "shared")
	require.NoError(t, os.MkdirAll(filepath.Join(packageDir, "assets"), 0o700))
	executable, err := os.Executable()
	require.NoError(t, err)
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "run"), []byte(script), 0o700))
	manifest := extension.Manifest{
		Protocol: extension.Protocol, ID: "shared", Name: "Shared", Version: "1", Executable: "run", PreferredWorkspace: true,
		Capabilities: []string{extension.CapabilityContributionAuthorize},
		Pages:        []extension.Page{{ID: "spaces", Title: "Workspace", Scope: "global", Route: "workspace", Entry: "main.js", Owner: "workspace"}},
		Panels:       []extension.Panel{{ID: "workspace", Title: "Workspace", Entry: "main.js", Owner: "workspace"}},
	}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), encoded, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "assets", "main.js"), []byte("export function mount() {}"), 0o600))
	enableIntegrationExtension(t, root, manifest.ID)
	manager := extensions_service.NewManager(root)
	require.NoError(t, manager.Start(context.Background()))
	require.Eventually(t, func() bool { return len(manager.List()) == 1 }, 10*time.Second, 10*time.Millisecond)
	previous := extensions_service.GetManager()
	extensions_service.SetDefault(manager)
	defer func() {
		extensions_service.SetDefault(previous)
		require.NoError(t, manager.Close())
	}()
	owner := loginUser(t, "user2")
	document := func(path string) *RequestWrapper {
		request := NewRequest(t, http.MethodGet, path)
		request.Header.Set("Sec-Fetch-Dest", "document")
		return request
	}
	for _, path := range []string{"/workspace", "/user2/repo1/workspace"} {
		page := owner.MakeRequest(t, document(path), http.StatusOK)
		generation := page.Header().Get(extension.SessionGenerationHeader)
		require.Len(t, generation, 43)
		doc := assertExtensionPanelsHome(t, page, generation, true)
		doc.AssertElement(t, `[data-extension-panel-id="workspace"]`, true)
		doc.AssertAttrEqual(t, "[data-extension-panel]", "data-extension-owner-id", "workspace")
		doc.AssertAttrEqual(t, "[data-extension-panel]", "data-extension-api-base", "/-/native/pages/shared/spaces/api/")
		doc.AssertElement(t, ".extension-page-owner-presented", true)
		doc.AssertElement(t, "[data-extension-page]", false)
		doc.AssertElement(t, "nav#navbar", true)
		if strings.HasPrefix(path, "/user2/") {
			doc.AssertElement(t, ".repo-header", true)
			doc.AssertAttrEqual(t, "[data-extension-panels]", "data-extension-repository-id", "1")
		}
	}
}

func extractBodyPageData(t *testing.T, body string) map[string]any {
	t.Helper()
	const prefix = `data-page-data="`
	start := strings.Index(body, prefix)
	require.NotEqual(t, -1, start, "page must carry machine-readable pageData on <body>")
	rest := body[start+len(prefix):]
	end := strings.Index(rest, `"`)
	require.NotEqual(t, -1, end, "pageData attribute must terminate")
	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(html.UnescapeString(rest[:end])), &data))
	return data
}

// NFX-A02 slice 1: every native document carries its pageData as
// machine-readable JSON on <body> so workspace transitions can refresh the
// live config from the entered document. Mirrors what getAttribute returns.
//
//nolint:usetesting // Keep Unix socket paths short or retain the external fixture root.
func TestExtensionPageDataEmbed(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	root, err := os.MkdirTemp("", "fe-")
	require.NoError(t, err)
	defer os.RemoveAll(root)
	packageDir := filepath.Join(root, "preferred")
	require.NoError(t, os.MkdirAll(filepath.Join(packageDir, "assets"), 0o700))
	executable, err := os.Executable()
	require.NoError(t, err)
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "run"), []byte(script), 0o700))
	manifest := extension.Manifest{
		Protocol: extension.Protocol, ID: "preferred", Name: "Preferred", Version: "1", Executable: "run", PreferredWorkspace: true,
		Capabilities: []string{extension.CapabilityContributionAuthorize},
		Pages:        []extension.Page{{ID: "home", Title: "Home", Scope: "global", Route: "workspace", Entry: "main.js"}},
	}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), encoded, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "assets", "main.js"), []byte("export function mount() {}"), 0o600))
	enableIntegrationExtension(t, root, manifest.ID)
	manager := extensions_service.NewManager(root)
	require.NoError(t, manager.Start(context.Background()))
	require.Eventually(t, func() bool { return len(manager.List()) == 1 }, 10*time.Second, 10*time.Millisecond)
	previous := extensions_service.GetManager()
	extensions_service.SetDefault(manager)
	defer func() { extensions_service.SetDefault(previous); require.NoError(t, manager.Close()) }()
	owner := loginUser(t, "user1")
	document := func(path string) *RequestWrapper {
		request := NewRequest(t, http.MethodGet, path)
		request.Header.Set("Sec-Fetch-Dest", "document")
		return request
	}
	direct := owner.MakeRequest(t, document("/workspace"), http.StatusOK)
	directData := extractBodyPageData(t, direct.Body.String())
	require.Equal(t, "preferred", directData["extension"].(map[string]any)["id"])
	require.Contains(t, directData, "apiBase")
	explore := owner.MakeRequest(t, document("/explore/repos"), http.StatusOK)
	exploreHTML := explore.Body.String()
	require.Contains(t, exploreHTML, `data-extension-panels`)
	require.NotNil(t, extractBodyPageData(t, exploreHTML))
	plain := owner.MakeRequest(t, document("/admin/users"), http.StatusOK)
	plainHTML := plain.Body.String()
	require.Contains(t, plainHTML, `data-extension-panels`)
	require.NotNil(t, extractBodyPageData(t, plainHTML))
	// P1: every eligible shape carries the same authoritative generation so
	// transplants verify before joining entered HTML to owners.
	directGeneration := direct.Header().Get(extension.SessionGenerationHeader)
	require.Len(t, directGeneration, 43)
	require.Equal(t, directGeneration, explore.Header().Get(extension.SessionGenerationHeader))
	require.Equal(t, directGeneration, plain.Header().Get(extension.SessionGenerationHeader))
	assertExtensionPanelsHome(t, explore, directGeneration, false)
	assertExtensionPanelsHome(t, plain, directGeneration, false)
}

func enableIntegrationExtension(t *testing.T, root, id string) {
	t.Helper()
	store, err := packages.OpenPackageStore(root)
	require.NoError(t, err)
	_, err = store.SetDesired(id, true)
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

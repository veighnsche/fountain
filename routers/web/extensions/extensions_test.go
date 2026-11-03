// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/perm"
	access "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/services/auth"
	"forgejo.org/services/context"

	"code.forgejo.org/go-chi/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sessionUID struct {
	session.Store
	uid any
	id  string
}

func (s sessionUID) Get(key any) any {
	if key == "uid" {
		return s.uid
	}
	return nil
}

func (s sessionUID) ID() string { return s.id }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testContext(req *http.Request) (*context.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	return &context.Context{Base: &context.Base{Req: req, Resp: context.WrapResponseWriter(recorder)}, Doer: &user_model.User{ID: 42}, Authentication: &auth.UnauthenticatedResult{}}, recorder
}

func TestNativeSessionAuthority(t *testing.T) {
	ctx, _ := testContext(httptest.NewRequest("GET", "http://forgejo/-/native/pages/demo/page", nil))
	require.False(t, nativeSession(ctx), "an authenticated actor without a native session is insufficient")
	ctx.Session = sessionUID{uid: int64(42)}
	require.True(t, nativeSession(ctx))
	ctx.Session = sessionUID{uid: int64(43)}
	require.False(t, nativeSession(ctx), "session identity must match selected actor")
	ctx.Session = sessionUID{uid: int64(42)}
	ctx.Req.Header.Set("Authorization", "Basic credential")
	require.False(t, nativeSession(ctx))
	ctx.Req.Header.Del("Authorization")
	ctx.Req.URL.RawQuery = "access_token=credential"
	require.False(t, nativeSession(ctx))
}

func TestSessionGenerationTracksNativeSessionAndActor(t *testing.T) {
	previousSecret := setting.SecretKey
	setting.SecretKey = "test-only-extension-secret"
	t.Cleanup(func() { setting.SecretKey = previousSecret })

	ctx, _ := testContext(httptest.NewRequest(http.MethodGet, "http://forgejo/-/native/session", nil))
	ctx.Session = sessionUID{uid: int64(42), id: "native-session-a"}
	first, err := sessionGeneration(ctx)
	require.NoError(t, err)
	require.Len(t, first, 43)
	require.Equal(t, first, mustSessionGeneration(t, ctx))

	ctx.Session = sessionUID{uid: int64(42), id: "native-session-b"}
	require.NotEqual(t, first, mustSessionGeneration(t, ctx))

	ctx.Doer = &user_model.User{ID: 43}
	ctx.Session = sessionUID{uid: int64(43), id: "native-session-a"}
	require.NotEqual(t, first, mustSessionGeneration(t, ctx))

	ctx.Session = sessionUID{uid: int64(42), id: "native-session-a"}
	_, err = sessionGeneration(ctx)
	require.Error(t, err, "a session generation must not bind a different selected actor")
}

func mustSessionGeneration(t *testing.T, ctx *context.Context) string {
	t.Helper()
	generation, err := sessionGeneration(ctx)
	require.NoError(t, err)
	return generation
}

func TestRepositoryPagePermissions(t *testing.T) {
	ctx, _ := testContext(httptest.NewRequest("GET", "http://forgejo/", nil))
	ctx.Repo = &context.Repository{Repository: &repo_model.Repository{}, Permission: access.Permission{AccessMode: perm.AccessModeRead, UnitsMode: map[unit.Type]perm.AccessMode{unit.TypeCode: perm.AccessModeRead}}}
	p := extension.Page{Scope: "repository", Permission: "read"}
	require.True(t, allowed(ctx, p))
	p.Permission = "write"
	require.False(t, allowed(ctx, p))
	ctx.Repo.UnitsMode[unit.TypeCode] = perm.AccessModeWrite
	require.True(t, allowed(ctx, p))
	p.Permission = "admin"
	require.False(t, allowed(ctx, p))
	ctx.Repo.AccessMode = perm.AccessModeAdmin
	require.True(t, allowed(ctx, p))
	ctx.Repo.UnitsMode = map[unit.Type]perm.AccessMode{}
	p.Permission = "read"
	require.False(t, allowed(ctx, p))
	require.False(t, allowed(ctx, extension.Page{Scope: "admin"}))
}

func TestProxyReplacesAuthorityAndRemovesCredentials(t *testing.T) {
	req := httptest.NewRequest("POST", "http://forgejo/-/native/pages/demo/page/api/action", strings.NewReader("request"))
	req.Header.Set("Cookie", "session=private")
	req.Header.Set("Authorization", "Bearer private")
	req.Header.Set(extension.ContextHeader, `{"actor":{"id":1}}`)
	req.Header.Set(extension.AdmissionHeader, "browser-forged-admission")
	req.Header.Set("X-Forwarded-User", "admin")
	req.Header.Set(extension.SessionGenerationHeader, "browser-forged-generation")
	req.Header.Set("Origin", "http://forgejo")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	ctx, recorder := testContext(req)
	authority := extension.Authority{ExtensionID: "demo", SessionGeneration: strings.Repeat("g", 43), Contribution: extension.Contribution{ID: "page", Kind: "page", Scope: "global", Action: "post"}, Actor: extension.Actor{ID: "42", Username: "synthetic-user"}}
	proxy(ctx, roundTripFunc(func(out *http.Request) (*http.Response, error) {
		var actual extension.Authority
		err := json.Unmarshal([]byte(out.Header.Get(extension.ContextHeader)), &actual)
		require.NoError(t, err)
		assert.Equal(t, authority, actual)
		assert.Empty(t, out.Header.Get("Cookie"))
		assert.Empty(t, out.Header.Get("Authorization"))
		assert.Empty(t, out.Header.Get(extension.AdmissionHeader))
		assert.Empty(t, out.Header.Get("X-Forwarded-User"))
		assert.Equal(t, []string{authority.SessionGeneration}, out.Header.Values(extension.SessionGenerationHeader))
		assert.Equal(t, []string{"http://forgejo"}, out.Header.Values("Origin"))
		assert.Equal(t, []string{"same-origin"}, out.Header.Values("Sec-Fetch-Site"))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"session=evil"}, "Location": {"https://evil.invalid"}}, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
	}), authority)
	assert.Equal(t, 200, recorder.Code)
	assert.Equal(t, `{"ok":true}`, recorder.Body.String())
	assert.Empty(t, recorder.Header().Get("Set-Cookie"))
	assert.Empty(t, recorder.Header().Get("Location"))
}

func TestProxyUsesOnlyHostMintedAdmission(t *testing.T) {
	req := httptest.NewRequest("GET", "http://forgejo/", nil)
	req.Header.Set(extension.AdmissionHeader, "browser-forged-admission")
	ctx, recorder := testContext(req)
	proxyWithAdmission(ctx, roundTripFunc(func(out *http.Request) (*http.Response, error) {
		assert.Equal(t, "host-minted-admission", out.Header.Get(extension.AdmissionHeader))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}), extension.Authority{}, "host-minted-admission", req.Context())
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestWorkspaceRejectsForeignAndRecursiveURLs(t *testing.T) {
	for _, path := range []string{"https://evil.invalid/", "//evil.invalid/", "/\\evil.invalid/", "/-/native/session?path=/", "/user/login", "/user/recover_account", "/user/oauth2/provider", "/user/settings/security/two_factor/enroll", "/user/settings/applications", "/user/settings/keys", "/oauth2/authorize", "/openid", "/login/openid", "/install", "/repo?access_token=private", "/repo?PASSWORD=private", "/%2fevil.invalid/", "/%5cevil.invalid/"} {
		assert.False(t, validWorkspacePath(path), path)
	}
	assert.True(t, validWorkspacePath("/user2/repo1/issues"))
}

//nolint:nilnil // The fixture deliberately supplies no session or backend.
func TestProxyRejectsStreamingAndRedirects(t *testing.T) {
	authority := extension.Authority{ExtensionID: "demo", Contribution: extension.Contribution{Scope: "global"}}
	for _, header := range []string{"Upgrade", "Accept"} {
		req := httptest.NewRequest("GET", "http://forgejo/", nil)
		value := "websocket"
		if header == "Accept" {
			value = "text/event-stream"
		}
		req.Header.Set(header, value)
		ctx, recorder := testContext(req)
		proxy(ctx, roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("stream must not reach backend"); return nil, nil }), authority)
		assert.Equal(t, http.StatusNotImplemented, recorder.Code)
	}
	for _, test := range []struct {
		name        string
		status      int
		contentType string
	}{
		{"redirect", http.StatusFound, "text/plain"},
		{"unsolicited event stream", http.StatusOK, "text/event-stream"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, recorder := testContext(httptest.NewRequest("GET", "http://forgejo/", nil))
			proxy(ctx, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {test.contentType}}, Body: io.NopCloser(strings.NewReader("body"))}, nil
			}), authority)
			assert.Equal(t, http.StatusBadGateway, recorder.Code)
		})
	}
}

func TestWorkspaceStaysInsideSubURL(t *testing.T) {
	previous := setting.AppSubURL
	setting.AppSubURL = "/forge"
	defer func() { setting.AppSubURL = previous }()
	assert.True(t, validWorkspacePath("/forge/team/repo"))
	assert.False(t, validWorkspacePath("/forge/../outside"))
	assert.False(t, validWorkspacePath("/forge/%2e%2e/outside"))
	assert.False(t, validWorkspacePath("/forge/user/login"))
	assert.False(t, validWorkspacePath("/forge/team?client_secret=private"))
}

func TestWorkspaceDocumentRequiresBrowserDocumentRequest(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		want    bool
	}{
		{name: "fetch document", method: http.MethodGet, path: "/issues", headers: map[string]string{"Sec-Fetch-Dest": "document"}, want: true},
		{name: "legacy HTML navigation", method: http.MethodGet, path: "/issues", headers: map[string]string{"Accept": "text/html,application/xhtml+xml"}, want: true},
		{name: "direct Workspace fetch", method: http.MethodGet, path: "/workspace", headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "text/html", "X-Extension-Workspace-Navigation": "1"}, want: true},
		{name: "direct new-project fetch", method: http.MethodGet, path: "/workspace/new", headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "text/html", "X-Extension-Workspace-Navigation": "1"}, want: true},
		{name: "direct request fetch", method: http.MethodGet, path: "/workspace/requests/d0123456789abcdef01234567", headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "text/html", "X-Extension-Workspace-Navigation": "1"}, want: true},
		{name: "marked transplant fetch serves any eligible path", method: http.MethodGet, path: "/workspace/new/extra", headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "text/html", "X-Extension-Workspace-Navigation": "1"}, want: true},
		{name: "marked plain transplant fetch", method: http.MethodGet, path: "/admin/users", headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "text/html", "X-Extension-Workspace-Navigation": "1"}, want: true},
		{name: "unmarked fetch", method: http.MethodGet, path: "/workspace", headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "text/html"}},
		{name: "marked ordinary fetch", method: http.MethodGet, path: "/issues", headers: map[string]string{"Sec-Fetch-Dest": "empty", "Accept": "text/html", "X-Extension-Workspace-Navigation": "1"}, want: true},
		{name: "iframe", method: http.MethodGet, path: "/issues", headers: map[string]string{"Sec-Fetch-Dest": "iframe", "Accept": "text/html"}},
		{name: "HX request", method: http.MethodGet, path: "/issues", headers: map[string]string{"Sec-Fetch-Dest": "document", "HX-Request": "true"}},
		{name: "XHR", method: http.MethodGet, path: "/issues", headers: map[string]string{"Sec-Fetch-Dest": "document", "X-Requested-With": "XMLHttpRequest"}},
		{name: "JSON accept", method: http.MethodGet, path: "/issues", headers: map[string]string{"Accept": "application/json"}},
		{name: "mutation", method: http.MethodPost, path: "/issues", headers: map[string]string{"Sec-Fetch-Dest": "document"}},
		{name: "account security flow", method: http.MethodGet, path: "/user/settings/security", headers: map[string]string{"Sec-Fetch-Dest": "document"}},
		{name: "session endpoint", method: http.MethodGet, path: "/-/native/session", headers: map[string]string{"Sec-Fetch-Dest": "document"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://forgejo"+tt.path, nil)
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}
			ctx, _ := testContext(req)
			assert.Equal(t, tt.want, workspaceDocument(ctx))
		})
	}
}

func TestUnsafeAPIRejectsOriginBeforeAdmission(t *testing.T) {
	previous := setting.AppURL
	setting.AppURL = "https://forge.example/base/"
	defer func() { setting.AppURL = previous }()
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("Origin") },
		func(r *http.Request) { r.Header.Set("Origin", "") },
		func(r *http.Request) { r.Header.Set("Origin", "null") },
		func(r *http.Request) { r.Header.Set("Origin", "https://foreign.example") },
		func(r *http.Request) { r.Header.Add("Origin", "https://forge.example") },
		func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		func(r *http.Request) { r.Header.Add("Sec-Fetch-Site", "same-origin") },
	} {
		req := httptest.NewRequest(http.MethodPost, "https://forge.example/base/api/action", nil)
		req.Header.Set("Origin", "https://forge.example")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		mutate(req)
		ctx, response := testContext(req)
		// No native session/runtime exists here: validation must precede lookup
		// and admission, so the only valid outcome is the origin denial.
		api("global", false)(ctx)
		require.Equal(t, http.StatusForbidden, response.Code)
	}
}

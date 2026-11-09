// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/unit"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	extensions_service "forgejo.org/services/extensions"

	"golang.org/x/net/websocket"
)

// NativeSession excludes API and reverse-proxy credentials from the extension surface.
// The session uid must be the actor selected by Forgejo's authentication chain.
func NativeSession(ctx *context.Context) {
	if !nativeSession(ctx) {
		ctx.Error(http.StatusUnauthorized)
	}
}

func nativeSession(ctx *context.Context) bool {
	if ctx.Session == nil || ctx.Doer == nil || ctx.Req.Header.Get("Authorization") != "" || ctx.Req.URL.Query().Has("token") || ctx.Req.URL.Query().Has("access_token") {
		return false
	}
	uid, ok := ctx.Session.Get("uid").(int64)
	return ok && uid == ctx.Doer.ID && ctx.Authentication != nil && !ctx.Authentication.IsReverseProxyAuthentication()
}

// Register keeps the existing native account-state and cross-origin middleware.
// The admin Extensions settings routes reuse the caller's live site-admin
// guard, so lifecycle actions keep native admin authentication and the same
// cross-origin protection as every other admin route.
func Register(m *web.Route, signedIn, admin any) {
	m.Group("/admin/extensions", func() {
		m.Get("", NativeSession, admin, AdminExtensions)
		m.Get("/status", NativeSession, admin, AdminExtensionsStatus)
		m.Get("/{id}/settings/{page}", NativeSession, admin, AdminExtensionSettings)
		m.Post("/enable/{id}", NativeSession, admin, AdminEnableExtension)
		m.Post("/disable/{id}", NativeSession, admin, AdminDisableExtension)
	})
	register := func(prefix, scope string, guards ...any) {
		m.Methods("GET,HEAD,POST,PUT,PATCH,DELETE", prefix+"/{extension}/{page}/api/*", append(guards, api(scope, false))...)
	}
	register("/-/native/pages", "global", NativeSession, signedIn)
	register("/user/settings/-/native/pages", "user", NativeSession, signedIn)
	register("/admin/-/native/pages", "admin", NativeSession, admin)
	register("/{username}/{reponame}/-/native/pages", "repository", NativeSession, signedIn, context.RepoAssignment, context.UnitTypes(), Navigation)
	for _, scope := range []string{"global", "user", "admin"} {
		for _, route := range extension.NativePageRoutes(scope) {
			prefix := ""
			guard := signedIn
			if scope == "user" {
				prefix = "/user/settings"
			}
			if scope == "admin" {
				prefix = "/admin"
				guard = admin
			}
			m.Get(prefix+"/"+route, guard, NativeSession, pageRoute(scope, route))
			// Declared subordinate content routes (NFX-C04/H-ROUTE) are
			// resolved generically against the page's manifest
			// declarations; undeclared suffixes 404 in the handler.
			m.Get(prefix+"/"+route+"/*", guard, NativeSession, pageSubroute(scope, route))
		}
	}
	m.Get("/{username}/{reponame}/feed", signedIn, NativeSession, context.RepoAssignment, context.UnitTypes(), Navigation, repositoryFeed)
	m.Get("/{username}/{reponame}/feed/*", signedIn, NativeSession, context.RepoAssignment, context.UnitTypes(), Navigation, repositorySubroute)
	m.Get("/-/native/session", NativeSession, signedIn, SessionCheck)
	m.Methods("GET,HEAD", "/-/native/assets/{extension}/*", NativeSession, signedIn, Assets)
	m.Methods("GET,HEAD,POST,PUT,PATCH,DELETE", "/-/native/panels/{extension}/{page}/api/*", NativeSession, signedIn, api("panel", true))
	if setting.IsInTesting {
		m.Get("/-/native/test/session-stream", NativeSession, signedIn, sessionStreamTest)
	}
}

func sessionStreamTest(ctx *context.Context) {
	websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var value string
			if err := websocket.Message.Receive(conn, &value); err != nil {
				return
			}
			if err := websocket.Message.Send(conn, value); err != nil {
				return
			}
		}
	}).ServeHTTP(ctx.Resp, ctx.Req)
}

type Link struct {
	ID, Title, ExtensionID, URL, Entry, APIBase, AssetBase, Permission string
	OwnerID                                                            string
}

// Navigation also runs after repository assignment, when its permission is known.
func Navigation(ctx *context.Context) {
	pages := map[string][]Link{}
	ctx.Data["ExtensionPages"] = pages
	manager := extensions_service.GetManager()
	if manager == nil || !nativeSession(ctx) {
		return
	}
	// Composition changes the successful HTML presentation, never the native
	// handler, URL, session, repository assignment or account-state guards.
	eligibleWorkspace := workspaceDocument(ctx)
	preferred := false

	for _, listed := range manager.List() {
		descriptor, transport, ok := manager.Lookup(listed.Manifest.ID)
		if !ok {
			continue
		}
		if eligibleWorkspace && descriptor.Manifest.PreferredWorkspace {
			preferred = true
		}
		for _, p := range descriptor.Manifest.Pages {
			if p.Route == "" || !allowed(ctx, p) {
				continue
			}
			repositoryID := ""
			if p.Scope == "repository" {
				repositoryID = strconv.FormatInt(ctx.Repo.Repository.ID, 10)
			}
			if !authorizesContribution(ctx.Req.Context(), descriptor, transport, pageContribution(p, http.MethodGet), repositoryID, ctx.Doer.ID) {
				continue
			}
			base := pageBase(ctx, p.Scope)
			if base == "" && p.Scope == "repository" {
				continue
			}
			pages[p.Scope] = append(pages[p.Scope], Link{ID: p.ID, Title: p.Title, ExtensionID: descriptor.Manifest.ID, Permission: p.Permission, URL: base + "/" + p.Route})
		}
	}
	if preferred {
		composePanelsHome(ctx)
	}
}

func pageBase(ctx *context.Context, scope string) string {
	switch scope {
	case "global":
		return setting.AppSubURL
	case "user":
		return setting.AppSubURL + "/user/settings"
	case "admin":
		return setting.AppSubURL + "/admin"
	case "repository":
		if ctx.Repo != nil && ctx.Repo.Repository != nil {
			return ctx.Repo.RepoLink
		}
	}
	return ""
}

func allowed(ctx *context.Context, p extension.Page) bool {
	switch p.Scope {
	case "global", "user":
		return true
	case "admin":
		return ctx.Doer.IsAdmin
	case "repository":
		if ctx.Repo == nil || ctx.Repo.Repository == nil {
			return false
		}
		switch p.Permission {
		case "issues":
			return ctx.Repo.CanRead(unit.TypeIssues)
		case "read":
			return ctx.Repo.CanRead(unit.TypeCode)
		case "write":
			return ctx.Repo.CanWrite(unit.TypeCode)
		case "admin":
			return ctx.Repo.IsAdmin()
		}
	}
	return false
}

func findPage(ctx *context.Context, scope string) (extensions_service.Descriptor, http.RoundTripper, extension.Page, bool) {
	manager := extensions_service.GetManager()
	if manager != nil {
		if d, transport, ok := manager.Lookup(ctx.Params("extension")); ok {
			for _, p := range d.Manifest.Pages {
				if p.ID == ctx.Params("page") && p.Scope == scope && allowed(ctx, p) {
					return d, transport, p, true
				}
			}
		}
	}
	ctx.NotFound("Page", nil)
	return extensions_service.Descriptor{}, nil, extension.Page{}, false
}

// Public pages resolve host-owned routes without exposing package identities.
func pageRoute(scope, route string) func(*context.Context) {
	return func(ctx *context.Context) {
		d, transport, p, ok := findRoute(ctx, scope, route)
		if !ok {
			ctx.NotFound("Page", nil)
			return
		}
		renderPage(ctx, d, transport, p)
	}
}

// matchDirectPattern reports whether a root-relative path matches one
// declared direct pattern such as "/workspace/requests/{request}".
// Parameter segments capture one bounded opaque segment; the host
// applies no product-ID shape rule (NFX-C04/H-ROUTE).
func matchDirectPattern(path, pattern string) bool {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	segments := strings.Split(pattern, "/")
	if len(parts) != len(segments) {
		return false
	}
	for i, segment := range segments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			if parts[i] == "" || len(parts[i]) > 255 {
				return false
			}
			continue
		}
		if segment != parts[i] {
			return false
		}
	}
	return true
}

// pageSubroute serves a declared subordinate route of a native page with
// identical admission to its page root: the native session and scope
// guards run first, then the suffix must match a manifest declaration.
// Undeclared suffixes 404; declared parameter values are passed through
// opaquely for the consumer backend to validate.
func pageSubroute(scope, route string) func(*context.Context) {
	return func(ctx *context.Context) {
		d, transport, p, ok := findRoute(ctx, scope, route)
		if !ok {
			ctx.NotFound("Page", nil)
			return
		}
		sub, _, matched := p.MatchSubroute(ctx.Params("*"))
		if !matched {
			ctx.NotFound("Page", nil)
			return
		}
		if sub.Title != "" {
			p.Title = sub.Title
		}
		renderPage(ctx, d, transport, p)
	}
}

func repositorySubroute(ctx *context.Context) {
	if !ctx.Repo.CanRead(unit.TypeCode) && !ctx.Repo.CanRead(unit.TypeIssues) {
		ctx.NotFound("Feed", nil)
		return
	}
	d, transport, p, ok := findRoute(ctx, "repository", "feed")
	if !ok {
		ctx.NotFound("Feed", nil)
		return
	}
	sub, _, matched := p.MatchSubroute(ctx.Params("*"))
	if !matched {
		ctx.NotFound("Page", nil)
		return
	}
	if sub.Title != "" {
		p.Title = sub.Title
	}
	ctx.Data["ExtensionRepositoryID"] = strconv.FormatInt(ctx.Repo.Repository.ID, 10)
	renderPage(ctx, d, transport, p)
}

func findRoute(ctx *context.Context, scope, route string) (extensions_service.Descriptor, http.RoundTripper, extension.Page, bool) {
	if manager := extensions_service.GetManager(); manager != nil {
		for _, listed := range manager.List() {
			d, transport, ok := manager.Lookup(listed.Manifest.ID)
			if !ok {
				continue
			}
			for _, p := range d.Manifest.Pages {
				if p.Scope == scope && p.Route == route && allowed(ctx, p) {
					return d, transport, p, true
				}
			}
		}
	}
	return extensions_service.Descriptor{}, nil, extension.Page{}, false
}

// repositoryFeed serves the repository-scoped feed.
func repositoryFeed(ctx *context.Context) {
	if !ctx.Repo.CanRead(unit.TypeCode) && !ctx.Repo.CanRead(unit.TypeIssues) {
		ctx.NotFound("Feed", nil)
		return
	}
	d, transport, p, ok := findRoute(ctx, "repository", "feed")
	if !ok {
		ctx.NotFound("Feed", nil)
		return
	}
	ctx.Data["ExtensionRepositoryID"] = strconv.FormatInt(ctx.Repo.Repository.ID, 10)
	renderPage(ctx, d, transport, p)
}

func pageAPIBase(ctx *context.Context, d extensions_service.Descriptor, p extension.Page) string {
	prefix := setting.AppSubURL
	switch p.Scope {
	case "user":
		prefix += "/user/settings"
	case "admin":
		prefix += "/admin"
	case "repository":
		prefix = ctx.Repo.RepoLink
	}
	return prefix + "/-/native/pages/" + d.Manifest.ID + "/" + p.ID + "/api/"
}

func renderPage(ctx *context.Context, d extensions_service.Descriptor, transport http.RoundTripper, p extension.Page) {
	generation, err := sessionGeneration(ctx)
	if err != nil {
		ctx.Error(http.StatusServiceUnavailable, "Session authority unavailable")
		return
	}
	repositoryID := ""
	if p.Scope == "repository" {
		repositoryID = strconv.FormatInt(ctx.Repo.Repository.ID, 10)
	}
	if !authorizesContribution(ctx.Req.Context(), d, transport, pageContribution(p, http.MethodGet), repositoryID, ctx.Doer.ID) {
		ctx.NotFound("Page", nil)
		return
	}
	assetBase := setting.AppSubURL + "/-/native/assets/" + d.Manifest.ID + "/"
	ctx.Data["Title"] = p.Title
	ctx.Data["PageIsUserSettings"] = p.Scope == "user"
	ctx.Data["PageIsAdmin"] = p.Scope == "admin"
	ctx.Data["ExtensionScope"] = p.Scope
	ctx.Data["ExtensionRepoSettings"] = p.Scope == "repository" && p.Permission == "admin"
	ctx.Data["PageIsRepoSettings"] = p.Scope == "repository" && p.Permission == "admin"
	ctx.Data["Extension"] = map[string]string{"ID": d.Manifest.ID, "Name": d.Manifest.Name, "PageID": p.ID, "Title": p.Title, "OwnerID": p.Owner}
	ctx.Data["ExtensionAPIBase"] = pageAPIBase(ctx, d, p)
	ctx.Data["ExtensionAssetBase"] = assetBase
	ctx.Data["ExtensionEntry"] = assetBase + p.Entry
	ctx.Data["BrowserContractVersion"] = strconv.Itoa(extension.BrowserContractVersion)
	ctx.Data["ExtensionSessionGeneration"] = generation
	ctx.Resp.Header().Set("Cache-Control", "no-store")
	ctx.Resp.Header().Set(extension.SessionGenerationHeader, generation)
	ctx.Data["PageIsExtension"] = true
	// A shared owner presents this page through its retained panel. An
	// independent page (including a repository feed at /feed) owns
	// its own mount and must not expose the workspace panels below it.
	ctx.Data["PageIsWorkspacePage"] = p.Owner != ""
	ctx.Data["ExtensionPageURL"] = setting.AppSubURL + ctx.Req.URL.EscapedPath()
	ctx.PageData["extension"] = map[string]string{"id": d.Manifest.ID, "name": d.Manifest.Name, "pageID": p.ID, "title": p.Title}
	ctx.PageData["apiBase"] = pageAPIBase(ctx, d, p)
	ctx.PageData["assetBase"] = assetBase
	ctx.PageData["entry"] = assetBase + p.Entry
	ctx.HTML(http.StatusOK, "extensions/page")
}

func SessionCheck(ctx *context.Context) {
	generation, err := sessionGeneration(ctx)
	if err != nil {
		ctx.Error(http.StatusServiceUnavailable, "Session authority unavailable")
		return
	}
	ctx.Resp.Header().Set("Cache-Control", "no-store")
	ctx.Resp.Header().Set(extension.SessionGenerationHeader, generation)
	ctx.Resp.WriteHeader(http.StatusNoContent)
}

func workspaceDocument(ctx *context.Context) bool {
	if ctx.Req.Method != http.MethodGet || !validWorkspacePath(setting.AppSubURL+ctx.Req.URL.RequestURI()) {
		return false
	}
	if ctx.Req.Header.Get("HX-Request") == "true" || ctx.Req.Header.Get("X-Requested-With") != "" {
		return false
	}
	dest := ctx.Req.Header.Get("Sec-Fetch-Dest")
	// This header selects HTML presentation only; native admission remains authoritative.
	// Transplant fetches serve every eligible path (validWorkspacePath gates
	// above): the client only fetches transplantable layouts, and the entered
	// document must carry the home and generation whatever its shape.
	if dest == "empty" && ctx.Req.Header.Get("X-Extension-Workspace-Navigation") == "1" && strings.Contains(ctx.Req.Header.Get("Accept"), "text/html") {
		return true
	}
	return dest == "document" || dest == "" && strings.Contains(ctx.Req.Header.Get("Accept"), "text/html")
}

// composePanelsHome sets the panels-home data for every eligible workspace
// document. The native shared layout (templates/base/footer.tmpl) renders a
// persistent generic panels region from ExtensionPanels; direct
// contribution paths additionally receive the no-store generation envelope
// from renderPage. There is no parallel shell: ordinary native documents
// keep their templates and gain only data plus the layout region.
func composePanelsHome(ctx *context.Context) {
	manager := extensions_service.GetManager()
	if manager == nil {
		return
	}
	generation, err := sessionGeneration(ctx)
	if err != nil {
		return
	}
	panels := []Link{}
	directPatterns := []string{}
	for _, listed := range manager.List() {
		d, transport, ok := manager.Lookup(listed.Manifest.ID)
		if !ok {
			continue
		}
		for _, pg := range d.Manifest.Pages {
			if pg.Scope != "global" || pg.Route == "" {
				continue
			}
			for _, sub := range pg.Subroutes {
				directPatterns = append(directPatterns, "/"+pg.Route+"/"+sub.Pattern)
			}
		}
		for _, p := range d.Manifest.Panels {
			if !authorizesContribution(ctx.Req.Context(), d, transport, panelContribution(p, http.MethodGet), "", ctx.Doer.ID) {
				continue
			}
			assetBase := setting.AppSubURL + "/-/native/assets/" + d.Manifest.ID + "/"
			apiBase := setting.AppSubURL + "/-/native/panels/" + d.Manifest.ID + "/" + p.ID + "/api/"
			ownerID := ""
			if p.Owner != "" {
				// Canonical shared-owner API identity (NFX-C04/H-OWNER):
				// the linked panel mounts on the page's API base.
				if page, _, linked := d.Manifest.OwnerContributions(p.Owner); linked {
					apiBase = setting.AppSubURL + "/-/native/pages/" + d.Manifest.ID + "/" + page.ID + "/api/"
					ownerID = p.Owner
				}
			}
			panels = append(panels, Link{ID: p.ID, Title: p.Title, ExtensionID: d.Manifest.ID, Entry: assetBase + p.Entry, APIBase: apiBase, AssetBase: assetBase, OwnerID: ownerID})
		}
	}
	// P1: every eligible document carries the authoritative session
	// generation so transplants verify the entered page belongs to the live
	// session before joining it to owners. Cache-Control stays with the
	// direct renderers (renderPage); native documents keep their headers.
	ctx.Resp.Header().Set(extension.SessionGenerationHeader, generation)
	ctx.Data["ExtensionPanelsHome"] = true
	// C03 pairing: mount roots carry the SDK-derived browser contract
	// version; the mount refuses anything else before import.
	if len(directPatterns) > 0 {
		if encoded, err := json.Marshal(directPatterns); err == nil {
			ctx.Data["ExtensionDirectPatterns"] = string(encoded)
		}
	}
	ctx.Data["BrowserContractVersion"] = strconv.Itoa(extension.BrowserContractVersion)
	ctx.Data["ExtensionPanels"] = panels
	ctx.Data["ExtensionSessionGeneration"] = generation
	ctx.PageData["extensionPanels"] = panels
}

var (
	workspaceAuthPath      = regexp.MustCompile(`(^|/)user/(login|logout|sign_up|forgot_password|forget_password|reset_password|recover_account|activate|activate_email|two_factor|webauthn|oauth2|openid|link_account|link_account_signin|link_account_signup)(/|$)|(^|/)user/settings/(change_password|security|applications|keys)(/|$)|(^|/)(install|login/oauth|login/openid|oauth2|openid)(/|$)`)
	workspaceCredentialKey = regexp.MustCompile(`(?i)^(token|password|secret|client_secret|authorization|auth|code|credential|session|api_key|private_key|.*_token)$`)
)

func validWorkspacePath(path string) bool {
	u, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, setting.AppSubURL+"/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n") || u.Host != "" || u.Scheme != "" {
		return false
	}
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.ContainsAny(u.Path, "\\\r\n") {
		return false
	}
	clean := pathpkg.Clean(u.Path)
	if setting.AppSubURL != "" && clean != setting.AppSubURL && !strings.HasPrefix(clean, setting.AppSubURL+"/") {
		return false
	}
	if workspaceAuthPath.MatchString(clean) || strings.Contains(clean, "/-/native/") {
		return false
	}
	for key := range u.Query() {
		if workspaceCredentialKey.MatchString(key) {
			return false
		}
	}
	return true
}

func Assets(ctx *context.Context) {
	manager := extensions_service.GetManager()
	if manager == nil {
		ctx.NotFound("Page", nil)
		return
	}
	d, _, ok := manager.Lookup(ctx.Params("extension"))
	if !ok {
		ctx.NotFound("Page", nil)
		return
	}
	root, err := os.OpenRoot(filepath.Join(d.Root, "assets"))
	if err != nil {
		ctx.NotFound("Asset", nil)
		return
	}
	defer root.Close()
	name := ctx.Params("*")
	file, err := root.Open(name)
	if err != nil {
		ctx.NotFound("Asset", nil)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		ctx.NotFound("Asset", nil)
		return
	}
	ctx.Resp.Header().Set("X-Content-Type-Options", "nosniff")
	ctx.Resp.Header().Set("Cache-Control", "no-store")
	http.ServeContent(ctx.Resp, ctx.Req, name, stat.ModTime(), file)
}

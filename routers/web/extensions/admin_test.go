// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	user_model "forgejo.org/models/user"
	packages "forgejo.org/modules/extensions"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/templates"
	"forgejo.org/services/auth"
	"forgejo.org/services/context"
	"forgejo.org/services/contexttest"
	extensions_service "forgejo.org/services/extensions"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func adminTestContext(t *testing.T, method, path string, admin bool) (*context.Context, *httptest.ResponseRecorder) {
	t.Helper()
	ctx, recorder := contexttest.MockContext(t, path, contexttest.MockContextOption{Render: templates.HTMLRenderer()})
	doer := &user_model.User{ID: 42}
	if admin {
		doer.IsAdmin = true
	}
	ctx.Doer = doer
	ctx.Session = sessionUID{uid: int64(42), id: "admin-test-session"}
	ctx.Authentication = &auth.UnauthenticatedResult{}
	ctx.Req.Method = method
	return ctx, recorder
}

func withExtensionID(ctx *context.Context, id string) {
	rctx := chi.RouteContext(ctx.Req.Context())
	rctx.URLParams.Add("id", id)
}

func withExtensionPage(ctx *context.Context, id, page string) {
	rctx := chi.RouteContext(ctx.Req.Context())
	rctx.URLParams.Add("id", id)
	rctx.URLParams.Add("page", page)
}

// setAdminOrigin installs the exact-test Origin for one request through
// the real validAPIOrigin guard: AppURL is the only trusted origin.
func setAdminOrigin(t *testing.T, ctx *context.Context) {
	t.Helper()
	previous := setting.AppURL
	setting.AppURL = "https://forge.example/base/"
	t.Cleanup(func() { setting.AppURL = previous })
	ctx.Req.Header.Set("Origin", "https://forge.example")
	ctx.Req.Header.Set("Sec-Fetch-Site", "same-origin")
}

// setAdminForm installs one exact 4 KiB form mutation body: one
// expected_generation and one catalog_sha256, nothing else.
func setAdminForm(ctx *context.Context, generation, catalogSHA string) {
	form := url.Values{"expected_generation": {generation}, "catalog_sha256": {catalogSHA}}.Encode()
	ctx.Req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx.Req.Body = io.NopCloser(strings.NewReader(form))
	ctx.Req.ContentLength = int64(len(form))
}

// writeAdminCatalog writes one release catalog file and returns its
// exact SHA-256 for mutation pins.
func writeAdminCatalog(t *testing.T, doc string) (path, sha string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "catalog.json")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))
	sum := sha256.Sum256([]byte(doc))
	return path, hex.EncodeToString(sum[:])
}

func sampleCatalogDoc(sha string) string {
	return `{"version":1,"packages":[{"id":"sample","version":"1.0","url":"https://publisher.example/releases/download/v1/sample-1.0.0.tar.gz","size":100,"sha256":"` + sha + `","required":false,"fountain_protocol":1,"browser_contract":1,"capabilities":[]}]}`
}

// startAdminManager starts one manager with the given catalog document
// (empty for the zero-extension layout) and registers it as default.
func startAdminManager(t *testing.T, root, catalogDoc string) *extensions_service.Manager {
	t.Helper()
	m := extensions_service.NewManager(root)
	if catalogDoc != "" {
		path, _ := writeAdminCatalog(t, catalogDoc)
		require.NoError(t, m.SetCatalogPath(path))
	}
	require.NoError(t, m.Start(t.Context()))
	t.Cleanup(func() { _ = m.Close() })
	previous := extensions_service.GetManager()
	extensions_service.SetDefault(m)
	t.Cleanup(func() { extensions_service.SetDefault(previous) })
	return m
}

func writeAdminReceipt(t *testing.T, root, id, version, sha string) {
	t.Helper()
	doc := `{"schema":1,"package_id":"` + id + `","package_version":"` + version + `","archive_sha256":"` + sha + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(root, id, ".fountain-package.json"), []byte(doc), 0o600))
}

func writeAdminPackageWithPages(t *testing.T, root, id, pages string) {
	t.Helper()
	packageDir := filepath.Join(root, id)
	require.NoError(t, os.MkdirAll(packageDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), []byte(`{"protocol":1,"id":"`+id+`","name":"Sample","version":"1.0","executable":"backend","pages":[`+pages+`]}`), 0o600))
}

const adminSettingsPageDoc = `{"id":"main","title":"Main","scope":"admin","permission":"admin","entry":"main.js"}`

func adminTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	previousPath, previousEnabled, previousRequired := setting.Extensions.Path, setting.Extensions.Enabled, setting.Extensions.RequiredIDs
	setting.Extensions.Path, setting.Extensions.Enabled = root, true
	setting.Extensions.RequiredIDs = nil
	t.Cleanup(func() {
		setting.Extensions.Path, setting.Extensions.Enabled, setting.Extensions.RequiredIDs = previousPath, previousEnabled, previousRequired
	})
	return root
}

// setAdminDesiredState records file-level requested state through the
// owned intent CAS path, which works before and after manager start:
// the manager holds the package lock, not the intent files.
func setAdminDesiredState(t *testing.T, root, id string, desired bool) {
	t.Helper()
	installation, err := packages.EnsureInstallation(root, id)
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, id, installation, desired, nil, "0")
	require.NoError(t, err)
}

func writeAdminPackage(t *testing.T, root, id string, executable bool) {
	t.Helper()
	packageDir := filepath.Join(root, id)
	require.NoError(t, os.MkdirAll(packageDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(packageDir, "extension.json"), []byte(`{"protocol":1,"id":"`+id+`","name":"Sample","version":"1.0","executable":"backend"}`), 0o600))
	if executable {
		require.NoError(t, os.WriteFile(filepath.Join(packageDir, "backend"), []byte("fixture"), 0o755))
	}
}

func TestAdminExtensionsDeniesNonAdmin(t *testing.T) {
	_ = adminTestRoot(t)
	ctx, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions", false)
	// Forged browser headers never promote the session-selected actor.
	ctx.Req.Header.Set("X-Forgejo-Extension-Context", `{"actor":{"id":"1","site_admin":true}}`)
	ctx.Req.Header.Set("X-Admin", "true")
	AdminExtensions(ctx)
	require.Equal(t, http.StatusForbidden, recorder.Code)

	ctx, recorder = adminTestContext(t, http.MethodPost, "/admin/extensions/disable/sample", false)
	withExtensionID(ctx, "sample")
	AdminDisableExtension(ctx)
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestAdminExtensionsIgnoresBrowserActorHeaders(t *testing.T) {
	root := adminTestRoot(t)
	writeAdminPackage(t, root, "sample", false)
	ctx, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions", true)
	// A header claiming a non-admin actor must not demote the session admin.
	ctx.Req.Header.Set("X-Forgejo-Extension-Context", `{"actor":{"id":"2","site_admin":false}}`)
	AdminExtensions(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "sample")
	require.Contains(t, recorder.Body.String(), "1.0")
}

func TestAdminDisableRefusesRequiredWithoutManager(t *testing.T) {
	_ = adminTestRoot(t)
	previous := setting.Extensions.RequiredIDs
	setting.Extensions.RequiredIDs = []string{"soda"}
	t.Cleanup(func() { setting.Extensions.RequiredIDs = previous })
	ctx, recorder := adminTestContext(t, http.MethodPost, "/admin/extensions/disable/soda", true)
	withExtensionID(ctx, "soda")
	AdminDisableExtension(ctx)
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func adminDocSHA(doc string) string {
	sum := sha256.Sum256([]byte(doc))
	return hex.EncodeToString(sum[:])
}

func TestAdminEnableRejectsInvalidAndUnknown(t *testing.T) {
	root := adminTestRoot(t)
	doc := sampleCatalogDoc(strings.Repeat("a", 64))
	startAdminManager(t, root, doc)
	sha := adminDocSHA(doc)

	ctx, recorder := adminTestContext(t, http.MethodPost, "/admin/extensions/enable/../escape", true)
	setAdminOrigin(t, ctx)
	withExtensionID(ctx, "../escape")
	AdminEnableExtension(ctx)
	require.Equal(t, http.StatusBadRequest, recorder.Code)

	ctx, recorder = adminTestContext(t, http.MethodPost, "/admin/extensions/enable/ghost", true)
	setAdminOrigin(t, ctx)
	withExtensionID(ctx, "ghost")
	setAdminForm(ctx, "0", sha)
	AdminEnableExtension(ctx)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func TestAdminDisableIdlePackageRedirects(t *testing.T) {
	root := adminTestRoot(t)
	doc := `{"version":1,"packages":[]}`
	startAdminManager(t, root, doc)
	sha := adminDocSHA(doc)
	writeAdminPackage(t, root, "sample", false)
	setAdminDesiredState(t, root, "sample", false)
	intent, err := packages.LoadIntent(root, "sample")
	require.NoError(t, err)
	require.NoError(t, packages.BindIntentController(root, "sample", intent.InstallationID, ""))

	ctx, recorder := adminTestContext(t, http.MethodPost, "/admin/extensions/disable/sample", true)
	setAdminOrigin(t, ctx)
	withExtensionID(ctx, "sample")
	setAdminForm(ctx, intent.Generation, sha)
	AdminDisableExtension(ctx)
	require.Equal(t, http.StatusSeeOther, recorder.Code)
	require.Equal(t, "/admin/extensions", recorder.Header().Get("Location"))
}

func TestAdminExtensionsStatusGuardsAndRows(t *testing.T) {
	root := adminTestRoot(t)
	m := extensions_service.NewManager(root)
	require.NoError(t, m.Start(t.Context()))
	t.Cleanup(func() { _ = m.Close() })
	previous := extensions_service.GetManager()
	extensions_service.SetDefault(m)
	t.Cleanup(func() { extensions_service.SetDefault(previous) })

	// Installed after boot with requested state: the thin status shell
	// reports durable desired state without causing effects.
	writeAdminPackage(t, root, "sample", false)
	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "sample", installation, true, nil, "0")
	require.NoError(t, err)

	ctx, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions/status", true)
	AdminExtensionsStatus(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "sample", rows[0]["id"])
	require.Equal(t, true, rows[0]["desired_enabled"])
	require.Equal(t, "1", rows[0]["generation"])
	require.Equal(t, true, rows[0]["installed"])

	ctx, recorder = adminTestContext(t, http.MethodGet, "/admin/extensions/status", false)
	AdminExtensionsStatus(ctx)
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestAdminExtensionSettingsShell(t *testing.T) {
	_ = adminTestRoot(t)
	ctx, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions/sample/settings/main", true)
	withExtensionID(ctx, "sample")
	AdminExtensionSettings(ctx)
	require.Equal(t, http.StatusNotFound, recorder.Code)

	ctx, recorder = adminTestContext(t, http.MethodGet, "/admin/extensions/sample/settings/main", false)
	withExtensionID(ctx, "sample")
	AdminExtensionSettings(ctx)
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

// Acceptance redirects 303 immediately with the interruption marker
// already durable; the operational failure surfaces in status, never
// by turning the accepted POST into an error.
func TestAdminEnableFailureRendersTruthfully(t *testing.T) {
	root := adminTestRoot(t)
	archiveSHA := strings.Repeat("a", 64)
	doc := sampleCatalogDoc(archiveSHA)
	manager := startAdminManager(t, root, doc)
	sha := adminDocSHA(doc)
	// Installed after boot with no executable: activation has a real
	// runtime effect attempt and reports its failure in status. The
	// receipt pins match the catalog so no download is attempted.
	writeAdminPackage(t, root, "sample", false)
	writeAdminReceipt(t, root, "sample", "1.0", archiveSHA)

	ctx, recorder := adminTestContext(t, http.MethodPost, "/admin/extensions/enable/sample", true)
	setAdminOrigin(t, ctx)
	withExtensionID(ctx, "sample")
	setAdminForm(ctx, "0", sha)
	AdminEnableExtension(ctx)
	require.Equal(t, http.StatusSeeOther, recorder.Code)
	require.Equal(t, "/admin/extensions", recorder.Header().Get("Location"))
	intent, err := packages.LoadIntent(root, "sample")
	require.NoError(t, err)
	require.Equal(t, "1", intent.Generation)
	require.True(t, intent.DesiredEnabled)
	require.NotNil(t, intent.PendingOperation, "acceptance persists before effects run")

	deadline := time.Now().Add(10 * time.Second)
	for {
		observed := manager.Observe("sample")
		if observed.State == packages.LifecycleFailed && observed.Error != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted backend failure never appeared in status")
		}
		time.Sleep(50 * time.Millisecond)
	}
	observed := manager.Observe("sample")
	require.Equal(t, packages.LifecycleFailed, observed.State)
}

// Mutations go through the real session, site-admin and exact-Origin
// guards: a missing or foreign Origin on POST is forbidden without
// touching manager state.
func TestAdminMutateOriginEnforcement(t *testing.T) {
	root := adminTestRoot(t)
	doc := `{"version":1,"packages":[]}`
	startAdminManager(t, root, doc)
	sha := adminDocSHA(doc)
	writeAdminPackage(t, root, "sample", false)
	setAdminDesiredState(t, root, "sample", false)
	intent, err := packages.LoadIntent(root, "sample")
	require.NoError(t, err)

	ctx, recorder := adminTestContext(t, http.MethodPost, "/admin/extensions/disable/sample", true)
	withExtensionID(ctx, "sample")
	setAdminForm(ctx, intent.Generation, sha)
	AdminDisableExtension(ctx)
	require.Equal(t, http.StatusForbidden, recorder.Code)

	ctx, recorder = adminTestContext(t, http.MethodPost, "/admin/extensions/disable/sample", true)
	setAdminOrigin(t, ctx)
	ctx.Req.Header.Set("Origin", "https://foreign.example")
	withExtensionID(ctx, "sample")
	setAdminForm(ctx, intent.Generation, sha)
	AdminDisableExtension(ctx)
	require.Equal(t, http.StatusForbidden, recorder.Code)

	after, err := packages.LoadIntent(root, "sample")
	require.NoError(t, err)
	require.Equal(t, intent.Generation, after.Generation)
	require.Nil(t, after.PendingOperation)
}

// Only the exact mutation shape is accepted: form URL encoding, at most
// 4 KiB, exactly one generation and one catalog pin, no query strings.
func TestAdminMutateFormValidation(t *testing.T) {
	root := adminTestRoot(t)
	doc := `{"version":1,"packages":[]}`
	startAdminManager(t, root, doc)
	sha := adminDocSHA(doc)

	cases := map[string]func(ctx *context.Context){
		"query string": func(ctx *context.Context) {
			ctx.Req.URL.RawQuery = "expected_generation=1"
			setAdminForm(ctx, "1", sha)
		},
		"wrong content type": func(ctx *context.Context) {
			ctx.Req.Header.Set("Content-Type", "application/json")
			ctx.Req.Body = io.NopCloser(strings.NewReader(`{}`))
		},
		"duplicate field": func(ctx *context.Context) {
			form := "expected_generation=1&expected_generation=1&catalog_sha256=" + sha
			ctx.Req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			ctx.Req.Body = io.NopCloser(strings.NewReader(form))
		},
		"unknown field": func(ctx *context.Context) {
			form := "expected_generation=1&catalog_sha256=" + sha + "&version=9.9"
			ctx.Req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			ctx.Req.Body = io.NopCloser(strings.NewReader(form))
		},
		"missing pin": func(ctx *context.Context) {
			form := "expected_generation=1"
			ctx.Req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			ctx.Req.Body = io.NopCloser(strings.NewReader(form))
		},
		"oversize body": func(ctx *context.Context) {
			form := "expected_generation=1&catalog_sha256=" + sha + "&padding=" + strings.Repeat("p", 4<<10)
			ctx.Req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			ctx.Req.Body = io.NopCloser(strings.NewReader(form))
		},
		"bad generation": func(ctx *context.Context) {
			setAdminForm(ctx, "01", sha)
		},
		"bad catalog pin": func(ctx *context.Context) {
			setAdminForm(ctx, "1", "ZZZ")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, recorder := adminTestContext(t, http.MethodPost, "/admin/extensions/enable/sample", true)
			setAdminOrigin(t, ctx)
			withExtensionID(ctx, "sample")
			mutate(ctx)
			AdminEnableExtension(ctx)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

// Stale pins are rejected with 409 through the live compare-and-swap
// path: a changed catalog and a superseded generation both fail closed.
func TestAdminMutateStaleCatalogAndGeneration(t *testing.T) {
	root := adminTestRoot(t)
	doc := `{"version":1,"packages":[]}`
	startAdminManager(t, root, doc)
	sha := adminDocSHA(doc)
	writeAdminPackage(t, root, "sample", false)
	setAdminDesiredState(t, root, "sample", false)
	intent, err := packages.LoadIntent(root, "sample")
	require.NoError(t, err)

	ctx, recorder := adminTestContext(t, http.MethodPost, "/admin/extensions/disable/sample", true)
	setAdminOrigin(t, ctx)
	withExtensionID(ctx, "sample")
	setAdminForm(ctx, intent.Generation, strings.Repeat("b", 64))
	AdminDisableExtension(ctx)
	require.Equal(t, http.StatusConflict, recorder.Code)

	ctx, recorder = adminTestContext(t, http.MethodPost, "/admin/extensions/disable/sample", true)
	setAdminOrigin(t, ctx)
	withExtensionID(ctx, "sample")
	setAdminForm(ctx, "999999", sha)
	AdminDisableExtension(ctx)
	require.Equal(t, http.StatusConflict, recorder.Code)

	after, err := packages.LoadIntent(root, "sample")
	require.NoError(t, err)
	require.Equal(t, intent.Generation, after.Generation)
	require.Nil(t, after.PendingOperation)
}

// Required packages are refused with 403 before any manager effect,
// whether or not the manager is running.
func TestAdminDisableRequiredRefused(t *testing.T) {
	root := adminTestRoot(t)
	doc := `{"version":1,"packages":[]}`
	startAdminManager(t, root, doc)
	previous := setting.Extensions.RequiredIDs
	setting.Extensions.RequiredIDs = []string{"soda"}
	t.Cleanup(func() { setting.Extensions.RequiredIDs = previous })

	ctx, recorder := adminTestContext(t, http.MethodPost, "/admin/extensions/disable/soda", true)
	setAdminOrigin(t, ctx)
	withExtensionID(ctx, "soda")
	setAdminForm(ctx, "0", adminDocSHA(doc))
	AdminDisableExtension(ctx)
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

// Status rows carry the exact section-7 fields with nulls for
// unavailable versions and errors, never invented empty state.
func TestAdminStatusExactShape(t *testing.T) {
	root := adminTestRoot(t)
	startAdminManager(t, root, "")
	writeAdminPackage(t, root, "sample", false)
	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "sample", installation, true, nil, "0")
	require.NoError(t, err)

	ctx, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions/status", true)
	AdminExtensionsStatus(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	row := rows[0]
	for _, key := range []string{"id", "name", "required", "available_version", "installed_version", "installed", "desired_enabled", "generation", "backend", "effects", "state", "progress", "error"} {
		_, ok := row[key]
		require.True(t, ok, "status row misses %s", key)
	}
	require.Equal(t, "sample", row["id"])
	require.Equal(t, "Sample", row["name"])
	require.Equal(t, true, row["installed"])
	require.Equal(t, true, row["desired_enabled"])
	require.Equal(t, "1", row["generation"])
	require.Equal(t, "1.0", row["installed_version"])
	require.Nil(t, row["available_version"])
	require.Equal(t, "failed", row["state"])
	require.Equal(t, "stopped", row["backend"])
	require.Equal(t, "unknown", row["effects"])
	failure, ok := row["error"].(map[string]any)
	require.True(t, ok, "failed row needs an error object")
	require.Equal(t, "unconfirmed", failure["code"])
	require.NotEmpty(t, failure["message"])
}

// Refreshing status never repeats an action: generations and pending
// markers are unchanged by reads.
func TestAdminStatusRefreshWithoutRepeat(t *testing.T) {
	root := adminTestRoot(t)
	startAdminManager(t, root, "")
	writeAdminPackage(t, root, "sample", false)
	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "sample", installation, false, nil, "0")
	require.NoError(t, err)

	for range 2 {
		ctx, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions/status", true)
		AdminExtensionsStatus(ctx)
		require.Equal(t, http.StatusOK, recorder.Code)
	}
	after, err := packages.LoadIntent(root, "sample")
	require.NoError(t, err)
	require.Equal(t, "1", after.Generation)
	require.False(t, after.DesiredEnabled)
	require.Nil(t, after.PendingOperation)
}

// Settings resolve only a live enabled admin page with an empty route.
// Anything else 404s without starting anything.
func TestAdminSettingsResolutions(t *testing.T) {
	root := adminTestRoot(t)
	startAdminManager(t, root, "")
	writeAdminPackageWithPages(t, root, "sample", adminSettingsPageDoc)

	disabled, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions/sample/settings/main", true)
	setAdminOrigin(t, disabled)
	withExtensionPage(disabled, "sample", "main")
	AdminExtensionSettings(disabled)
	require.Equal(t, http.StatusNotFound, recorder.Code)

	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "sample", installation, true, nil, "0")
	require.NoError(t, err)

	stopped, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions/sample/settings/main", true)
	setAdminOrigin(t, stopped)
	withExtensionPage(stopped, "sample", "main")
	AdminExtensionSettings(stopped)
	require.Equal(t, http.StatusNotFound, recorder.Code)
	observed := extensions_service.GetManager().Observe("sample")
	require.NotEqual(t, "enabled", observed.State)

	writeAdminPackageWithPages(t, root, "other", `{"id":"home","title":"Home","scope":"global","entry":"main.js"}`)
	global, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions/other/settings/home", true)
	setAdminOrigin(t, global)
	withExtensionPage(global, "other", "home")
	AdminExtensionSettings(global)
	require.Equal(t, http.StatusNotFound, recorder.Code)

	unknown, recorder := adminTestContext(t, http.MethodGet, "/admin/extensions/sample/settings/missing", true)
	setAdminOrigin(t, unknown)
	withExtensionPage(unknown, "sample", "missing")
	AdminExtensionSettings(unknown)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

// StartRuntime wires the configured trusted catalog path into the
// manager: a missing file fails closed as catalog-unavailable (not
// as an empty catalog selection error), and the zero-extension
// layout still boots.
func TestStartRuntimeWiresConfiguredCatalogPath(t *testing.T) {
	root := adminTestRoot(t)
	previousCatalog, previousRequired := setting.Extensions.CatalogPath, setting.Extensions.RequiredIDs
	t.Cleanup(func() {
		setting.Extensions.CatalogPath, setting.Extensions.RequiredIDs = previousCatalog, previousRequired
	})

	setting.Extensions.CatalogPath = filepath.Join(root, "absent-catalog.json")
	setting.Extensions.RequiredIDs = []string{"soda"}
	_, err := StartRuntime(t.Context())
	require.ErrorContains(t, err, "unavailable")

	setting.Extensions.CatalogPath = ""
	setting.Extensions.RequiredIDs = nil
	closeFunc, err := StartRuntime(t.Context())
	require.NoError(t, err)
	require.NoError(t, closeFunc())
}

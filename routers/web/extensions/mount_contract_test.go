// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"encoding/json" //nolint:depguard // Mount datasets use the native wire JSON codec.
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
	"forgejo.org/services/contexttest"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

func renderMountContractTemplate(t *testing.T, name string, data any) string {
	t.Helper()
	tmpl := template.New("mount-contract").Funcs(template.FuncMap{"AppSubUrl": func() string { return setting.AppSubURL }})
	for _, partial := range []string{"extensions/mount", "extensions/content", "extensions/panels"} {
		source, err := os.ReadFile(filepath.Join(setting.StaticRootPath, "templates", partial+".tmpl"))
		require.NoError(t, err)
		_, err = tmpl.New(partial).Parse(string(source))
		require.NoError(t, err)
	}
	var output bytes.Buffer
	require.NoError(t, tmpl.ExecuteTemplate(&output, name, data))
	return output.String()
}

func mountContractAttributes(t *testing.T, output, marker string) []map[string]string {
	t.Helper()
	document, err := html.Parse(strings.NewReader(output))
	require.NoError(t, err)
	var matches []map[string]string
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		attributes := make(map[string]string, len(node.Attr))
		for _, attribute := range node.Attr {
			attributes[attribute.Key] = attribute.Val
		}
		if _, found := attributes[marker]; found {
			matches = append(matches, attributes)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return matches
}

func TestMountContractSerializesPageAndPanelFromOneDescriptor(t *testing.T) {
	previous := setting.AppSubURL
	setting.AppSubURL = "/forge"
	t.Cleanup(func() { setting.AppSubURL = previous })
	link := Link{
		ID: "spaces", ExtensionID: "example", OwnerID: "shared",
		Entry:   "/forge/-/native/assets/example/main.js",
		APIBase: "/forge/-/native/pages/example/spaces/api/", AssetBase: "/forge/-/native/assets/example/",
	}
	patterns := []string{"/applications", "/applications/request/{request}"}
	for _, mode := range []string{"page", "panel"} {
		t.Run(mode, func(t *testing.T) {
			descriptor := contributionMount(mode, link, "session-a", "7", patterns)
			output := renderMountContractTemplate(t, "extensions/mount", descriptor)
			roots := mountContractAttributes(t, output, "data-extension-"+mode)
			require.Len(t, roots, 1)
			attributes := roots[0]
			for key, value := range map[string]string{
				"data-extension-id": "example", "data-extension-" + mode + "-id": "spaces",
				"data-extension-owner-id": "shared", "data-app-sub-url": "/forge",
				"data-extension-entry": link.Entry, "data-extension-api-base": link.APIBase,
				"data-extension-asset-base": link.AssetBase, "data-extension-session-generation": "session-a",
				"data-extension-contract-version": "1", "data-extension-repository-id": "7",
			} {
				require.Equal(t, value, attributes[key], key)
			}
			var decoded []string
			require.NoError(t, json.Unmarshal([]byte(attributes["data-extension-direct-patterns"]), &decoded))
			require.Equal(t, patterns, decoded, "HTML escaping must preserve the owner route document")
			other := "page"
			if mode == "page" {
				other = "panel"
			}
			_, present := attributes["data-extension-"+other+"-id"]
			require.False(t, present, "one contribution kind must be serialized")
		})
	}
}

func TestMountContractOwnerPresentationStaysInsideExtension(t *testing.T) {
	panels := []Link{{ID: "terminal", ExtensionID: "other", OwnerID: "shared"}}
	for _, test := range []struct {
		name   string
		panels []Link
		want   bool
	}{
		{"foreign extension owner label", panels, false},
		{"own extension linked owner", append(panels, Link{ID: "terminal", ExtensionID: "example", OwnerID: "shared"}), true},
		{"another own owner", []Link{{ExtensionID: "example", OwnerID: "another"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			presented := ownerPresented("example", "shared", test.panels)
			require.Equal(t, test.want, presented)
			output := renderMountContractTemplate(t, "extensions/content", map[string]any{
				"ExtensionOwnerPresented": presented,
				"Extension":               map[string]string{"Title": "Applications"},
				"ExtensionMount": contributionMount("page", Link{
					ID: "spaces", ExtensionID: "example", OwnerID: "shared",
					Entry: "/-/native/assets/example/main.js", APIBase: "/-/native/pages/example/spaces/api/", AssetBase: "/-/native/assets/example/",
				}, "session-a", "", nil),
			})
			if test.want {
				require.Empty(t, mountContractAttributes(t, output, "data-extension-page"))
			} else {
				require.Len(t, mountContractAttributes(t, output, "data-extension-page"), 1)
			}
		})
	}
	require.False(t, ownerPresented("example", "", []Link{{ExtensionID: "example"}}), "unowned contributions do not share a retained owner")
}

func TestMountContractRoutesBelongToEachOwner(t *testing.T) {
	pages := []extension.Page{
		{ID: "applications", Scope: "global", Route: "applications", Owner: "first", Subroutes: []extension.Subroute{{Pattern: "new"}}},
		{ID: "activity", Scope: "global", Route: "feed", Owner: "second", Subroutes: []extension.Subroute{{Pattern: "requests/{request}"}}},
	}
	presentations := []panelPresentation{
		{Link: Link{ID: "applications", ExtensionID: "example", OwnerID: "first"}, Mount: contributionMount("panel", Link{ID: "applications", ExtensionID: "example", OwnerID: "first"}, "session-a", "", pageDirectPatterns(pages[0]))},
		{Link: Link{ID: "activity", ExtensionID: "example", OwnerID: "second"}, Mount: contributionMount("panel", Link{ID: "activity", ExtensionID: "example", OwnerID: "second"}, "session-a", "", pageDirectPatterns(pages[1]))},
	}
	output := renderMountContractTemplate(t, "extensions/panels", map[string]any{
		"ExtensionPanelsHome": true, "ExtensionPanelMounts": presentations, "ExtensionSessionGeneration": "session-a",
	})
	roots := mountContractAttributes(t, output, "data-extension-panel")
	require.Len(t, roots, 2)
	for index, want := range [][]string{{"/applications", "/applications/new"}, {"/feed", "/feed/requests/{request}"}} {
		var patterns []string
		require.NoError(t, json.Unmarshal([]byte(roots[index]["data-extension-direct-patterns"]), &patterns))
		require.Equal(t, want, patterns)
	}
	homes := mountContractAttributes(t, output, "data-extension-panels")
	require.Len(t, homes, 1)
	_, broadcast := homes[0]["data-extension-subroutes"]
	require.False(t, broadcast, "an owner's routes must not be broadcast through the panels home")
	for _, page := range []extension.Page{{Scope: "global"}, {Scope: "repository", Route: "feed"}, {Scope: "user", Route: "workspace"}, {Scope: "admin", Route: "workspace"}} {
		require.Empty(t, pageDirectPatterns(page), "global linked owners cannot claim another scope's paths")
	}
}

func TestMountContractRepositoryRouteKeepsCodeOrIssueVisibility(t *testing.T) {
	for _, test := range []struct {
		name  string
		units map[unit.Type]perm.AccessMode
		want  bool
	}{
		{"code read", map[unit.Type]perm.AccessMode{unit.TypeCode: perm.AccessModeRead}, true},
		{"issues read", map[unit.Type]perm.AccessMode{unit.TypeIssues: perm.AccessModeRead}, true},
		{"no readable contribution unit", map[unit.Type]perm.AccessMode{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, response := testContext(httptest.NewRequest(http.MethodGet, "/team/repo/feed", nil))
			ctx.Repo = &context.Repository{Repository: &repo_model.Repository{}, Permission: access_model.Permission{AccessMode: perm.AccessModeRead, UnitsMode: test.units}}
			require.Equal(t, test.want, pageScopeVisible(ctx, "repository"))
			if !test.want {
				require.Equal(t, http.StatusNotFound, response.Code)
			}
		})
	}
}

func TestMountContractRepositoryPresentationRefreshesExistingPanels(t *testing.T) {
	for _, test := range []struct {
		name, composedID, lateID string
	}{
		{"ordinary native repository fact arrives after composition", "", "7"},
		{"late repository fact supersedes composed display", "3", "7"},
		{"no repository display fact", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := contexttest.MockContext(t, "/team/repo/feed")
			link := Link{ID: "terminal", ExtensionID: "example", OwnerID: "shared"}
			composed := panelPresentation{Link: link, Mount: contributionMount("panel", link, "session-a", test.composedID, []string{"/applications"})}
			ctx.Data["ExtensionPanelsHome"] = true
			ctx.Data["ExtensionSessionGeneration"] = "session-a"
			ctx.Data["ExtensionPanelMounts"] = []panelPresentation{composed}
			ctx.Data["ExtensionPanels"] = []Link{link}
			ctx.PageData["extensionPanels"] = []Link{link}
			if test.lateID != "" {
				ctx.Data["ExtensionRepositoryID"] = test.lateID
			}
			output := renderMountContractTemplate(t, "extensions/panels", ctx.Data)
			roots := mountContractAttributes(t, output, "data-extension-panel")
			require.Len(t, roots, 1)
			if test.lateID == "" {
				_, present := roots[0]["data-extension-repository-id"]
				require.False(t, present, "a missing legacy display fact must remain valid and omit the repository attribute")
			} else {
				require.Equal(t, test.lateID, roots[0]["data-extension-repository-id"], "panels composed before Context.HTML must receive its final repository display fact")
			}
			require.Equal(t, []panelPresentation{composed}, ctx.Data["ExtensionPanelMounts"], "template projection must not mutate the admitted descriptor")
			require.Equal(t, []Link{link}, ctx.Data["ExtensionPanels"], "public Link projection must keep its existing shape")
			require.Equal(t, []Link{link}, ctx.PageData["extensionPanels"], "existing PageData projection remains compatible")
		})
	}
}

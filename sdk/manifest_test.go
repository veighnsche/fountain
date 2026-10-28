// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifest(t *testing.T) {
	packageDir := t.TempDir()
	valid := `{"protocol":1,"id":"sample-extension","name":"Sample","version":"1.0.0","executable":"bin/sample","pages":[{"id":"home","title":"Home","scope":"repository","entry":"main.js","permission":"read"}]}`
	if err := os.WriteFile(filepath.Join(packageDir, "extension.json"), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(packageDir)
	if err != nil || manifest.ID != "sample-extension" || manifest.Pages[0].Entry != "main.js" {
		t.Fatalf("valid manifest: %+v, %v", manifest, err)
	}
	for _, edit := range []struct{ old, replacement string }{
		{`"protocol":1`, `"protocol":2`},
		{`"executable":"bin/sample"`, `"executable":"../sample"`},
		{`"entry":"main.js"`, `"entry":"../main.js"`},
		{`"permission":"read"`, `"permission":"owner"`},
		{`"id":"sample-extension"`, `"id":"../sample"`},
	} {
		invalid := strings.Replace(valid, edit.old, edit.replacement, 1)
		if err := os.WriteFile(filepath.Join(packageDir, "extension.json"), []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadManifest(packageDir); err == nil {
			t.Fatalf("accepted invalid manifest: %s", invalid)
		}
	}
}

func TestLoadManifestBoundsAndSingleObject(t *testing.T) {
	const maximumManifestBytes = 1 << 20
	packageDir := t.TempDir()
	valid := `{"protocol":1,"id":"sample-extension","name":"Sample","version":"1.0.0","executable":"bin/sample"}`
	write := func(contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(packageDir, "extension.json"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(valid + strings.Repeat(" ", maximumManifestBytes-len(valid)))
	if manifest, err := LoadManifest(packageDir); err != nil || manifest.ID != "sample-extension" {
		t.Fatalf("exact byte limit rejected: manifest=%+v err=%v", manifest, err)
	}
	for name, contents := range map[string]string{
		"limit plus one whitespace": valid + strings.Repeat(" ", maximumManifestBytes-len(valid)+1),
		"large trailing whitespace": valid + strings.Repeat(" ", 4*maximumManifestBytes),
		"second JSON value":         valid + `{}`,
		"unknown field":             strings.TrimSuffix(valid, "}") + `,"extra":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			write(contents)
			if _, err := LoadManifest(packageDir); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}

	write(`{"protocol":1,"id":"sample-extension","name":"First","name":"Last","version":"1.0.0","executable":"bin/sample"}`)
	if manifest, err := LoadManifest(packageDir); err != nil || manifest.Name != "Last" {
		t.Fatalf("duplicate-field behavior changed: manifest=%+v err=%v", manifest, err)
	}
	manifestPath := filepath.Join(packageDir, "extension.json")
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(manifestPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(packageDir); err == nil {
		t.Fatal("manifest read failure accepted")
	}
}

func TestSharedOwnerDeclaration(t *testing.T) {
	base := func() Manifest {
		return Manifest{
			Protocol: Protocol, ID: "sample", Name: "Sample", Version: "1", Executable: "backend",
			Pages:  []Page{{ID: "spaces", Route: "workspace", Title: "Workspace", Scope: "global", Entry: "main.js", Owner: "workspace"}},
			Panels: []Panel{{ID: "workspace", Title: "Workspace", Entry: "main.js", Owner: "workspace"}},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatal("valid linked owner rejected:", err)
	}
	page, panel, ok := base().OwnerContributions("workspace")
	if !ok || page.ID != "spaces" || panel.ID != "workspace" {
		t.Fatalf("owner contributions = %q %q %v", page.ID, panel.ID, ok)
	}
	refusals := map[string]func(*Manifest){
		"page without panel": func(m *Manifest) { m.Panels[0].Owner = "" },
		"panel without page": func(m *Manifest) { m.Pages[0].Owner = "" },
		"second owner page": func(m *Manifest) {
			m.Pages = append(m.Pages, Page{ID: "other", Route: "tailnet", Title: "Other", Scope: "global", Entry: "main.js", Owner: "workspace"})
		},
		"second owner panel": func(m *Manifest) {
			m.Panels = append(m.Panels, Panel{ID: "other", Title: "Other", Entry: "main.js", Owner: "workspace"})
		},
		"entries differ": func(m *Manifest) { m.Panels[0].Entry = "panel.js" },
		"non-global page": func(m *Manifest) {
			m.Pages[0].Scope, m.Pages[0].Permission, m.Pages[0].Route = "user", "user", "workspace"
		},
		"routeless page":     func(m *Manifest) { m.Pages[0].Route = "" },
		"invalid owner slug": func(m *Manifest) { m.Pages[0].Owner, m.Panels[0].Owner = "Work Space", "Work Space" },
	}
	for name, mutate := range refusals {
		t.Run(name, func(t *testing.T) {
			m := base()
			mutate(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("shared-owner refusal accepted")
			}
		})
	}
}

func TestDeclaredSubroutes(t *testing.T) {
	m := Manifest{
		Protocol: Protocol, ID: "sample", Name: "Sample", Version: "1", Executable: "backend",
		Pages: []Page{{
			ID: "spaces", Route: "workspace", Title: "Workspace", Scope: "global", Entry: "main.js",
			Subroutes: []Subroute{{Pattern: "new", Title: "New Project"}, {Pattern: "requests/{request}", Title: "Build request"}},
		}},
	}
	if err := m.Validate(); err != nil {
		t.Fatal("valid declared subroutes rejected:", err)
	}
	page := m.Pages[0]
	// The host admits bounded opaque parameters without any product-ID
	// shape rule; the consumer backend validates product identities.
	for _, suffix := range []string{"new", "new/", "requests/d0123456789abcdef01234567", "requests/not-an-id", "requests/ABC-123"} {
		if _, _, ok := page.MatchSubroute("workspace", suffix); !ok {
			t.Fatalf("declared subroute %q refused", suffix)
		}
	}
	if sub, params, ok := page.MatchSubroute("workspace", "requests/abc"); !ok || sub.Title != "Build request" || params["request"] != "abc" {
		t.Fatalf("subroute match = %+v %v %v", sub, params, ok)
	}
	for _, suffix := range []string{"", "requests", "requests/", "requests/a/b", "new/extra", "unknown", "requests/" + strings.Repeat("x", 256)} {
		if _, _, ok := page.MatchSubroute("workspace", suffix); ok {
			t.Fatalf("undeclared subroute %q admitted", suffix)
		}
	}
	invalid := []Page{
		{ID: "spaces", Title: "Workspace", Scope: "global", Entry: "main.js", Subroutes: []Subroute{{Pattern: "new"}}},
		{ID: "spaces", Route: "workspace", Title: "Workspace", Scope: "global", Entry: "main.js", Subroutes: []Subroute{{Pattern: "new"}, {Pattern: "new"}}},
		{ID: "spaces", Route: "workspace", Title: "Workspace", Scope: "global", Entry: "main.js", Subroutes: []Subroute{{Pattern: "a/b/c/d"}}},
		{ID: "spaces", Route: "workspace", Title: "Workspace", Scope: "global", Entry: "main.js", Subroutes: []Subroute{{Pattern: "-/native"}}},
		{ID: "spaces", Route: "workspace", Title: "Workspace", Scope: "global", Entry: "main.js", Subroutes: []Subroute{{Pattern: "requests/{request}/{request}"}}},
		{ID: "spaces", Route: "workspace", Title: "Workspace", Scope: "global", Entry: "main.js", Subroutes: []Subroute{{Pattern: "requests/{Bad Name}"}}},
		{ID: "spaces", Route: "workspace", Title: "Workspace", Scope: "global", Entry: "main.js", Subroutes: []Subroute{{Pattern: "../escape"}}},
	}
	for _, p := range invalid {
		m.Pages[0] = p
		if err := m.Validate(); err == nil {
			t.Fatalf("invalid subroutes accepted: %+v", p.Subroutes)
		}
	}
}

func TestNativePageRouteSlots(t *testing.T) {
	m := Manifest{Protocol: Protocol, ID: "sample", Name: "Sample", Version: "1", Executable: "backend", Pages: []Page{{ID: "spaces", Route: "workspace", Title: "Workspace", Scope: "global", Entry: "main.js"}}}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"admin", "user", "api", "projects", "../workspace", "/workspace", "workspace/child"} {
		m.Pages[0].Route = route
		if err := m.Validate(); err == nil {
			t.Fatalf("unsupported public route accepted: %q", route)
		}
	}
	m.Pages[0].Route = "workspace"
	m.Pages = append(m.Pages, Page{ID: "second", Route: "workspace", Title: "Second", Scope: "global", Entry: "main.js"})
	if err := m.Validate(); err == nil {
		t.Fatal("duplicate native route accepted")
	}
	m.Pages[1].Route = ""
	if err := m.Validate(); err != nil {
		t.Fatal("API-only contribution rejected:", err)
	}
	// One repository page owns several named routes.
	m.Pages[1] = Page{
		ID: "places", Route: "feed", Title: "Feed", Scope: "repository", Permission: "read", Entry: "main.js",
		Routes:    []PageRoute{{Route: "requests", Title: "Requests"}, {Route: "terminals", Title: "Terminals", Persist: true}, {Route: "issue-factory", Title: "Factory"}},
		Subroutes: []Subroute{{Route: "requests", Pattern: "new"}, {Route: "requests", Pattern: "{request}"}},
	}
	if err := m.Validate(); err != nil {
		t.Fatal("named repository routes rejected:", err)
	}
	if !m.Pages[1].Serves("terminals") || !m.Pages[1].RoutePersists("terminals") || m.Pages[1].RoutePersists("feed") || m.Pages[1].RouteTitle("requests") != "Requests" {
		t.Fatal("named route facts are wrong")
	}
	if _, _, ok := m.Pages[1].MatchSubroute("requests", "abc"); !ok {
		t.Fatal("subroute under a named route refused")
	}
	if _, _, ok := m.Pages[1].MatchSubroute("feed", "abc"); ok {
		t.Fatal("subroute matched under the wrong route")
	}
	for name, mutate := range map[string]func(p *Page){
		"unknown route":          func(p *Page) { p.Routes = append(p.Routes, PageRoute{Route: "projects"}) },
		"duplicate route":        func(p *Page) { p.Routes = append(p.Routes, PageRoute{Route: "feed"}) },
		"settings without admin": func(p *Page) { p.Routes = append(p.Routes, PageRoute{Route: "settings"}) },
		"unserved subroute":      func(p *Page) { p.Subroutes = append(p.Subroutes, Subroute{Route: "app", Pattern: "x"}) },
	} {
		page := m.Pages[1]
		page.Routes = append([]PageRoute(nil), page.Routes...)
		page.Subroutes = append([]Subroute(nil), page.Subroutes...)
		mutate(&page)
		candidate := m
		candidate.Pages = []Page{m.Pages[0], page}
		if err := candidate.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

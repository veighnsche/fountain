// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const Protocol = 1

var slug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func ValidID(id string) bool { return slug.MatchString(id) }

type Manifest struct {
	Protocol           int      `json:"protocol"`
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Version            string   `json:"version"`
	Executable         string   `json:"executable"`
	Pages              []Page   `json:"pages,omitempty"`
	Panels             []Panel  `json:"panels,omitempty"`
	Capabilities       []string `json:"capabilities,omitempty"`
	Policies           []string `json:"policies,omitempty"`
	PreferredWorkspace bool     `json:"preferred_workspace,omitempty"`
	// LifecycleController names the required package that controls this
	// package's external effects. It is omitted for packages with no
	// external effects. Catalog and extracted manifest must agree on it
	// exactly; the required-set and controller-readiness checks that need
	// release configuration stay with the manager.
	LifecycleController string `json:"lifecycle_controller,omitempty"`
}

type Page struct {
	ID         string `json:"id"`
	Route      string `json:"route,omitempty"`
	Title      string `json:"title"`
	Scope      string `json:"scope"`
	Entry      string `json:"entry"`
	Permission string `json:"permission,omitempty"`
	// Owner links this page to exactly one panel as one shared retained
	// owner (NFX-C04/H-OWNER). The canonical owner identity is the pair
	// (extension ID, owner ID); both modes share the page entry and the
	// page API identity. Empty means an ordinary independent page.
	Owner string `json:"owner,omitempty"`
	// Routes are further host-owned slots served by this same page, in
	// addition to Route. One page owning several named routes keeps one
	// page identity (extension, page, API base), so moving between its
	// routes readopts the same retained root instead of remounting.
	Routes []PageRoute `json:"routes,omitempty"`
	// Persist asks the host to keep this page's retained root in the
	// shelter across other pages: it is never evicted by the shelter
	// bound. Routes and subroutes may also set it; the host marks the
	// root persistent once any persistent route has been entered.
	Persist bool `json:"persist,omitempty"`
	// Subroutes are bounded subordinate content routes declared under
	// Route, or under one of Routes when Subroute.Route names it
	// (NFX-C04/H-ROUTE). The host enforces only their bounded shape and
	// conflicts; parameter meaning and any product-ID validation stay
	// with the consumer backend.
	Subroutes []Subroute `json:"subroutes,omitempty"`
}

// PageRoute is one further native slot served by a page.
type PageRoute struct {
	Route   string `json:"route"`
	Title   string `json:"title,omitempty"`
	Persist bool   `json:"persist,omitempty"`
	// Hidden serves the route without a navigation link, e.g. an old
	// address the page forwards in the browser.
	Hidden bool `json:"hidden,omitempty"`
}

// RouteHidden reports whether the route has no navigation link.
func (p Page) RouteHidden(route string) bool {
	for _, r := range p.Routes {
		if r.Route == route {
			return r.Hidden
		}
	}
	return false
}

type Panel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Entry string `json:"entry"`
	// Owner links this panel to exactly one page; see Page.Owner.
	Owner string `json:"owner,omitempty"`
}

// Subroute is one declared subordinate route under a page Route. Pattern
// is 1-3 segments; each segment is a slug literal or a single "{name}"
// parameter capturing one bounded opaque segment.
type Subroute struct {
	// Route names the page route this subroute sits under; empty means
	// the page's primary Route.
	Route   string `json:"route,omitempty"`
	Pattern string `json:"pattern"`
	Title   string `json:"title,omitempty"`
	Persist bool   `json:"persist,omitempty"`
}

// RouteNames lists every native slot the page serves, primary first.
func (p Page) RouteNames() []string {
	if p.Route == "" {
		return nil
	}
	names := []string{p.Route}
	for _, r := range p.Routes {
		names = append(names, r.Route)
	}
	return names
}

// Serves reports whether the page serves the named native slot.
func (p Page) Serves(route string) bool {
	return route != "" && slices.Contains(p.RouteNames(), route)
}

// RouteTitle is the page title shown on one of its routes.
func (p Page) RouteTitle(route string) string {
	for _, r := range p.Routes {
		if r.Route == route && r.Title != "" {
			return r.Title
		}
	}
	return p.Title
}

// RoutePersists reports whether entering the route marks the root persistent.
func (p Page) RoutePersists(route string) bool {
	if p.Persist {
		return true
	}
	for _, r := range p.Routes {
		if r.Route == route {
			return r.Persist
		}
	}
	return false
}

// SubrouteRoute is the page route a subroute sits under.
func (p Page) SubrouteRoute(sub Subroute) string {
	if sub.Route == "" {
		return p.Route
	}
	return sub.Route
}

// LoadManifest reads and validates an extension package without executing it.
func LoadManifest(packageDir string) (Manifest, error) {
	f, err := os.Open(filepath.Join(packageDir, "extension.json"))
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	const maximumManifestBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(f, maximumManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read extension manifest: %w", err)
	}
	if len(data) > maximumManifestBytes {
		return Manifest{}, errors.New("extension manifest exceeds size limit")
	}
	var manifest Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode extension manifest: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("extension manifest must contain one JSON object")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.Protocol != Protocol {
		return fmt.Errorf("unsupported extension protocol %d", m.Protocol)
	}
	if !slug.MatchString(m.ID) || strings.TrimSpace(m.Name) == "" || !validPackageVersion(m.Version) {
		return errors.New("extension id, name, or version is invalid")
	}
	if !safeRelative(m.Executable) {
		return errors.New("extension executable path is invalid")
	}
	if err := validateNames(m.Capabilities, catalogCapabilities(), "capability"); err != nil {
		return err
	}
	if m.LifecycleController != "" {
		if !ValidID(m.LifecycleController) || len(m.LifecycleController) > maximumPackageIDBytes {
			return fmt.Errorf("extension %q has an invalid lifecycle controller", m.ID)
		}
		if m.LifecycleController == m.ID {
			return fmt.Errorf("extension %q cannot control itself", m.ID)
		}
	}
	if err := validateNames(m.Policies, map[string]bool{PolicyForgejoUsername: true}, "policy"); err != nil {
		return err
	}
	seenPages := make(map[string]bool)
	seenRoutes := make(map[string]bool)
	for _, p := range m.Pages {
		if !slug.MatchString(p.ID) || seenPages[p.ID] || strings.TrimSpace(p.Title) == "" || !assetEntry(p.Entry) {
			return fmt.Errorf("invalid or duplicate page %q", p.ID)
		}
		if p.Owner != "" && !slug.MatchString(p.Owner) {
			return fmt.Errorf("page %q has invalid shared owner %q", p.ID, p.Owner)
		}
		if err := validateSubroutes(p); err != nil {
			return err
		}
		seenPages[p.ID] = true
		if p.Route == "" && len(p.Routes) > 0 {
			return fmt.Errorf("page %q declares routes without a route", p.ID)
		}
		if len(p.Routes) > 8 {
			return fmt.Errorf("page %q declares too many routes", p.ID)
		}
		for _, r := range p.Routes {
			if len(r.Title) > 128 || strings.TrimSpace(r.Title) != r.Title {
				return fmt.Errorf("page %q route %q has invalid title", p.ID, r.Route)
			}
		}
		for _, route := range p.RouteNames() {
			if !slices.Contains(NativePageRoutes(p.Scope), route) || seenRoutes[p.Scope+"/"+route] {
				return fmt.Errorf("invalid or duplicate native page route %q", route)
			}
			seenRoutes[p.Scope+"/"+route] = true
			if p.Scope == "repository" && route == RepositorySettingsRoute && p.Permission != "admin" {
				return fmt.Errorf("repository page %q serves settings without admin permission", p.ID)
			}
		}
		switch p.Scope {
		case "global":
			if p.Permission != "" {
				return fmt.Errorf("global page %q must not declare a permission", p.ID)
			}
		case "user":
			if p.Permission != "user" {
				return fmt.Errorf("user page %q requires user permission", p.ID)
			}
		case "admin":
			if p.Permission != "admin" {
				return fmt.Errorf("admin page %q requires admin permission", p.ID)
			}
		case "repository":
			if p.Permission != "read" && p.Permission != "write" && p.Permission != "admin" {
				return fmt.Errorf("repository page %q has invalid permission", p.ID)
			}
		default:
			return fmt.Errorf("page %q has invalid scope", p.ID)
		}
	}
	seenPanels := make(map[string]bool)
	for _, p := range m.Panels {
		if !slug.MatchString(p.ID) || seenPanels[p.ID] || strings.TrimSpace(p.Title) == "" || !assetEntry(p.Entry) {
			return fmt.Errorf("invalid or duplicate panel %q", p.ID)
		}
		if p.Owner != "" && !slug.MatchString(p.Owner) {
			return fmt.Errorf("panel %q has invalid shared owner %q", p.ID, p.Owner)
		}
		seenPanels[p.ID] = true
	}
	return validateOwners(m)
}

// validateOwners enforces the linked page/panel owner contract: each
// declared owner names exactly one global routed page and one panel
// sharing one entry, so the host can select a single retained instance
// with one canonical entry/API identity.
func validateOwners(m Manifest) error {
	pages := make(map[string]Page)
	for _, p := range m.Pages {
		if p.Owner == "" {
			continue
		}
		if p.Scope != "global" {
			return fmt.Errorf("shared owner page %q must use global scope", p.ID)
		}
		if p.Route == "" {
			return fmt.Errorf("shared owner page %q must declare a route", p.ID)
		}
		if _, dup := pages[p.Owner]; dup {
			return fmt.Errorf("shared owner %q links more than one page", p.Owner)
		}
		pages[p.Owner] = p
	}
	panels := make(map[string]Panel)
	for _, p := range m.Panels {
		if p.Owner == "" {
			continue
		}
		if _, dup := panels[p.Owner]; dup {
			return fmt.Errorf("shared owner %q links more than one panel", p.Owner)
		}
		panels[p.Owner] = p
	}
	for owner, page := range pages {
		panel, ok := panels[owner]
		if !ok {
			return fmt.Errorf("shared owner %q has no linked panel", owner)
		}
		if panel.Entry != page.Entry {
			return fmt.Errorf("shared owner %q page and panel entries differ", owner)
		}
	}
	for owner := range panels {
		if _, ok := pages[owner]; !ok {
			return fmt.Errorf("shared owner %q has no linked page", owner)
		}
	}
	return nil
}

func validateSubroutes(p Page) error {
	if len(p.Subroutes) == 0 {
		return nil
	}
	if p.Route == "" {
		return fmt.Errorf("page %q declares subroutes without a route", p.ID)
	}
	if len(p.Subroutes) > 16 {
		return fmt.Errorf("page %q declares too many subroutes", p.ID)
	}
	seen := make(map[string]bool)
	for _, sub := range p.Subroutes {
		key := p.SubrouteRoute(sub) + "/" + sub.Pattern
		if sub.Pattern == "" || len(sub.Pattern) > 128 || seen[key] {
			return fmt.Errorf("page %q has invalid or duplicate subroute %q", p.ID, sub.Pattern)
		}
		if !p.Serves(p.SubrouteRoute(sub)) {
			return fmt.Errorf("page %q subroute %q names a route the page does not serve", p.ID, sub.Pattern)
		}
		seen[key] = true
		if len(sub.Title) > 128 || strings.TrimSpace(sub.Title) != sub.Title {
			return fmt.Errorf("page %q subroute %q has invalid title", p.ID, sub.Pattern)
		}
		segments := strings.Split(sub.Pattern, "/")
		if len(segments) > 3 {
			return fmt.Errorf("page %q subroute %q is too deep", p.ID, sub.Pattern)
		}
		params := make(map[string]bool)
		for _, segment := range segments {
			if strings.HasPrefix(segment, "{") || strings.HasSuffix(segment, "}") {
				name, ok := strings.CutPrefix(segment, "{")
				name, closed := strings.CutSuffix(name, "}")
				if !ok || !closed || segment != "{"+name+"}" || !slug.MatchString(name) || params[name] {
					return fmt.Errorf("page %q subroute %q has invalid parameter", p.ID, sub.Pattern)
				}
				params[name] = true
				continue
			}
			// Literals are subordinate segments only: native private
			// ("-") and account namespaces stay unreachable because a
			// subroute can never escape its page route prefix.
			if !slug.MatchString(segment) {
				return fmt.Errorf("page %q subroute %q has invalid segment %q", p.ID, sub.Pattern, segment)
			}
		}
	}
	return nil
}

// OwnerContributions returns the linked page and panel for a declared
// shared owner, or false when the owner is not declared.
func (m Manifest) OwnerContributions(owner string) (Page, Panel, bool) {
	var page Page
	var panel Panel
	foundPage, foundPanel := false, false
	for _, p := range m.Pages {
		if p.Owner == owner && owner != "" {
			page, foundPage = p, true
		}
	}
	for _, p := range m.Panels {
		if p.Owner == owner && owner != "" {
			panel, foundPanel = p, true
		}
	}
	return page, panel, foundPage && foundPanel
}

// MatchSubroute matches a subordinate path suffix (no leading slash,
// one optional trailing slash) under one of the page's routes against
// the subroutes declared for that route. Parameter values are bounded
// opaque segments: the host deliberately applies no product-ID shape
// rule here.
func (p Page) MatchSubroute(route, suffix string) (Subroute, map[string]string, bool) {
	suffix = strings.TrimSuffix(suffix, "/")
	if suffix == "" {
		return Subroute{}, nil, false
	}
	segments := strings.Split(suffix, "/")
	for _, sub := range p.Subroutes {
		if p.SubrouteRoute(sub) != route {
			continue
		}
		pattern := strings.Split(sub.Pattern, "/")
		if len(pattern) != len(segments) {
			continue
		}
		params := make(map[string]string)
		matched := true
		for i, segment := range pattern {
			if strings.HasPrefix(segment, "{") {
				value := segments[i]
				if value == "" || len(value) > 255 {
					matched = false
					break
				}
				params[segment[1:len(segment)-1]] = value
				continue
			}
			if segment != segments[i] {
				matched = false
				break
			}
		}
		if matched {
			return sub, params, true
		}
	}
	return Subroute{}, nil, false
}

// Repository slots whose address differs from their name.
const (
	// RepositorySettingsRoute renders inside the native repository
	// settings layout at /{owner}/{repo}/settings/sodaos, with a sidebar
	// entry; it requires admin permission.
	RepositorySettingsRoute = "settings"
	// RepositoryIssueFactoryRoute is /{owner}/{repo}/issues/{index}/factory.
	RepositoryIssueFactoryRoute = "issue-factory"
)

// NativePageRoutes are host-owned contribution slots, never package namespaces.
// An empty Page.Route contributes an API without adding a public page.
func NativePageRoutes(scope string) []string {
	switch scope {
	case "global":
		return []string{"workspace", "feed", "applications", "tailnet", "requests"}
	case "user":
		return []string{"workspace", "identity", "development-keys"}
	case "admin":
		return []string{"workspace", "sodaos"}
	case "repository":
		return []string{"workspace", "feed", "requests", "app", "terminals", "knowledge", RepositoryIssueFactoryRoute, RepositorySettingsRoute}
	}
	return nil
}

// RepositoryRoutePath is the address of a repository slot below
// /{owner}/{repo}, without a leading slash.
func RepositoryRoutePath(route string) string {
	switch route {
	case RepositorySettingsRoute:
		return "settings/sodaos"
	case RepositoryIssueFactoryRoute:
		return "issues/{index}/factory"
	}
	return route
}

func validateNames(names []string, allowed map[string]bool, kind string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !allowed[name] || seen[name] {
			return fmt.Errorf("invalid or duplicate %s %q", kind, name)
		}
		seen[name] = true
	}
	return nil
}

func safeRelative(name string) bool {
	return name != "" && !filepath.IsAbs(name) && !strings.Contains(name, `\`) &&
		path.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

func assetEntry(name string) bool {
	return safeRelative(name) && strings.HasSuffix(name, ".js")
}

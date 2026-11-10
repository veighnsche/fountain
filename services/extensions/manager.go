// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"
	"forgejo.org/modules/log"

	"github.com/gofrs/flock"
	"github.com/hashicorp/go-hclog"
	plugin "github.com/hashicorp/go-plugin"
)

// Descriptor contains a defensive manifest copy and the package directory.
// Root is for the core asset handler; it must never be sent to browsers.
type Descriptor struct {
	Manifest   sdk.Manifest
	Root       string
	InstanceID string
}

type running struct {
	descriptor     Descriptor
	client         *plugin.Client
	transport      *http.Transport
	runtimeDir     string
	callbackServer *http.Server
	installation   string
}

type Manager struct {
	validateManifest       func(sdk.Manifest) error
	root                   string
	requiredIDs            []string
	mu                     sync.Mutex
	running                map[string]*running
	started                bool
	env                    []string
	lock                   *flock.Flock
	callbackHandlerFactory func(instanceID string) http.Handler
	instanceStopped        func(instanceID string)
	serviceCallbackPath    string
	serviceCallbackHandler http.Handler
	serviceCallback        *serviceCallbackEndpoint
	background             *backgroundRegistry
	serviceBridgePeers     map[uint32]string
	// store is the manager's lifetime package-store handle, bound to the
	// held package lock so live mutations share the CLI's validation and
	// rename code. Set in Start; F03 owns mutation dispatch and ordering.
	store *packages.PackageStore
	// lastErr carries advisory in-memory failure text for interrupted
	// operations. It is never persisted: intents carry only the
	// interruption marker. Guarded by errMu, never m.mu.
	errMu   sync.Mutex
	lastErr map[string]*packages.LifecycleError
	// Live lifecycle state: busy serializes one operation per package,
	// draining closes new admission during teardown, and the catalog is
	// the release's trusted package set loaded once before startup.
	// Guarded by mu.
	busy     map[string]string
	draining map[string]bool
	// F03 controller dispatch: mutation is the single global package
	// mutation slot ("" when free), mutationPhase tracks the active
	// command's phase for the failed-enable cleanup exception, and
	// controllers records required-controller readiness by package ID.
	// Guarded by mu; no HTTP, download, process wait, or callback runs
	// while holding it.
	mutation       string
	mutationPhase  string
	mutationTokens []string
	controllers    map[string]controllerState
	// catalogPath configures the release-supplied trusted catalog file;
	// catalog and catalogSHA load once in Start. maintenanceDir
	// configures the shared directory-inode exclusion; empty means
	// standalone Fountain with no guard.
	catalogPath    string
	catalog        []sdk.CatalogPackage
	catalogSHA     string
	maintenanceDir string
	// lifeCtx bounds manager-owned background work: the accepted admin
	// mutation task and the startup optional-reconciliation pass. It is
	// cancelled in Close. Browser disconnect never cancels these tasks;
	// Close cancels and joins the active mutation before the package
	// store unlocks. Guarded by mu for assignment; cancelled once.
	lifeCtx    context.Context
	lifeCancel context.CancelFunc
	// activeOp tracks the single manager-owned mutation task, if any.
	// A second adoption while it is set gets operation_in_progress.
	// Guarded by mu; done closes when the task ends.
	activeOp *activeOperation
	// acceptMu excludes Close from durable adoption; concurrent requests
	// use TryLock and remain conflicts rather than queued mutations.
	acceptMu sync.Mutex
	// startupDone closes when the bounded startup optional pass ends.
	// Guarded by mu; nil when no pass is running.
	startupDone chan struct{}
	// lastObs journals the latest controller-confirmed external effect
	// per controlled package: state, operational grant, and the exact
	// call identity it was observed under. State reads consult this
	// journal only and never call the controller or host. Guarded by
	// obsMu, never m.mu.
	obsMu   sync.Mutex
	lastObs map[string]controlledEffect
	// recoveryBlocked marks optional packages whose interrupted-swap
	// repair failed at startup (RCX-2 FX04). Their bytes stay
	// unavailable and are never validated or executed until an
	// explicit repair replaces them. Set once in Start; guarded by
	// mu.
	recoveryBlocked map[string]bool
}

// activeOperation is one manager-owned mutation task: its cancel stops
// the bounded work and done closes when the task has fully ended,
// including intent settlement. Close cancels and waits on done before
// releasing the package-store lock.
type activeOperation struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
}

// controlledEffect is the last controller-reported external fact for one
// package. Generation, installation and controller instance bind it to
// the current intent; anything else is stale and reads as unconfirmed.
type controlledEffect struct {
	state        string
	admission    bool
	generation   string
	installation string
	instanceID   string
}

// controllerState is one required controller's readiness: its current
// native instance ID and whether its capability registration agreed.
type controllerState struct {
	instanceID string
	ready      bool
}

var defaultManager atomic.Pointer[Manager]

var errServiceCallbackUnconfigured = errors.New("service callback configuration failed")

func SetDefault(manager *Manager) {
	defaultManager.Store(manager)
	if manager == nil {
		packages.SetPolicyRuntime(nil)
	} else {
		packages.SetPolicyRuntime(manager)
	}
}
func GetManager() *Manager { return defaultManager.Load() }

func NewManager(root string, requiredIDs ...string) *Manager {
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	return &Manager{root: root, requiredIDs: append([]string(nil), requiredIDs...), running: make(map[string]*running), background: newBackgroundRegistry(), busy: make(map[string]string), draining: make(map[string]bool), lastErr: make(map[string]*packages.LifecycleError), controllers: make(map[string]controllerState), lifeCtx: lifeCtx, lifeCancel: lifeCancel, lastObs: make(map[string]controlledEffect)}
}

// SetCatalogPath configures the release-supplied trusted catalog file,
// loaded once in Start. It must be configured before Start.
func (m *Manager) SetCatalogPath(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("extension manager already started")
	}
	m.catalogPath = path
	return nil
}

// SetMaintenanceDir configures the shared directory-inode exclusion.
// Empty means standalone Fountain with no guard. It must be configured
// before Start.
func (m *Manager) SetMaintenanceDir(dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("extension manager already started")
	}
	m.maintenanceDir = dir
	return nil
}

// SetCallbackHandlerFactory installs the host capability handler used by
// extensions that declare native callback capabilities. It must be configured
// before Start.
func (m *Manager) SetCallbackHandlerFactory(factory func(instanceID string) http.Handler) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("extension manager already started")
	}
	m.callbackHandlerFactory = factory
	return nil
}

// SetManifestValidator installs host-specific admission checks, including
// account route reservations, for both startup and later activation.
func (m *Manager) SetManifestValidator(validate func(sdk.Manifest) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("extension manager already started")
	}
	m.validateManifest = validate
	return nil
}

// SetInstanceStopped installs a callback to revoke admissions owned by an
// instance. The callback must not call back into Manager: it runs under m.mu.
func (m *Manager) SetInstanceStopped(callback func(instanceID string)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("extension manager already started")
	}
	m.instanceStopped = callback
	return nil
}

// Start loads the trusted catalog once, bootstraps and starts required
// packages synchronously, then restores desired optional backends
// asynchronously after core readiness. Required-first: an
// optional backend, download, or controller call never blocks or fails
// core startup. Optional package failure closes only that package's
// admission and is reconciled by the bounded startup pass; deterministic
// host-configuration errors (route claims, service-callback setup) still
// fail startup with no partial state. m.mu is held only for brief
// map/slot snapshots, never across downloads, handshakes, or callbacks.
func (m *Manager) Start(ctx context.Context) (err error) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("extension manager already started")
	}
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		m.mu.Unlock()
		return errors.New("extension package directory unavailable")
	}
	// The trusted catalog loads once for the manager lifetime. A
	// configured missing or malformed file is catalog-unavailable, never
	// an empty catalog; empty is permitted only with no required IDs.
	catalog, sha, err := packages.LoadCatalogFile(m.catalogPath)
	if err != nil {
		m.mu.Unlock()
		return errors.New("extension catalog is unavailable")
	}
	// The required set must equal the configured required IDs exactly,
	// and lifecycle controllers must name a required package without
	// cycles. A required package with no catalog configured fails
	// closed here; only the zero-extension layout (no required IDs,
	// no catalog) starts without one.
	if err := packages.ValidateCatalogSelection(catalog, m.requiredIDs); err != nil {
		m.mu.Unlock()
		return errors.New("extension catalog does not match configuration")
	}
	m.catalog = catalog.Packages
	m.catalogSHA = sha
	// A fresh lifetime owns this run's background work: an earlier
	// Close cancelled the previous one, and a second Start must not
	// inherit a dead context.
	if m.lifeCancel != nil {
		m.lifeCancel()
	}
	m.lifeCtx, m.lifeCancel = context.WithCancel(context.Background())
	m.activeOp = nil
	m.startupDone = nil
	m.recoveryBlocked = nil
	m.lock, err = packages.AcquirePackageLock(m.root)
	if err != nil {
		m.mu.Unlock()
		return errors.New("extension package lock unavailable")
	}
	m.store, err = packages.PackageStoreFromLock(m.root, m.lock)
	if err != nil {
		_ = m.lock.Unlock()
		m.lock = nil
		m.mu.Unlock()
		return errors.New("extension package store unavailable")
	}
	if err := m.startServiceCallback(); err != nil {
		_ = m.lock.Unlock()
		m.lock = nil
		m.store = nil
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()

	fail := func(err error) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.stopAll()
		m.closeServiceCallback()
		_ = m.lock.Unlock()
		m.lock = nil
		m.store = nil
		return err
	}

	// Repair interrupted package swaps before any validation,
	// acquisition or execution (RCX-2 FX04): the store handle runs
	// the exact per-package repair for known IDs — required IDs,
	// trusted catalog IDs, and intent/installation-owned IDs — under
	// the lifetime lock. A required repair failure blocks required
	// readiness; an optional failure isolates that package with a
	// visible failure, and its bytes are never validated or
	// executed. Current catalog and receipt checks below re-run
	// after this repair, so restored older bytes never authorize an
	// obsolete package.
	requiredSet := make(map[string]bool, len(m.requiredIDs))
	for _, id := range m.requiredIDs {
		requiredSet[id] = true
	}
	if failures := m.store.RecoverInterrupted(packages.RecoveryCandidates(m.root, m.catalog, m.requiredIDs)); len(failures) != 0 {
		for _, id := range sortedFailureIDs(failures) {
			if requiredSet[id] {
				return fail(fmt.Errorf("required extension %q package repair failed: %v", id, failures[id]))
			}
			m.recordFailure(id, packages.LifecycleErrUnavailable, "interrupted package repair failed; explicit repair is required")
			if m.recoveryBlocked == nil {
				m.recoveryBlocked = make(map[string]bool)
			}
			m.recoveryBlocked[id] = true
		}
	}
	// Reject contradictory required intent before acquisition or replacement.
	for _, id := range m.requiredIDs {
		intent, err := packages.LoadIntent(m.root, id)
		if err != nil {
			return fail(fmt.Errorf("required extension %q requested state unavailable", id))
		}
		if intent.Generation != "0" && !intent.DesiredEnabled {
			return fail(fmt.Errorf("required extension %q is disabled", id))
		}
	}
	// Required bootstrap reuses exact installed receipts first, the
	// verified archive cache second, and bounded HTTPS acquisition only
	// when neither is available, so exact cached packages reboot
	// offline. There is no separate bootstrap image and no bundled
	// core package. It preserves installation UUIDs and writes no
	// intents itself.
	if len(m.requiredIDs) != 0 {
		requiredCatalog := sdk.Catalog{Version: sdk.CatalogVersion, Packages: append([]sdk.CatalogPackage(nil), m.catalog...)}
		if err := packages.BootstrapRequired(ctx, m.store, requiredCatalog, m.requiredIDs, nil); err != nil {
			return fail(fmt.Errorf("required extension bootstrap failed: %v", err))
		}
	}

	entries, err := os.ReadDir(m.root)
	if err != nil {
		return fail(errors.New("cannot list extension packages"))
	}
	required := make(map[string]bool, len(m.requiredIDs))
	for _, id := range m.requiredIDs {
		if !sdk.ValidID(id) || required[id] {
			return fail(errors.New("invalid or duplicate required extension id"))
		}
		required[id] = false
	}
	for _, entry := range entries {
		if _, ok := required[entry.Name()]; !ok {
			continue
		}
		if !entry.IsDir() {
			return fail(fmt.Errorf("required extension %q is not a package directory", entry.Name()))
		}
		required[entry.Name()] = true
	}
	for _, id := range m.requiredIDs {
		if !required[id] {
			return fail(fmt.Errorf("required extension %q is missing", id))
		}
	}
	claimedRoutes := make(map[string]bool)
	claimRoutes := func(manifest sdk.Manifest) error {
		for _, page := range manifest.Pages {
			if page.Route == "" {
				continue
			}
			key := page.Scope + "/" + page.Route
			if claimedRoutes[key] {
				return fmt.Errorf("native page route %q is already claimed", key)
			}
			claimedRoutes[key] = true
			for _, sub := range page.Subroutes {
				subKey := key + "/" + sub.Pattern
				if claimedRoutes[subKey] {
					return fmt.Errorf("native page route %q is already claimed", subKey)
				}
				claimedRoutes[subKey] = true
			}
		}
		return nil
	}
	// Validate static host claims without starting optional processes. A slow
	// optional handshake must never sit on the core-readiness path.
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(id, ".") {
			continue
		}
		if m.recoveryIsolated(id) {
			// Unrepairable optional bytes stay isolated: never
			// validated nor executed until an explicit repair
			// replaces them.
			continue
		}
		if !sdk.ValidID(id) {
			return fail(errors.New("invalid extension package directory name"))
		}
		if !slices.Contains(m.requiredIDs, id) {
			intent, err := packages.LoadIntent(m.root, id)
			if err != nil || !intent.DesiredEnabled {
				continue
			}
		}
		manifest, err := sdk.LoadManifest(filepath.Join(m.root, id))
		if err != nil || manifest.ID != id {
			continue // Required validation below is fatal; optional failure is isolated.
		}
		if slices.Contains(manifest.Capabilities, sdk.CapabilityServiceBridge) && m.serviceCallback == nil {
			return fail(errServiceCallbackUnconfigured)
		}
		if m.validateManifest != nil {
			if err := m.validateManifest(manifest); err != nil {
				return fail(err)
			}
		}
		if err := claimRoutes(manifest); err != nil {
			return fail(err)
		}
	}
	registerRunning := func(item *running) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.running[item.descriptor.Manifest.ID] = item
		m.noteControllerStarted(item.descriptor)
	}
	// Required packages start by configuration, in order. Any required
	// failure is fatal: core never reports ready without them.
	for _, id := range m.requiredIDs {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		item, err := m.startOne(filepath.Join(m.root, id), id)
		if err != nil {
			log.Error("Extension %s startup failed: %v", id, err)
			return fail(fmt.Errorf("start extension %q: %w", id, err))
		}
		registerRunning(item)
		log.Info("Extension %s started", id)
		go m.observeExit(id, item)
	}
	m.mu.Lock()
	m.started = true
	m.startupDone = make(chan struct{})
	startupDone, lifeCtx := m.startupDone, m.lifeCtx
	m.mu.Unlock()
	// Optional lifecycle effects (controller activation, residual
	// stops, grant refresh) reconcile on the bounded schedule only
	// after Start has released its mutex and returned core readiness.
	go m.runStartupOptionalPass(lifeCtx, startupDone)
	return nil
}

// sortedFailureIDs orders repair failures deterministically for
// startup reporting.
func sortedFailureIDs(failures map[string]error) []string {
	ids := make([]string, 0, len(failures))
	for id := range failures {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// recoveryIsolated reports whether an optional package's startup
// repair failed: its bytes stay unavailable and are never validated
// or executed on startup paths.
func (m *Manager) recoveryIsolated(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoveryBlocked[id]
}

func (m *Manager) startOne(packageDir, dirName string) (_ *running, err error) {
	phase := "package validation"
	defer func() {
		if err != nil && !errors.Is(err, errServiceCallbackUnconfigured) {
			err = errors.New(phase + " failed")
		}
	}()
	manifest, err := sdk.LoadManifest(packageDir)
	if err != nil {
		return nil, err
	}
	if manifest.ID != dirName {
		return nil, errors.New("package directory must match manifest id")
	}
	if m.validateManifest != nil {
		if err := m.validateManifest(manifest); err != nil {
			return nil, err
		}
	}
	// The installer assigns installation identity; start only re-reads the
	// recorded UUID so restart and replacement preserve it.
	installation, err := packages.EnsureInstallation(m.root, manifest.ID)
	if err != nil {
		return nil, err
	}
	if slices.Contains(manifest.Capabilities, sdk.CapabilityServiceBridge) && m.serviceCallback == nil {
		return nil, errServiceCallbackUnconfigured
	}
	executable := filepath.Join(packageDir, filepath.FromSlash(manifest.Executable))
	phase = "executable validation"
	info, err := os.Lstat(executable)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("extension executable must be a regular executable file")
	}
	dataDir := filepath.Join(m.root, ".data", manifest.ID)
	phase = "runtime directory setup"
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	runtimeRoot := filepath.Join(m.root, ".runtime")
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		return nil, err
	}
	runtimeDir, err := os.MkdirTemp(runtimeRoot, "r")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(runtimeDir)
		}
	}()
	instanceBytes := make([]byte, 32)
	if _, err := rand.Read(instanceBytes); err != nil {
		return nil, err
	}
	instanceID := base64.RawURLEncoding.EncodeToString(instanceBytes)
	var callbackServer *http.Server
	if needsNativeCallback(manifest.Capabilities) {
		if m.callbackHandlerFactory == nil {
			return nil, errors.New("native callback handler is unavailable")
		}
		handler := m.callbackHandlerFactory(instanceID)
		if handler == nil {
			return nil, errors.New("native callback handler is unavailable")
		}
		callbackSocket := filepath.Join(runtimeDir, "c")
		rawListener, listenErr := net.Listen("unix", callbackSocket)
		if listenErr != nil {
			return nil, listenErr
		}
		listener := wrapPeerCredentialListener(rawListener)
		if chmodErr := os.Chmod(callbackSocket, 0o600); chmodErr != nil {
			_ = listener.Close()
			return nil, chmodErr
		}
		callbackServer = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if serveErr := callbackServer.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				log.Error("Extension native callback server failed: %v", serveErr)
			}
		}()
		defer func() {
			if err != nil {
				_ = callbackServer.Close()
			}
		}()
	}
	socket := filepath.Join(runtimeDir, "s")
	cmd := exec.Command(executable)
	cmd.Dir = packageDir
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dataDir,
		"TMPDIR=" + runtimeDir,
		sdk.SocketEnv + "=" + socket,
		sdk.DataEnv + "=" + dataDir,
	}, m.env...)
	if needsNativeCallback(manifest.Capabilities) {
		cmd.Env = append(cmd.Env, sdk.CallbackEnv+"="+filepath.Join(runtimeDir, "c"))
	}
	handshake, plugins := sdk.ClientContract()
	phase = "process handshake"
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshake,
		Plugins:         plugins,
		Cmd:             cmd,
		StartTimeout:    10 * time.Second,
		SkipHostEnv:     true,
		Logger:          hclog.NewNullLogger(),
	})
	defer func() {
		if err != nil {
			client.Kill()
		}
	}()
	protocol, err := client.Client()
	if err != nil {
		return nil, err
	}
	control, err := protocol.Dispense("control")
	if err != nil {
		return nil, err
	}
	phase = "control readiness"
	if err = control.(interface{ Ready() error }).Ready(); err != nil {
		return nil, err
	}
	phase = "runtime registration"
	registration, err := control.(interface {
		Registration() (sdk.Registration, error)
	}).Registration()
	if err != nil {
		return nil, err
	}
	if registration.AuthorizesContribution != slices.Contains(manifest.Capabilities, sdk.CapabilityContributionAuthorize) || len(registration.Policies) != len(manifest.Policies) {
		return nil, errors.New("extension runtime registration does not match manifest")
	}
	// A declared lifecycle capability requires the handler
	// registration, and a handler registration requires the declared
	// capability. A controller that is missing or unready is a failure,
	// never an implicit no-op hook.
	if err := sdk.ValidateLifecycleRegistration(manifest, registration); err != nil {
		return nil, err
	}
	for _, policy := range manifest.Policies {
		if !slices.Contains(registration.Policies, policy) {
			return nil, errors.New("extension runtime registration does not match manifest")
		}
	}
	if slices.Contains(manifest.Capabilities, sdk.CapabilityBackgroundOperations) {
		phase = "background admission"
		admission, issueErr := m.IssueRuntimeAdmission(manifest.ID, installation, instanceID, manifest.Capabilities)
		if issueErr != nil {
			return nil, issueErr
		}
		deliverer, ok := control.(interface {
			DeliverBackgroundAdmission(sdk.BackgroundAdmissionDelivery) error
		})
		if !ok {
			m.RevokeBackgroundForInstance(instanceID)
			return nil, errors.New("extension control cannot receive background admission")
		}
		if err := deliverer.DeliverBackgroundAdmission(sdk.BackgroundAdmissionDelivery{Admission: admission, InstallationID: installation}); err != nil {
			m.RevokeBackgroundForInstance(instanceID)
			return nil, err
		}
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	phase = "HTTP listener"
	if err != nil {
		return nil, fmt.Errorf("extension HTTP listener: %w", err)
	}
	_ = conn.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &running{descriptor: Descriptor{Manifest: manifest, Root: packageDir, InstanceID: instanceID}, client: client, transport: transport, runtimeDir: runtimeDir, callbackServer: callbackServer, installation: installation}, nil
}

func needsNativeCallback(capabilities []string) bool {
	return sdk.NeedsNativeCallback(capabilities)
}

func cloneDescriptor(d Descriptor) Descriptor {
	d.Manifest.Pages = append([]sdk.Page(nil), d.Manifest.Pages...)
	for i := range d.Manifest.Pages {
		d.Manifest.Pages[i].Subroutes = append([]sdk.Subroute(nil), d.Manifest.Pages[i].Subroutes...)
	}
	d.Manifest.Panels = append([]sdk.Panel(nil), d.Manifest.Panels...)
	d.Manifest.Capabilities = append([]string(nil), d.Manifest.Capabilities...)
	d.Manifest.Policies = append([]string(nil), d.Manifest.Policies...)
	return d
}

func (m *Manager) List() []Descriptor {
	m.mu.Lock()
	type candidate struct {
		id         string
		descriptor Descriptor
	}
	candidates := make([]candidate, 0, len(m.running))
	for id, item := range m.running {
		// Draining packages closed new admission for teardown; they stay
		// out of navigation and routing until teardown completes or fails.
		if m.draining[id] {
			continue
		}
		if item.client.Exited() {
			m.stopOne(id, item, true)
			continue
		}
		candidates = append(candidates, candidate{id: id, descriptor: cloneDescriptor(item.descriptor)})
	}
	m.mu.Unlock()
	list := make([]Descriptor, 0, len(candidates))
	for _, c := range candidates {
		// Ordinary admission opens only after durable activation:
		// a merely registered backend stays out of navigation.
		if !m.ordinaryAdmissionOpen(c.id) {
			continue
		}
		list = append(list, c.descriptor)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Manifest.ID < list[j].Manifest.ID })
	return list
}

func (m *Manager) Lookup(id string) (Descriptor, http.RoundTripper, bool) {
	m.mu.Lock()
	if m.draining[id] {
		m.mu.Unlock()
		return Descriptor{}, nil, false
	}
	item := m.running[id]
	if item == nil {
		m.mu.Unlock()
		return Descriptor{}, nil, false
	}
	if item.client.Exited() {
		m.stopOne(id, item, true)
		m.mu.Unlock()
		return Descriptor{}, nil, false
	}
	descriptor, transport := cloneDescriptor(item.descriptor), item.transport
	m.mu.Unlock()
	// Ordinary admission opens only after durable activation: a merely
	// registered backend is not routable even while its process runs.
	if !m.ordinaryAdmissionOpen(id) {
		return Descriptor{}, nil, false
	}
	return descriptor, transport, true
}

var ErrRequiredPolicyUnavailable = packages.ErrRequiredPolicyUnavailable

// EvaluateRequiredPolicy asks every required package that declares policyID.
// A missing package or handler, process exit, timeout, or malformed response
// fails closed. Required packages are evaluated in configured order.
func (m *Manager) EvaluateRequiredPolicy(ctx context.Context, policyID string, request sdk.PolicyRequest) (sdk.PolicyDecision, error) {
	m.mu.Lock()
	if !m.started || len(m.requiredIDs) == 0 {
		m.mu.Unlock()
		return sdk.PolicyDecision{}, ErrRequiredPolicyUnavailable
	}
	transports := make([]http.RoundTripper, 0, len(m.requiredIDs))
	for _, id := range m.requiredIDs {
		item := m.running[id]
		if item == nil {
			m.mu.Unlock()
			return sdk.PolicyDecision{}, ErrRequiredPolicyUnavailable
		}
		if item.client.Exited() {
			m.stopOne(id, item, true)
			m.mu.Unlock()
			return sdk.PolicyDecision{}, ErrRequiredPolicyUnavailable
		}
		if slices.Contains(item.descriptor.Manifest.Policies, policyID) {
			transports = append(transports, item.transport)
		}
	}
	m.mu.Unlock()
	if len(transports) == 0 {
		return sdk.PolicyDecision{}, ErrRequiredPolicyUnavailable
	}
	data, err := json.Marshal(request)
	if err != nil {
		return sdk.PolicyDecision{}, ErrRequiredPolicyUnavailable
	}
	for _, transport := range transports {
		callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		httpRequest, err := http.NewRequestWithContext(callCtx, http.MethodPost, "http://extension/v1/policies/"+url.PathEscape(policyID), bytes.NewReader(data))
		if err != nil {
			cancel()
			return sdk.PolicyDecision{}, ErrRequiredPolicyUnavailable
		}
		httpRequest.Header.Set("Content-Type", "application/json")
		response, err := transport.RoundTrip(httpRequest)
		if err != nil {
			cancel()
			return sdk.PolicyDecision{}, ErrRequiredPolicyUnavailable
		}
		var decision sdk.PolicyDecision
		if response.StatusCode != http.StatusOK {
			err = ErrRequiredPolicyUnavailable
		} else {
			var body []byte
			body, err = io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
			if err == nil && len(body) <= 64<<10 {
				decoder := json.NewDecoder(bytes.NewReader(body))
				decoder.DisallowUnknownFields()
				err = decoder.Decode(&decision)
				if err == nil && decoder.Decode(new(any)) != io.EOF {
					err = ErrRequiredPolicyUnavailable
				}
			} else {
				err = ErrRequiredPolicyUnavailable
			}
		}
		_ = response.Body.Close()
		cancel()
		if err != nil {
			return sdk.PolicyDecision{}, ErrRequiredPolicyUnavailable
		}
		if !decision.Allowed {
			return decision, nil
		}
	}
	return sdk.PolicyDecision{Allowed: true}, nil
}

// noteControllerStarted records required-controller readiness when a
// backend whose manifest declares the lifecycle capability starts. The
// handshake already matched the registration to the capability, so a
// started declaring backend is ready. Callers hold m.mu. Instance stop
// or replacement deletes the record; lifecycle admissions die with the
// instance through the shared background registry.
func (m *Manager) noteControllerStarted(descriptor Descriptor) {
	if slices.Contains(descriptor.Manifest.Capabilities, sdk.CapabilityExtensionLifecycle) {
		m.controllers[descriptor.Manifest.ID] = controllerState{instanceID: descriptor.InstanceID, ready: true}
		return
	}
}

func (m *Manager) stopOne(id string, item *running, crashed bool) {
	delete(m.running, id)
	delete(m.controllers, id)
	// Runtime stop revokes its runtime and service admissions; browser
	// admissions are revoked through the injected callback as before.
	m.RevokeBackgroundForInstance(item.descriptor.InstanceID)
	if m.instanceStopped != nil {
		m.instanceStopped(item.descriptor.InstanceID)
	}
	item.transport.CloseIdleConnections()
	if item.callbackServer != nil {
		_ = item.callbackServer.Close()
	}
	item.client.Kill()
	_ = os.RemoveAll(item.runtimeDir)
	if crashed {
		log.Warn("Extension %s exited unexpectedly; runtime unavailable", id)
	} else {
		log.Info("Extension %s stopped", id)
	}
}

func (m *Manager) observeExit(id string, item *running) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		if m.running[id] != item {
			m.mu.Unlock()
			return
		}
		if item.client.Exited() {
			m.stopOne(id, item, true)
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

func (m *Manager) stopAll() {
	for id, item := range m.running {
		m.stopOne(id, item, false)
	}
}

func (m *Manager) Close() error {
	// Cancel manager-owned background work first: the accepted admin
	// mutation task and the startup optional pass both observe lifeCtx.
	// Join the active mutation without holding m.mu — its settlement
	// takes brief snapshots — before stopping runtime and unlocking the
	// package store, so unfinished effects remain pending and an
	// offline CLI blocked on the lock proceeds only after the task
	// has fully ended.
	m.acceptMu.Lock()
	m.mu.Lock()
	m.started = false
	cancel := m.lifeCancel
	active := m.activeOp
	startupDone := m.startupDone
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.acceptMu.Unlock()
	if active != nil {
		<-active.done
	}
	if startupDone != nil {
		<-startupDone
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeOp = nil
	m.startupDone = nil
	m.stopAll()
	m.closeServiceCallback()
	m.started = false
	m.store = nil
	if m.lock != nil {
		err := m.lock.Unlock()
		m.lock = nil
		return err
	}
	return nil
}

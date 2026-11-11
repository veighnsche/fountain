// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	sdk "forgejo.org/extension-sdk"
)

// BackgroundAdmission binds one ephemeral admission to its exact
// package installation and the runtime instance that owns it. Caller
// payloads can never select another installation: the dispatcher
// derives identity from this record.
type BackgroundAdmission struct {
	// PackageID binds an ordinary admission to its exact issuing
	// package. Lifecycle admissions carry their target in the call
	// tuple instead and leave this empty.
	PackageID      string
	InstallationID string
	InstanceID     string
	Service        bool
	// ServicePeerUID records the bootstrapped service peer; service
	// operations must arrive from the same UID.
	ServicePeerUID uint32
	Capabilities   map[string]struct{}
	// Lifecycle marks the distinct lifecycle admission variant. It
	// carries one immutable call tuple with its deadline and the
	// desired flag it was issued under; CleanupException is stored in
	// this record only, never supplied by a caller, and marks the
	// single bounded deactivate a still-active failed-enable command
	// may issue. Ordinary background operations reject this variant;
	// browser admissions and lifecycle admissions are never
	// interchangeable.
	Lifecycle                 bool
	Call                      sdk.LifecycleCall
	DesiredEnabled            bool
	CleanupException          bool
	lifecyclePurpose          lifecycleAdmissionPurpose
	cleanupControllerVerified bool
}

type backgroundRegistry struct {
	mu             sync.Mutex
	entries        map[string]*BackgroundAdmission
	byInstance     map[string]map[string]struct{}
	byInstallation map[string]map[string]struct{}
}

func newBackgroundRegistry() *backgroundRegistry {
	return &backgroundRegistry{
		entries:        make(map[string]*BackgroundAdmission),
		byInstance:     make(map[string]map[string]struct{}),
		byInstallation: make(map[string]map[string]struct{}),
	}
}

func (registry *backgroundRegistry) issue(admission *BackgroundAdmission) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.entries[token] = admission
	if registry.byInstance[admission.InstanceID] == nil {
		registry.byInstance[admission.InstanceID] = make(map[string]struct{})
	}
	registry.byInstance[admission.InstanceID][token] = struct{}{}
	if registry.byInstallation[admission.InstallationID] == nil {
		registry.byInstallation[admission.InstallationID] = make(map[string]struct{})
	}
	registry.byInstallation[admission.InstallationID][token] = struct{}{}
	return token, nil
}

func (registry *backgroundRegistry) get(token string) (BackgroundAdmission, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	entry := registry.entries[token]
	if entry == nil {
		return BackgroundAdmission{}, false
	}
	admission := *entry
	admission.Capabilities = make(map[string]struct{}, len(entry.Capabilities))
	for capability := range entry.Capabilities {
		admission.Capabilities[capability] = struct{}{}
	}
	return admission, true
}

func (registry *backgroundRegistry) revokeLocked(token string) {
	entry := registry.entries[token]
	delete(registry.entries, token)
	if entry == nil {
		return
	}
	delete(registry.byInstance[entry.InstanceID], token)
	if len(registry.byInstance[entry.InstanceID]) == 0 {
		delete(registry.byInstance, entry.InstanceID)
	}
	delete(registry.byInstallation[entry.InstallationID], token)
	if len(registry.byInstallation[entry.InstallationID]) == 0 {
		delete(registry.byInstallation, entry.InstallationID)
	}
}

// revokeInstance invalidates every runtime and service admission owned by a
// stopped or replaced runtime instance.
func (registry *backgroundRegistry) revokeInstance(instanceID string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for token := range registry.byInstance[instanceID] {
		registry.revokeLocked(token)
	}
}

// replaceService atomically revokes an installation's previous service
// admissions and issues one for the current runtime instance.
func (registry *backgroundRegistry) replaceService(admission *BackgroundAdmission) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for previous := range registry.byInstallation[admission.InstallationID] {
		if entry := registry.entries[previous]; entry != nil && entry.Service {
			registry.revokeLocked(previous)
		}
	}
	registry.entries[token] = admission
	if registry.byInstance[admission.InstanceID] == nil {
		registry.byInstance[admission.InstanceID] = make(map[string]struct{})
	}
	registry.byInstance[admission.InstanceID][token] = struct{}{}
	if registry.byInstallation[admission.InstallationID] == nil {
		registry.byInstallation[admission.InstallationID] = make(map[string]struct{})
	}
	registry.byInstallation[admission.InstallationID][token] = struct{}{}
	return token, nil
}

// IssueRuntimeAdmission binds a fresh runtime admission to its exact
// package installation and current instance. Runtime stop revokes
// it; a lifecycle fence dynamically rejects it without revocation,
// so a retained instance keeps its usable token across retry.
func (m *Manager) IssueRuntimeAdmission(packageID, installationID, instanceID string, capabilities []string) (string, error) {
	if packageID == "" || installationID == "" || instanceID == "" {
		return "", errors.New("background admission requires package, installation and instance")
	}
	entry := &BackgroundAdmission{PackageID: packageID, InstallationID: installationID, InstanceID: instanceID, Capabilities: make(map[string]struct{}, len(capabilities))}
	for _, capability := range capabilities {
		entry.Capabilities[capability] = struct{}{}
	}
	return m.background.issue(entry)
}

// VerifyBackgroundAdmission resolves an admission to its host-derived claims.
// The lifecycle variant is rejected here: ordinary background operations
// never accept it. An ordinary admission bound to a fenced package or a
// replaced instance never verifies, even while its token is retained.
func (m *Manager) VerifyBackgroundAdmission(token string) (BackgroundAdmission, bool) {
	admission, ok := m.background.get(token)
	if !ok || admission.Lifecycle {
		return BackgroundAdmission{}, false
	}
	if admission.PackageID == "" || !m.VerifyOrdinaryToken(admission.PackageID, admission.InstanceID) {
		return BackgroundAdmission{}, false
	}
	return admission, true
}

// IssueLifecycleAdmission mints one opaque lifecycle token for a full call
// tuple. Cleanup is limited to one check at each authenticated hop; its purpose
// is chosen by the issuer from internal command state, never a request flag.
func (m *Manager) IssueLifecycleAdmission(admission *BackgroundAdmission) (string, error) {
	if admission == nil || !admission.Lifecycle || admission.InstallationID == "" || admission.InstanceID == "" {
		return "", errors.New("lifecycle admission requires installation, instance and tuple")
	}
	issued := *admission
	issued.Capabilities = map[string]struct{}{}
	issued.cleanupControllerVerified = false
	// Compatibility callers retain the original exported flag; production
	// issuance selects a named purpose and never carries a cleanup boolean.
	if issued.lifecyclePurpose == "" {
		issued.lifecyclePurpose = purposeLifecycle
		if issued.CleanupException {
			issued.lifecyclePurpose = purposeFailedEnableCleanup
		}
	}
	return m.background.issue(&issued)
}

// VerifyLifecycleAdmission resolves one lifecycle admission. Ordinary
// admissions never verify here.
func (m *Manager) VerifyLifecycleAdmission(token string) (BackgroundAdmission, bool) {
	admission, ok := m.background.get(token)
	if !ok || !admission.Lifecycle {
		return BackgroundAdmission{}, false
	}
	return admission, true
}

// RevokeLifecycleAdmission drops one token after its call, deadline,
// controller instance stop, or superseding intent.
func (m *Manager) RevokeLifecycleAdmission(token string) {
	m.background.mu.Lock()
	defer m.background.mu.Unlock()
	m.background.revokeLocked(token)
}

// RevokeLifecycleForInstallation drops every lifecycle admission for one
// target installation when its intent is superseded.
func (m *Manager) RevokeLifecycleForInstallation(installationID string) {
	m.background.mu.Lock()
	defer m.background.mu.Unlock()
	for token := range m.background.byInstallation[installationID] {
		if entry := m.background.entries[token]; entry != nil && entry.Lifecycle {
			m.background.revokeLocked(token)
		}
	}
}

// RevokeBackgroundForInstance invalidates a stopped instance's admissions.
func (m *Manager) RevokeBackgroundForInstance(instanceID string) {
	m.background.revokeInstance(instanceID)
}

// ParseServiceBridgePeers parses explicit operator configuration mapping a
// service Unix peer UID to one permitted package ID: "uid:package,...".
// Socket group membership or manifest declaration alone never authorizes a
// peer, and one peer mapping to several packages is ambiguous and rejected.
func ParseServiceBridgePeers(raw string) (map[uint32]string, error) {
	mapping := make(map[uint32]string)
	if strings.TrimSpace(raw) == "" {
		return mapping, nil
	}
	for entry := range strings.SplitSeq(raw, ",") {
		uidText, packageID, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok || !sdk.ValidID(packageID) {
			return nil, fmt.Errorf("invalid service bridge peer %q", entry)
		}
		uid64, err := strconv.ParseUint(uidText, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid service bridge peer %q", entry)
		}
		uid := uint32(uid64)
		if _, duplicated := mapping[uid]; duplicated {
			return nil, fmt.Errorf("ambiguous service bridge peer %q", uidText)
		}
		mapping[uid] = packageID
	}
	return mapping, nil
}

// SetServiceBridgePeers installs the operator peer mapping before Start.
func (m *Manager) SetServiceBridgePeers(mapping map[uint32]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("extension manager already started")
	}
	m.serviceBridgePeers = mapping
	return nil
}

// VerifyServiceLifecyclePeer admits only the service peer UID explicitly
// mapped to the calling controller package. Socket group membership or
// manifest declaration alone never authorizes a peer.
func (m *Manager) VerifyServiceLifecyclePeer(peerUID uint32, controllerID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return controllerID != "" && m.serviceBridgePeers[peerUID] == controllerID
}

// BootstrapServiceAdmission maps an observed service peer to exactly one
// permitted installation and atomically replaces its service admission for
// the current runtime instance. requestedInstallation optionally pins the
// expected installation and must equal the mapped one when set.
func (m *Manager) BootstrapServiceAdmission(peerUID uint32, requestedInstallation string) (token, installation string, err error) {
	m.mu.Lock()
	packageID, mapped := m.serviceBridgePeers[peerUID]
	if !mapped {
		m.mu.Unlock()
		return "", "", errors.New("service bridge peer is not permitted")
	}
	item := m.running[packageID]
	if item == nil {
		m.mu.Unlock()
		return "", "", errors.New("service bridge package is not running")
	}
	if item.client.Exited() {
		m.stopOne(packageID, item, true)
		m.mu.Unlock()
		return "", "", errors.New("service bridge package is not running")
	}
	if !slices.Contains(item.descriptor.Manifest.Capabilities, sdk.CapabilityServiceBridge) {
		m.mu.Unlock()
		return "", "", errors.New("native service bridge not declared")
	}
	installation, instanceID := item.installation, item.descriptor.InstanceID
	capabilities := append([]string(nil), item.descriptor.Manifest.Capabilities...)
	m.mu.Unlock()
	if installation == "" || instanceID == "" {
		return "", "", errors.New("service bridge installation is unavailable")
	}
	if requestedInstallation != "" && requestedInstallation != installation {
		return "", "", errors.New("service bridge installation mismatch")
	}
	// A fenced package issues no service admission: ordinary
	// activation must complete durably first.
	if !m.VerifyOrdinaryToken(packageID, instanceID) {
		return "", "", errors.New("service bridge package is not admitted")
	}
	entry := &BackgroundAdmission{PackageID: packageID, InstallationID: installation, InstanceID: instanceID, Service: true, ServicePeerUID: peerUID, Capabilities: make(map[string]struct{}, len(capabilities))}
	for _, capability := range capabilities {
		entry.Capabilities[capability] = struct{}{}
	}
	token, err = m.background.replaceService(entry)
	if err != nil {
		return "", "", errors.New("service bridge admission unavailable")
	}
	return token, installation, nil
}

// AdmittedInstallation resolves a live native callback's package instance.
// Callers cannot select an installation UUID through a callback body.
func (m *Manager) AdmittedInstallation(packageID, instanceID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item := m.running[packageID]
	if item == nil || item.descriptor.InstanceID != instanceID || item.client.Exited() {
		return "", false
	}
	return item.installation, item.installation != ""
}

// peerCredentialAddr carries kernel Unix peer credentials in RemoteAddr form
// so private-socket handlers can enforce peer policy. An empty value marks a
// connection whose credentials could not be read; handlers must reject it.
type peerCredentialAddr struct{ value string }

func (addr peerCredentialAddr) Network() string { return "unix" }
func (addr peerCredentialAddr) String() string  { return addr.value }

// peerCredentialConn annotates an accepted Unix connection with its kernel
// peer credentials. Credential failures never fail Accept itself: the
// connection is served with an unusable address and handlers reject it,
// keeping one broken peer from stopping the listener.
type peerCredentialConn struct {
	net.Conn
	addr net.Addr
}

func (conn *peerCredentialConn) RemoteAddr() net.Addr { return conn.addr }

type peerCredentialListener struct{ net.Listener }

func wrapPeerCredentialListener(listener net.Listener) net.Listener {
	return peerCredentialListener{Listener: listener}
}

func (listener peerCredentialListener) Accept() (net.Conn, error) {
	conn, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	peer, err := sdk.PeerCredential(conn)
	if err != nil {
		return &peerCredentialConn{Conn: conn, addr: peerCredentialAddr{}}, nil
	}
	return &peerCredentialConn{Conn: conn, addr: peerCredentialAddr{value: sdk.FormatUnixPeer(sdk.UnixPeer{UID: peer.UID, GID: peer.GID, PID: peer.PID})}}, nil
}

// CurrentProcessUID reports the host process UID for runtime-channel peer
// checks. The per-instance socket is already mode 0600 in a 0700 runtime
// directory; the explicit check additionally verifies the deployed user
// mapping and rejects any other local peer.
func CurrentProcessUID() uint32 {
	return uint32(os.Geteuid())
}

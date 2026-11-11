// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	sdk "forgejo.org/extension-sdk"
	packages "forgejo.org/modules/extensions"

	"github.com/stretchr/testify/require"
)

const (
	controlSodaUUID   = "550e8400-e29b-41d4-a716-446655440000"
	controlLiveSHA    = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	controlReceiptSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

func controlCatalogEntries() []sdk.CatalogPackage {
	return []sdk.CatalogPackage{
		{ID: "soda", Version: "1.0", URL: "https://publisher.example/releases/soda-1.0.tar.gz", Size: 100, SHA256: controlReceiptSHA, Required: true, FountainProto: 1, BrowserContract: 1, Capabilities: []string{}},
		{ID: "sample", Version: "1.0", URL: "https://publisher.example/releases/sample-1.0.tar.gz", Size: 100, SHA256: controlReceiptSHA, Required: false, FountainProto: 1, BrowserContract: 1, Capabilities: []string{}, LifecycleController: "soda"},
	}
}

// controlManager builds one manager with configured catalog, peer
// mapping and required installation without starting backends. Live
// backend processes are never needed for snapshot, state, reconcile
// selection or cache-hit paths.
func controlManager(t *testing.T, root string) *Manager {
	t.Helper()
	sodaDir := filepath.Join(root, "soda")
	require.NoError(t, os.MkdirAll(sodaDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sodaDir, "extension.json"), []byte(`{"protocol":1,"id":"soda","name":"Soda","version":"1.0","executable":"backend"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sodaDir, "backend"), []byte("controller fixture"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sodaDir, ".fountain-package.json"), []byte(`{"schema":1,"package_id":"soda","package_version":"1.0","archive_sha256":"`+controlReceiptSHA+`"}`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".installations"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".installations", "soda.json"), []byte(`{"protocol":1,"installation_id":"`+controlSodaUUID+`"}`), 0o600))
	m := NewManager(root, "soda")
	m.catalog = controlCatalogEntries()
	m.catalogSHA = controlLiveSHA
	m.serviceBridgePeers = map[uint32]string{2000: "soda", 2001: "plain"}
	m.started = true
	return m
}

func TestControlPeerMapping(t *testing.T) {
	m := NewManager(t.TempDir(), "soda")
	m.serviceBridgePeers = map[uint32]string{2000: "soda", 2001: "plain"}

	controller, ok := m.ControlPeer(sdk.FormatUnixPeer(sdk.UnixPeer{UID: 2000, GID: 2000}))
	require.True(t, ok)
	require.Equal(t, "soda", controller)

	_, ok = m.ControlPeer(sdk.FormatUnixPeer(sdk.UnixPeer{UID: 2001, GID: 2001}))
	require.False(t, ok, "a peer mapped to a non-required package is not admitted")

	_, ok = m.ControlPeer(sdk.FormatUnixPeer(sdk.UnixPeer{UID: 9999, GID: 9999}))
	require.False(t, ok)

	_, ok = m.ControlPeer("bogus-remote-addr")
	require.False(t, ok)
}

func TestControlSnapshotEntries(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)

	rows, err := m.controlSnapshotEntries("soda")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "sample", rows[0].PackageID)
	require.Equal(t, "soda", rows[1].PackageID)

	required := rows[1]
	require.True(t, required.Required)
	require.True(t, required.DesiredEnabled)
	require.Equal(t, "0", required.Generation)
	require.NotNil(t, required.InstallationID)
	require.Equal(t, controlSodaUUID, *required.InstallationID)
	require.NotNil(t, required.InstalledVersion)
	require.Equal(t, "1.0", *required.InstalledVersion)
	require.NotNil(t, required.PackageSHA256)
	require.Equal(t, controlReceiptSHA, *required.PackageSHA256)

	optional := rows[0]
	require.False(t, optional.Required)
	require.Equal(t, "0", optional.Generation)
	require.False(t, optional.DesiredEnabled)
	require.Nil(t, optional.InstallationID)

	// A malformed optional intent is preserved and rejects the snapshot.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".intents"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".intents", "sample.json"), []byte(`{"schema":`), 0o600))
	rows, err = m.controlSnapshotEntries("soda")
	require.Error(t, err)
	require.Nil(t, rows)
	require.NoError(t, os.Remove(filepath.Join(root, ".intents", "sample.json")))

	// A missing required installation is invalid storage, not absence.
	require.NoError(t, os.Remove(filepath.Join(root, ".installations", "soda.json")))
	_, err = m.controlSnapshotEntries("soda")
	require.Error(t, err)
}

func TestControlSnapshotGuards(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)

	response, reject := m.ControlSnapshot(context.Background(), "soda")
	require.Nil(t, reject)
	require.Equal(t, controlLiveSHA, response.CatalogSHA256)
	require.Len(t, response.Entries, 2)

	require.NoError(t, m.acquireMutation("admin-op"))
	_, reject = m.ControlSnapshot(context.Background(), "soda")
	require.NotNil(t, reject)
	require.Equal(t, http.StatusConflict, reject.HTTPStatus)
	m.releaseMutation()

	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	pending := packages.IntentActivate
	_, err = packages.StoreIntent(root, "sample", installation, true, &pending, "0")
	require.NoError(t, err)
	_, reject = m.ControlSnapshot(context.Background(), "soda")
	require.NotNil(t, reject)
	require.Equal(t, http.StatusConflict, reject.HTTPStatus)
}

func TestControlStateConstrain(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)

	response, reject := m.ControlState(context.Background(), "soda", "sample")
	require.Nil(t, reject)
	require.Equal(t, controlLiveSHA, response.CatalogSHA256)
	require.Equal(t, "soda", response.ControllerID)
	require.Nil(t, response.ControllerInstance)
	require.False(t, response.ControllerReady)
	require.False(t, response.AdmissionOpen)
	require.Equal(t, "sample", response.Entry.PackageID)

	_, reject = m.ControlState(context.Background(), "soda", "soda")
	require.NotNil(t, reject)
	require.Equal(t, http.StatusForbidden, reject.HTTPStatus)

	_, reject = m.ControlState(context.Background(), "soda", "ghost")
	require.NotNil(t, reject)
	require.Equal(t, http.StatusForbidden, reject.HTTPStatus)
}

func TestControlReconcileRows(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)
	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "sample", installation, true, nil, "0")
	require.NoError(t, err)

	response, reject := m.ControlReconcile(context.Background(), "soda")
	require.Nil(t, reject)
	require.Equal(t, controlLiveSHA, response.CatalogSHA256)
	require.Len(t, response.Entries, 2)

	// Absent never-activated rows stay available without host calls.
	// Here sample is desired, so both rows carry truthful failures:
	// the required backend is stopped and the optional backend with a
	// failed observation is never displayed as enabled.
	byID := make(map[string]ControlReconcileEntry)
	for _, entry := range response.Entries {
		byID[entry.PackageID] = entry
	}
	sample := byID["sample"]
	require.Equal(t, packages.LifecycleFailed, sample.State)
	require.Equal(t, "stopped", sample.Backend)
	require.NotNil(t, sample.Error)
	require.False(t, sample.AdmissionOpen)

	required := byID["soda"]
	require.Equal(t, "stopped", required.Backend)
	require.False(t, required.AdmissionOpen)
	require.NotNil(t, required.Error)
}

func TestControlReconcileAbsentRow(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)

	response, reject := m.ControlReconcile(context.Background(), "soda")
	require.Nil(t, reject)
	require.Len(t, response.Entries, 2)
	require.Equal(t, "sample", response.Entries[0].PackageID)
	absent := response.Entries[0]
	require.Equal(t, "available", absent.State)
	require.Equal(t, "stopped", absent.Backend)
	require.Equal(t, "stopped", absent.Effects)
	require.False(t, absent.AdmissionOpen)
	require.Nil(t, absent.Error)
}

// A stable disabled row with no current controller fact is unknown,
// never confirmed disabled: without a live observation the reconcile
// row carries a bounded unconfirmed error with admission closed.
func TestControlReconcileDisabledRowNeedsObservation(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)
	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "sample", installation, false, nil, "0")
	require.NoError(t, err)
	sampleDir := filepath.Join(root, "sample")
	require.NoError(t, os.MkdirAll(sampleDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sampleDir, "extension.json"), []byte(`{"protocol":1,"id":"sample","name":"Sample","version":"1.0","executable":"backend"}`), 0o600))

	response, reject := m.ControlReconcile(context.Background(), "soda")
	require.Nil(t, reject)
	byID := make(map[string]ControlReconcileEntry)
	for _, entry := range response.Entries {
		byID[entry.PackageID] = entry
	}
	sample := byID["sample"]
	require.Equal(t, packages.LifecycleFailed, sample.State)
	require.Equal(t, "stopped", sample.Backend)
	require.Equal(t, "unknown", sample.Effects)
	require.False(t, sample.AdmissionOpen)
	require.NotNil(t, sample.Error)
	require.Equal(t, packages.LifecycleErrUnconfirmed, sample.Error.Code)
}

func TestControlReconcileGuards(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)
	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	pending := packages.IntentActivate
	_, err = packages.StoreIntent(root, "sample", installation, true, &pending, "0")
	require.NoError(t, err)
	_, reject := m.ControlReconcile(context.Background(), "soda")
	require.NotNil(t, reject)
	require.Equal(t, http.StatusConflict, reject.HTTPStatus)

	plain := t.TempDir()
	require.NoError(t, os.Chmod(plain, 0o755))
	holder, err := os.OpenFile(plain, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer func() { _ = holder.Close() }()
	require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	defer func() { _ = syscall.Flock(int(holder.Fd()), syscall.LOCK_UN) }()
	guarded := controlManager(t, t.TempDir())
	guarded.maintenanceDir = plain
	_, reject = guarded.ControlReconcile(context.Background(), "soda")
	require.NotNil(t, reject)
	require.Equal(t, http.StatusLocked, reject.HTTPStatus)

	marked := t.TempDir()
	require.NoError(t, os.Chmod(marked, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(marked, "operation.json"), []byte(`{}`), 0o600))
	markedManager := controlManager(t, t.TempDir())
	markedManager.maintenanceDir = marked
	_, reject = markedManager.ControlReconcile(context.Background(), "soda")
	require.NotNil(t, reject)
	require.Equal(t, http.StatusLocked, reject.HTTPStatus)
}

// controlTarGzip builds one gzip member holding USTAR regular-file
// entries for cache-hit fixtures.
func controlTarGzip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	unzip, err := gzip.NewWriterLevel(&archive, gzip.BestSpeed)
	require.NoError(t, err)
	writer := tar.NewWriter(unzip)
	for name, body := range files {
		mode := int64(0o644)
		if name == "backend" {
			mode = 0o755
		}
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Format: tar.FormatUSTAR}))
		_, err = writer.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.NoError(t, unzip.Close())
	return archive.Bytes()
}

func controlManifest(id, controller string) []byte {
	manifest := map[string]any{
		"protocol": 1, "id": id, "name": id, "version": "1.0", "executable": "backend",
	}
	if controller != "" {
		manifest["lifecycle_controller"] = controller
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		panic(err)
	}
	return encoded
}

func TestControlCacheValidation(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)
	source := packages.Snapshot{CatalogSHA256: controlLiveSHA}

	target := sdk.Catalog{Version: 1, Packages: []sdk.CatalogPackage{
		{ID: "soda", Version: "1.0", URL: "https://publisher.example/releases/soda-1.0.tar.gz", Size: 10, SHA256: controlReceiptSHA, Required: true, FountainProto: 1, BrowserContract: 1, Capabilities: []string{}},
	}}

	_, reject := m.ControlCache(context.Background(), "soda", target, []string{"bogus"}, source)
	require.NotNil(t, reject)
	require.Equal(t, http.StatusBadRequest, reject.HTTPStatus)

	_, reject = m.ControlCache(context.Background(), "soda", target, []string{}, source)
	require.NotNil(t, reject)
	require.Equal(t, http.StatusBadRequest, reject.HTTPStatus)

	stale := packages.Snapshot{CatalogSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	_, reject = m.ControlCache(context.Background(), "soda", target, []string{"soda"}, stale)
	require.NotNil(t, reject)
	require.Equal(t, http.StatusConflict, reject.HTTPStatus)
}

func TestControlCacheHit(t *testing.T) {
	root := t.TempDir()
	m := controlManager(t, root)

	sodaArchive := controlTarGzip(t, map[string][]byte{
		"extension.json": controlManifest("soda", ""),
		"backend":        []byte("soda controller fixture"),
	})
	sampleArchive := controlTarGzip(t, map[string][]byte{
		"extension.json": controlManifest("sample", "soda"),
		"backend":        []byte("sample backend fixture"),
	})
	sodaSum := sha256.Sum256(sodaArchive)
	sampleSum := sha256.Sum256(sampleArchive)
	sodaSHA := hex.EncodeToString(sodaSum[:])
	sampleSHA := hex.EncodeToString(sampleSum[:])
	cacheDir := filepath.Join(root, ".cache", "packages")
	require.NoError(t, os.MkdirAll(cacheDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, sodaSHA+".tar.gz"), sodaArchive, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, sampleSHA+".tar.gz"), sampleArchive, 0o600))

	installation, err := packages.EnsureInstallation(root, "sample")
	require.NoError(t, err)
	_, err = packages.StoreIntent(root, "sample", installation, true, nil, "0")
	require.NoError(t, err)

	target := sdk.Catalog{Version: 1, Packages: []sdk.CatalogPackage{
		{ID: "soda", Version: "1.0", URL: "https://publisher.example/releases/soda-1.0.tar.gz", Size: int64(len(sodaArchive)), SHA256: sodaSHA, Required: true, FountainProto: 1, BrowserContract: 1, Capabilities: []string{}},
		{ID: "sample", Version: "1.0", URL: "https://publisher.example/releases/sample-1.0.tar.gz", Size: int64(len(sampleArchive)), SHA256: sampleSHA, Required: false, FountainProto: 1, BrowserContract: 1, Capabilities: []string{}, LifecycleController: "soda"},
	}}
	source := packages.Snapshot{CatalogSHA256: controlLiveSHA, Entries: []packages.SnapshotEntry{
		{PackageID: "soda", InstallationID: &[]string{controlSodaUUID}[0], Generation: "0", DesiredEnabled: true, Required: true, InstalledVersion: &[]string{"1.0"}[0], PackageSHA256: &[]string{controlReceiptSHA}[0]},
		{PackageID: "sample", InstallationID: &[]string{installation}[0], Generation: "1", DesiredEnabled: true, Required: false},
	}}

	response, reject := m.ControlCache(context.Background(), "soda", target, []string{"soda", "sample"}, source)
	require.Nil(t, reject)
	require.Len(t, response.Cached, 2)
	require.Equal(t, "sample", response.Cached[0].ID)
	require.Equal(t, "soda", response.Cached[1].ID)
	require.Equal(t, sampleSHA, response.Cached[0].SHA256)
	require.Equal(t, int64(len(sampleArchive)), response.Cached[0].Size)

	// Cache-only means no install, no intent writes and no admission.
	require.NoDirExists(t, filepath.Join(root, "sample"))
	intent, err := packages.LoadIntent(root, "sample")
	require.NoError(t, err)
	require.Equal(t, "1", intent.Generation)
	require.Nil(t, intent.PendingOperation)
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"io"
	"net/http"
	"os"
	"sort"

	sdk "forgejo.org/extension-sdk"
)

// maximumSnapshotBytes bounds one source snapshot document: 1 MiB.
const maximumSnapshotBytes = 1 << 20

// SnapshotEntry is one frozen requested-state row: the exact catalog
// SHA, installation UUID, generation and desired values the updater
// froze before changing anything. Missing optional UUID, version and
// digest are null; absent intent reads generation "0".
type SnapshotEntry struct {
	PackageID        string  `json:"package_id"`
	InstallationID   *string `json:"installation_id"`
	Generation       string  `json:"generation"`
	DesiredEnabled   bool    `json:"desired_enabled"`
	PendingOperation *string `json:"pending_operation"`
	InstalledVersion *string `json:"installed_version"`
	PackageSHA256    *string `json:"package_sha256"`
	Required         bool    `json:"required"`
}

// Snapshot is the updater's frozen source intent set.
type Snapshot struct {
	CatalogSHA256 string          `json:"catalog_sha256"`
	Entries       []SnapshotEntry `json:"entries"`
}

// DecodeSnapshot strictly decodes one snapshot document: unknown fields
// and trailing data are rejected and every row is semantically checked.
// Semantic mismatches against live state are checked by PrepareRelease,
// not here.
func DecodeSnapshot(data []byte) (Snapshot, error) {
	if len(data) == 0 || len(data) > maximumSnapshotBytes {
		return Snapshot{}, errors.New("extension snapshot exceeds size limit")
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, errors.New("extension snapshot is invalid")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Snapshot{}, errors.New("extension snapshot is invalid")
	}
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.CatalogSHA256 == "" {
		return errors.New("extension snapshot is invalid")
	}
	seen := make(map[string]bool, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if !sdk.ValidID(entry.PackageID) || seen[entry.PackageID] {
			return errors.New("extension snapshot is invalid")
		}
		seen[entry.PackageID] = true
		if _, err := ParseGeneration(entry.Generation); err != nil {
			return err
		}
		if err := validatePending(entry.DesiredEnabled, entry.PendingOperation); err != nil {
			return err
		}
	}
	return nil
}

// BootstrapRequired acquires and installs every required catalog package
// in configuration order through the package store. Exact installed
// receipts with validated packages are reused first, verified archive
// cache second, and bounded HTTPS acquisition only when neither is
// available, so exact cached packages reboot offline. It installs through
// the same PackageStore as live and offline flows, preserving existing
// installation UUIDs and requested-state intents; it writes no intents
// itself. There is no separate bootstrap image or bundled core package.
func BootstrapRequired(ctx context.Context, store *PackageStore, catalog sdk.Catalog, requiredIDs []string, client *http.Client) error {
	if err := sdk.ValidateCatalog(catalog); err != nil {
		return err
	}
	if err := ValidateCatalogSelection(catalog, requiredIDs); err != nil {
		return err
	}
	if client == nil {
		client = newAcquireClient()
	}
	// Repair interrupted swaps before validating or acquiring: a
	// crash between the old-bytes rename and staged publication
	// leaves an absent target whose retained backup restores here,
	// so exact packages reboot offline with zero downloads. A
	// required repair failure blocks required readiness.
	if failures := store.RecoverInterrupted(requiredIDs); len(failures) != 0 {
		for _, id := range requiredIDs {
			if err, ok := failures[id]; ok {
				return err
			}
		}
	}
	byID := make(map[string]sdk.CatalogPackage, len(catalog.Packages))
	for _, entry := range catalog.Packages {
		byID[entry.ID] = entry
	}
	for _, id := range requiredIDs {
		entry := byID[id]
		if err := ValidateInstalled(store.root, entry); err == nil {
			continue
		}
		cached := cachePath(store.root, entry.SHA256)
		if info, statErr := os.Stat(cached); statErr != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
			if _, err := downloadToCache(ctx, store.root, entry, client); err != nil {
				return err
			}
		} else if ok, _ := verifyFileDigest(cached, entry.SHA256); !ok {
			if _, err := downloadToCache(ctx, store.root, entry, client); err != nil {
				return err
			}
		}
		stage, err := stageFromCache(store.root, entry, cached)
		if err != nil {
			return err
		}
		_, installErr := store.InstallStaged(stage, InstallOptions{Replace: true, Entry: &entry})
		_ = os.RemoveAll(stage)
		if installErr != nil {
			return installErr
		}
	}
	return nil
}

// PreparedPackage reports one cache-prepared package pin.
type PreparedPackage struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// PrepareRelease retains the local-state-only preparation adapter. It cannot
// authenticate source catalog identity; production callers use
// PrepareReleaseFromSource with independently loaded source trust. It validates the target catalog and selection, rejects a frozen source
// snapshot whose catalog SHA, installation UUIDs or desired generations
// differ from current source state, then verifies and stages every
// selected cached package before the first installed-directory swap and
// installs exact receipts while preserving UUIDs and intents. Selected
// entries are every target required ID plus source-snapshot
// desired-enabled optional IDs; disabled optionals are neither
// downloaded nor upgraded. A missing or corrupt cached target is refused
// without networking and without intent writes. Backends are never
// activated here.
func (s *PackageStore) PrepareRelease(target sdk.Catalog, source Snapshot, requiredIDs []string) ([]PreparedPackage, error) {
	if err := sdk.ValidateCatalog(target); err != nil {
		return nil, err
	}
	if err := ValidateCatalogSelection(target, requiredIDs); err != nil {
		return nil, err
	}
	if err := checkSnapshotCurrent(s.root, source, requiredIDs); err != nil {
		return nil, err
	}
	return s.prepareRelease(target, source, requiredIDs)
}

// PrepareReleaseFromSource verifies the source release independently of the
// target, then stages all selected target archives before the first replacement.
// Existing desired intents are never rewritten by release preparation.
func (s *PackageStore) PrepareReleaseFromSource(target sdk.Catalog, snapshot Snapshot, requiredIDs []string, trust SourceTrust) ([]PreparedPackage, error) {
	if err := sdk.ValidateCatalog(target); err != nil {
		return nil, err
	}
	if err := ValidateCatalogSelection(target, requiredIDs); err != nil {
		return nil, err
	}
	if err := validateSourceTrust(snapshot, trust); err != nil {
		return nil, err
	}
	inScope := sourceSnapshotScope(s.root, trust)
	var recoverIDs []string
	for _, id := range RecoveryCandidates(s.root, trust.Catalog.Packages, trust.RequiredIDs) {
		included, err := inScope(id)
		if err != nil {
			return nil, err
		}
		if included {
			recoverIDs = append(recoverIDs, id)
		}
	}
	if failures := s.RecoverInterrupted(recoverIDs); len(failures) != 0 {
		return nil, errRecoveryUnavailable
	}
	for _, entry := range trust.Catalog.Packages {
		if entry.Required || entry.LifecycleController != trust.ControllerID {
			continue
		}
		intent, err := LoadIntent(s.root, entry.ID)
		if err != nil {
			return nil, err
		}
		if intent.Generation != "0" {
			if err := BindIntentController(s.root, entry.ID, intent.InstallationID, trust.ControllerID); err != nil {
				return nil, err
			}
		}
	}
	if err := VerifySourceSnapshot(s.root, snapshot, trust); err != nil {
		return nil, err
	}
	for _, row := range snapshot.Entries {
		if row.Required || !row.DesiredEnabled {
			continue
		}
		for _, entry := range target.Packages {
			if entry.ID == row.PackageID && entry.LifecycleController != trust.ControllerID {
				return nil, errSourceSnapshot
			}
		}
	}
	return s.prepareRelease(target, snapshot, requiredIDs)
}

func (s *PackageStore) prepareRelease(target sdk.Catalog, source Snapshot, requiredIDs []string) ([]PreparedPackage, error) {
	enabled := make(map[string]bool)
	for _, entry := range source.Entries {
		if !entry.Required && entry.DesiredEnabled {
			enabled[entry.PackageID] = true
		}
	}
	byID := make(map[string]sdk.CatalogPackage, len(target.Packages))
	for _, entry := range target.Packages {
		byID[entry.ID] = entry
	}
	var selected []sdk.CatalogPackage
	for _, id := range requiredIDs {
		selected = append(selected, byID[id])
	}
	optional := make([]string, 0, len(enabled))
	for id := range enabled {
		optional = append(optional, id)
	}
	sort.Strings(optional)
	for _, id := range optional {
		entry, ok := byID[id]
		if !ok {
			return nil, errors.New("enabled package is absent from the target catalog")
		}
		if entry.Required {
			continue
		}
		selected = append(selected, entry)
	}
	// Repair interrupted swaps for the selected packages before
	// staging or swapping: retained backups restore absent targets
	// from validated bytes, so an earlier retained-source
	// interruption is recovered without network or fabricated
	// intent. A repair failure refuses the whole preparation.
	selectedIDs := make([]string, 0, len(selected))
	for _, entry := range selected {
		selectedIDs = append(selectedIDs, entry.ID)
	}
	if failures := s.RecoverInterrupted(selectedIDs); len(failures) != 0 {
		for _, id := range selectedIDs {
			if err, ok := failures[id]; ok {
				return nil, err
			}
		}
	}
	stages := make([]string, 0, len(selected))
	defer func() {
		for _, stage := range stages {
			_ = os.RemoveAll(stage)
		}
	}()
	for _, entry := range selected {
		cached := cachePath(s.root, entry.SHA256)
		if info, err := os.Stat(cached); err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
			return nil, errors.New("selected package is not cached")
		}
		if ok, _ := verifyFileDigest(cached, entry.SHA256); !ok {
			return nil, errors.New("selected package cache is corrupt")
		}
		stage, err := stageFromCache(s.root, entry, cached)
		if err != nil {
			return nil, err
		}
		stages = append(stages, stage)
	}
	prepared := make([]PreparedPackage, 0, len(selected))
	for i, entry := range selected {
		if _, err := s.InstallStaged(stages[i], InstallOptions{Replace: true, Entry: &entry}); err != nil {
			return nil, err
		}
		prepared = append(prepared, PreparedPackage{ID: entry.ID, Version: entry.Version, SHA256: entry.SHA256, Size: entry.Size})
	}
	return prepared, nil
}

// checkSnapshotCurrent rejects a frozen source snapshot that differs
// from live requested state: every snapshot optional row must match the
// recorded intent generation, desired and pending values and
// installation UUID, and every live intent must be represented.
func checkSnapshotCurrent(root string, source Snapshot, requiredIDs []string) error {
	return checkSnapshotIntents(root, source, requiredIDs, func(string) (bool, error) { return true, nil })
}

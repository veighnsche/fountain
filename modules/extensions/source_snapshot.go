// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"errors"
	"os"
	"path/filepath"

	sdk "forgejo.org/extension-sdk"
)

// SourceTrust is supplied by the host after loading its trusted source
// catalog. The target release and a snapshot cannot establish these facts.
type SourceTrust struct {
	Catalog       sdk.Catalog
	CatalogSHA256 string
	RequiredIDs   []string
	ControllerID  string
}

var errSourceSnapshot = errors.New("source snapshot does not match current state")

func sourceSnapshotScope(root string, trust SourceTrust) func(string) (bool, error) {
	byID := make(map[string]sdk.CatalogPackage, len(trust.Catalog.Packages))
	for _, entry := range trust.Catalog.Packages {
		byID[entry.ID] = entry
	}
	return func(id string) (bool, error) {
		entry, catalogued := byID[id]
		if catalogued && entry.Required {
			return true, nil
		}
		if catalogued && entry.LifecycleController != trust.ControllerID {
			return false, nil
		}
		binding, known, err := LoadIntentController(root, id)
		if err != nil {
			return false, err
		}
		if catalogued {
			if known && binding.ControllerID != entry.LifecycleController {
				return false, errSourceSnapshot
			}
			return entry.LifecycleController == trust.ControllerID, nil
		}
		if !known {
			return false, errSourceSnapshot
		}
		return binding.ControllerID == trust.ControllerID, nil
	}
}

// VerifySourceSnapshot checks one controller's frozen source against trusted
// catalog identity and current local facts. Unknown legacy intent ownership
// is preserved outside the scope, never inferred from the receiving controller.
func VerifySourceSnapshot(root string, snapshot Snapshot, trust SourceTrust) error {
	if err := validateSourceTrust(snapshot, trust); err != nil {
		return err
	}
	required := make(map[string]sdk.CatalogPackage, len(trust.RequiredIDs))
	for _, entry := range trust.Catalog.Packages {
		if entry.Required {
			required[entry.ID] = entry
		}
	}
	inScope := sourceSnapshotScope(root, trust)
	seen := make(map[string]bool, len(snapshot.Entries))
	for _, row := range snapshot.Entries {
		included, err := inScope(row.PackageID)
		if err != nil || !included {
			return errSourceSnapshot
		}
		seen[row.PackageID] = true
		entry, isRequired := required[row.PackageID]
		if row.Required != isRequired {
			return errSourceSnapshot
		}
		if isRequired {
			intent, err := LoadIntent(root, row.PackageID)
			if err != nil || intent.PendingOperation != nil || intent.Generation != "0" && !intent.DesiredEnabled {
				return errSourceSnapshot
			}
			installation, err := LoadInstallation(root, row.PackageID)
			if err != nil || row.InstallationID == nil || *row.InstallationID != installation || installation == "" || row.Generation != "0" || !row.DesiredEnabled || row.PendingOperation != nil {
				return errSourceSnapshot
			}
			if err := ValidateInstalled(root, entry); err != nil {
				return errSourceSnapshot
			}
		}
		if err := checkSnapshotPackage(root, row); err != nil {
			return err
		}
	}
	for _, entry := range trust.Catalog.Packages {
		included, err := inScope(entry.ID)
		if err != nil || included && !seen[entry.ID] {
			return errSourceSnapshot
		}
	}
	return checkSnapshotIntents(root, snapshot, trust.RequiredIDs, inScope)
}

func validateSourceTrust(snapshot Snapshot, trust SourceTrust) error {
	if err := sdk.ValidateCatalog(trust.Catalog); err != nil {
		return err
	}
	if err := ValidateCatalogSelection(trust.Catalog, trust.RequiredIDs); err != nil {
		return err
	}
	if trust.CatalogSHA256 == "" || snapshot.CatalogSHA256 != trust.CatalogSHA256 || !sdk.ValidID(trust.ControllerID) {
		return errSourceSnapshot
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	required := make(map[string]sdk.CatalogPackage, len(trust.RequiredIDs))
	for _, entry := range trust.Catalog.Packages {
		if entry.Required {
			required[entry.ID] = entry
		}
	}
	if _, ok := required[trust.ControllerID]; !ok {
		return errSourceSnapshot
	}
	return nil
}

func checkSnapshotPackage(root string, row SnapshotEntry) error {
	directory := filepath.Join(root, row.PackageID)
	_, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if row.InstalledVersion != nil || row.PackageSHA256 != nil {
			return errSourceSnapshot
		}
		return nil
	}
	if err != nil {
		return errSourceSnapshot
	}
	origin, err := provenPackageOrigin(root, row.PackageID, directory)
	if err != nil || row.InstalledVersion == nil || *row.InstalledVersion != origin.Version {
		return errSourceSnapshot
	}
	if origin.Receipt == nil {
		if row.PackageSHA256 != nil {
			return errSourceSnapshot
		}
	} else if row.PackageSHA256 == nil || *row.PackageSHA256 != origin.Receipt.ArchiveSHA256 {
		return errSourceSnapshot
	}
	return nil
}

// checkSnapshotIntents is shared by trusted verification and the retained
// local-state-only preparation adapter. Its scope never derives from target bytes.
func checkSnapshotIntents(root string, source Snapshot, requiredIDs []string, inScope func(string) (bool, error)) error {
	required := make(map[string]bool, len(requiredIDs))
	for _, id := range requiredIDs {
		required[id] = true
	}
	seen := make(map[string]bool, len(source.Entries))
	for _, row := range source.Entries {
		if required[row.PackageID] {
			continue
		}
		seen[row.PackageID] = true
		intent, err := LoadIntent(root, row.PackageID)
		if err != nil || intent.Generation != row.Generation || intent.DesiredEnabled != row.DesiredEnabled || !samePending(intent.PendingOperation, row.PendingOperation) {
			return errSourceSnapshot
		}
		if row.Generation == "0" {
			if row.InstallationID != nil {
				return errSourceSnapshot
			}
			continue
		}
		if row.InstallationID == nil || *row.InstallationID != intent.InstallationID {
			return errSourceSnapshot
		}
		installation, err := LoadInstallation(root, row.PackageID)
		if err != nil || installation != intent.InstallationID {
			return errSourceSnapshot
		}
	}
	intents, err := listIntents(root)
	if err != nil {
		return errSourceSnapshot
	}
	for _, id := range intents {
		if required[id] || seen[id] {
			continue
		}
		included, err := inScope(id)
		if err != nil || included {
			return errSourceSnapshot
		}
	}
	return nil
}

func samePending(first, second *string) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return *first == *second
}

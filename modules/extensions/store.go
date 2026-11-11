// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdk "forgejo.org/extension-sdk"

	"github.com/gofrs/flock"
)

// PackageStore is the one lock-bearing package mutation handle (RCX-2
// F02). The running manager owns one handle for its lifetime;
// stopped-host CLI commands acquire their own through OpenPackageStore,
// which fails while the manager holds the lock. Both veins run the same
// validation and rename code below: this is a small lock-bearing wrapper,
// not a second installer. One package mutation at a time is enforced by
// the lock for CLI use and by the manager's mutation slot for live use.
type PackageStore struct {
	root  string
	lock  *flock.Flock
	owned bool
}

// OpenPackageStore acquires the package lock for offline mutations. It
// fails while the running manager owns the store, so stopped-host CLI
// commands stay refused without a lock-bypass helper.
func OpenPackageStore(root string) (*PackageStore, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	lock, err := AcquirePackageLock(absolute)
	if err != nil {
		return nil, err
	}
	return &PackageStore{root: absolute, lock: lock, owned: true}, nil
}

// PackageStoreFromLock wraps a package lock the caller already holds (the
// running manager's lifetime lock). Close releases nothing; the lock
// owner keeps releasing it as before.
func PackageStoreFromLock(root string, lock *flock.Flock) (*PackageStore, error) {
	if lock == nil {
		return nil, errors.New("extension package lock is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &PackageStore{root: absolute, lock: lock}, nil
}

// Root returns the store's package root.
func (s *PackageStore) Root() string { return s.root }

// Close releases the store handle. A handle from OpenPackageStore
// unlocks; a handle from PackageStoreFromLock only detaches.
func (s *PackageStore) Close() error {
	if s.owned {
		return s.lock.Close()
	}
	return nil
}

// InstallOptions selects replacement and, for catalog-acquired packages,
// the trusted entry the staged bytes must match.
type InstallOptions struct {
	Replace bool
	Entry   *sdk.CatalogPackage
}

// InstallStaged validates a staged package directory and swaps it into
// the store. Catalog-acquired stages must carry the entry's identity and
// generated receipt; prebuilt local stages (Entry nil) validate without
// a receipt. Replacement preserves the installation UUID and desired
// intent: old bytes move to the fixed task-owned .previous-<id>
// directory, the validated stage (receipt traveling atomically with it)
// moves into place, and the package root is fsynced. On failure old bytes
// are restored; a crash between the two renames is repaired by
// RecoverPackage before packages execute. Fresh installation assigns a
// new UUID.
func (s *PackageStore) InstallStaged(stage string, opts InstallOptions) (sdk.Manifest, error) {
	manifest, err := sdk.LoadManifest(stage)
	if err != nil {
		return sdk.Manifest{}, err
	}
	if err := checkPackageFiles(stage, manifest); err != nil {
		return sdk.Manifest{}, err
	}
	if opts.Entry != nil {
		if err := MatchEntryManifest(*opts.Entry, manifest); err != nil {
			return sdk.Manifest{}, err
		}
		if err := ValidateReceipt(stage, *opts.Entry); err != nil {
			return sdk.Manifest{}, err
		}
	}
	kind := prebuiltOrigin
	if opts.Entry != nil {
		kind = catalogOrigin
	}
	next, err := inspectPackageOrigin(stage, kind)
	if err != nil {
		return sdk.Manifest{}, err
	}
	// A replacement first repairs any interrupted swap for this
	// package, whether or not the target currently exists: a crash
	// between the old-bytes rename and staged publication leaves an
	// absent target whose valid backup restores here, so the swap
	// below preserves installation UUID, intent and private data.
	// Unrepairable state fails closed before anything moves.
	if err := RecoverPackage(s.root, manifest.ID); err != nil {
		return sdk.Manifest{}, err
	}
	target := filepath.Join(s.root, manifest.ID)
	info, err := os.Lstat(target)
	if err == nil {
		if !opts.Replace {
			return sdk.Manifest{}, fmt.Errorf("extension %q is already installed; use --replace to update it", manifest.ID)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return sdk.Manifest{}, errors.New("installed package is not a regular directory")
		}
		previous, err := provenPackageOrigin(s.root, manifest.ID, target)
		if err != nil {
			return sdk.Manifest{}, errRecoveryUnavailable
		}
		state := packageState{
			Schema: 1, PackageID: manifest.ID, Current: previous,
			Replacement: &packageReplacement{Previous: previous, Next: next, Target: manifest.ID, Backup: ".previous-" + manifest.ID},
		}
		if err := writePackageState(s.root, state); err != nil {
			return sdk.Manifest{}, err
		}
		backup := previousPackagePath(s.root, manifest.ID)
		if err := os.Rename(target, backup); err != nil {
			return sdk.Manifest{}, err
		}
		if err := os.Rename(stage, target); err != nil {
			restoreErr := os.Rename(backup, target)
			if restoreErr == nil {
				restoreErr = writePackageState(s.root, packageState{Schema: 1, PackageID: manifest.ID, Current: previous})
			}
			return sdk.Manifest{}, errors.Join(err, restoreErr)
		}
		if err := directorySync(s.root); err != nil {
			if rollbackErr := rollbackInstallSwap(s.root, manifest.ID); rollbackErr != nil {
				return manifest, fmt.Errorf("package installation failed (%v) and rollback is incomplete: %v", err, rollbackErr)
			}
			if stateErr := writePackageState(s.root, packageState{Schema: 1, PackageID: manifest.ID, Current: previous}); stateErr != nil {
				return sdk.Manifest{}, errors.Join(err, stateErr)
			}
			return sdk.Manifest{}, fmt.Errorf("package installation failed (%v); previous version restored", err)
		}
		if err := os.RemoveAll(backup); err != nil {
			return manifest, fmt.Errorf("package updated but old package cleanup failed: %w", err)
		}
		if err := fsyncDirectory(s.root); err != nil {
			return manifest, err
		}
		if err := writePackageState(s.root, packageState{Schema: 1, PackageID: manifest.ID, Current: next}); err != nil {
			return manifest, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return sdk.Manifest{}, err
	} else {
		if err := writePackageState(s.root, packageState{Schema: 1, PackageID: manifest.ID, Current: next}); err != nil {
			return sdk.Manifest{}, err
		}
		if err := os.Rename(stage, target); err != nil {
			return sdk.Manifest{}, err
		}
		if err := fsyncDirectory(s.root); err != nil {
			return sdk.Manifest{}, err
		}
	}
	// Fresh installation assigns a new UUID; replacement preserves the
	// recorded one. Identity failures leave the package installed but the
	// installation incomplete, like package cleanup failures above.
	if _, err := EnsureInstallation(s.root, manifest.ID); err != nil {
		return manifest, errors.New("package installed but installation identity is unavailable")
	}
	return manifest, nil
}

// InstallPrebuilt stages a prebuilt package directory through the store
// without executing its contents. Existing packages require replace.
// Persistent extension data lives outside packages.
func (s *PackageStore) InstallPrebuilt(source string, replace bool) (sdk.Manifest, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return sdk.Manifest{}, err
	}
	if relative, err := filepath.Rel(absolute, s.root); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return sdk.Manifest{}, errors.New("package source must not contain the installation directory")
	}
	stage, err := os.MkdirTemp(s.root, ".install-")
	if err != nil {
		return sdk.Manifest{}, err
	}
	defer os.RemoveAll(stage)
	if err := copyPackage(absolute, stage); err != nil {
		return sdk.Manifest{}, err
	}
	return s.InstallStaged(stage, InstallOptions{Replace: replace})
}

// StoreIntent persists requested state through the store's serialization.
// It is the only writer beside the manager's own handle; offline intent
// changes and live intent changes share this compare-and-swap.
func (s *PackageStore) StoreIntent(id, installationID string, desired bool, pending *string, expectedGeneration string) (Intent, error) {
	return StoreIntent(s.root, id, installationID, desired, pending, expectedGeneration)
}

// SetDesired records next-start activation for an installed package
// without touching backends: it allocates the installation UUID, then
// compare-and-swaps the intent one generation forward with no pending
// interruption. It never deletes extension data.
func (s *PackageStore) SetDesired(id string, desired bool) (Intent, error) {
	if !sdk.ValidID(id) {
		return Intent{}, errors.New("invalid extension id")
	}
	directory := filepath.Join(s.root, id)
	info, err := os.Lstat(directory)
	if err != nil {
		return Intent{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Intent{}, errors.New("installed package is not a regular directory")
	}
	manifest, err := sdk.LoadManifest(directory)
	if err != nil {
		return Intent{}, err
	}
	if manifest.ID != id {
		return Intent{}, errors.New("installed directory does not match manifest id")
	}
	installation, err := EnsureInstallation(s.root, id)
	if err != nil {
		return Intent{}, err
	}
	current, err := LoadIntent(s.root, id)
	if err != nil {
		return Intent{}, err
	}
	return StoreIntent(s.root, id, installation, desired, nil, current.Generation)
}

// Remove deletes an installed package through the store. Private data
// under .data is deliberately outside the package directory and is
// retained. Removal retires the installation UUID and the requested
// state so a later installation cannot adopt this installation's
// operations.
func (s *PackageStore) Remove(id string) error {
	if !sdk.ValidID(id) {
		return errors.New("invalid extension id")
	}
	directory := filepath.Join(s.root, id)
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("installed package is not a regular directory")
	}
	if err := os.RemoveAll(directory); err != nil {
		return err
	}
	if err := RemoveInstallation(s.root, id); err != nil {
		return err
	}
	if err := RemoveIntent(s.root, id); err != nil {
		return err
	}
	if err := os.Remove(packageStatePath(s.root, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// previousPackagePath is the fixed task-owned backup directory for one
// package replacement. A fixed name (not a stage-derived one) keeps crash
// recovery exact: only this path is ever inspected or removed.
func previousPackagePath(root, id string) string {
	return filepath.Join(root, ".previous-"+id)
}

// directorySync persists the package root after a swap. Tests override it
// to force the fsync-failure rollback path; production always syncs.
var directorySync = fsyncDirectory

// rollbackInstallSwap restores the previous package after the root sync
// fails with the new bytes already at the target. Linux cannot rename the
// backup over a populated directory, so the new target first moves aside
// into a task-owned sibling, the backup returns to the target, and the
// root syncs before the aside copy is dropped. Both packages and their
// evidence survive every step: if the rollback itself fails, both
// locations stay intact and the error names exactly where the previous
// version is. No unrelated directory is touched.
func rollbackInstallSwap(root, id string) error {
	target := filepath.Join(root, id)
	backup := previousPackagePath(root, id)
	stray, err := os.MkdirTemp(root, ".rollback-")
	if err != nil {
		return fmt.Errorf("rollback cannot stage aside %s: %v; new bytes at target, previous version at %s", target, err, backup)
	}
	strayPkg := filepath.Join(stray, id)
	removeStrayDir := func() {
		_ = os.Remove(strayPkg)
		_ = os.Remove(stray)
	}
	if err := os.Rename(target, strayPkg); err != nil {
		removeStrayDir()
		return fmt.Errorf("rollback cannot move new bytes aside: %v; new bytes at target, previous version at %s", err, backup)
	}
	if err := os.Rename(backup, target); err != nil {
		_ = os.Rename(strayPkg, target)
		removeStrayDir()
		return fmt.Errorf("rollback cannot restore previous version: %v; previous version at %s", err, backup)
	}
	if err := fsyncDirectory(root); err != nil {
		return fmt.Errorf("rollback sync failed: %v; previous version restored at target, new bytes preserved at %s", err, strayPkg)
	}
	return os.RemoveAll(stray)
}

// RecoverPackage repairs an interrupted replacement before packages
// execute. If the target is absent and its valid backup exists, the
// backup is restored. If both exist, the target's generated receipt and
// package are validated before removing that exact backup. An
// invalid or incomplete target is unavailable and does not execute; the
// backup is kept for inspection. Unrelated directories are never scanned
// or pruned, and no download or external activation is part of this
// filesystem repair.
func RecoverPackage(root, id string) error {
	if !sdk.ValidID(id) {
		return errors.New("invalid extension id")
	}
	target := filepath.Join(root, id)
	backup := previousPackagePath(root, id)
	_, targetErr := os.Lstat(target)
	_, backupErr := os.Lstat(backup)
	targetExists := targetErr == nil
	backupExists := backupErr == nil
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return targetErr
	}
	if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
		return backupErr
	}
	state, stateErr := loadPackageState(root, id)
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return errRecoveryUnavailable
	}
	if state.Replacement != nil {
		return recoverWitnessedReplacement(root, id, state, targetExists, backupExists)
	}
	if !backupExists {
		if targetExists && stateErr == nil {
			return state.Current.verify(target)
		}
		return nil
	}
	previous, err := provenPackageOrigin(root, id, backup)
	if err != nil {
		if stateErr == nil && !targetExists {
			return errRecoveryUnavailable
		}
		// A legacy catalog backup may predate the current origin record.
		previous, err = inspectPackageOrigin(backup, catalogOrigin)
		if err != nil || previous.PackageID != id {
			return errRecoveryUnavailable
		}
	}
	current := previous
	if targetExists {
		current, err = provenPackageOrigin(root, id, target)
		if err != nil {
			return errRecoveryUnavailable
		}
		if err := fsyncDirectory(root); err != nil {
			return err
		}
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
	} else if err := os.Rename(backup, target); err != nil {
		return err
	}
	if err := fsyncDirectory(root); err != nil {
		return err
	}
	return writePackageState(root, packageState{Schema: 1, PackageID: id, Current: current})
}

func recoverWitnessedReplacement(root, id string, state packageState, targetExists, backupExists bool) error {
	target, backup := filepath.Join(root, id), previousPackagePath(root, id)
	witness := state.Replacement
	var current packageOrigin
	switch {
	case !targetExists && !backupExists:
		return errRecoveryUnavailable
	case !targetExists:
		if err := witness.Previous.verify(backup); err != nil {
			return err
		}
		if err := os.Rename(backup, target); err != nil {
			return err
		}
		current = witness.Previous
	case !backupExists:
		if witness.Next.verify(target) == nil {
			current = witness.Next
		} else if witness.Previous.verify(target) == nil {
			current = witness.Previous
		} else {
			return errRecoveryUnavailable
		}
	default:
		if witness.Next.verify(target) != nil || witness.Previous.verify(backup) != nil {
			return errRecoveryUnavailable
		}
		if err := fsyncDirectory(root); err != nil {
			return err
		}
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
		current = witness.Next
	}
	if err := fsyncDirectory(root); err != nil {
		return err
	}
	return writePackageState(root, packageState{Schema: 1, PackageID: id, Current: current})
}

// validatePackageDir checks that an installed directory is a regular
// directory carrying a loadable receipt, manifest and package files.
func validatePackageDir(directory string) error {
	_, err := inspectPackageOrigin(directory, catalogOrigin)
	return err
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"encoding/json" //nolint:depguard // Exact filesystem recovery fixtures.
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "forgejo.org/extension-sdk"

	"github.com/stretchr/testify/require"
)

func originPrebuilt(t *testing.T, id, version string) string {
	t.Helper()
	directory := t.TempDir()
	raw, err := json.Marshal(sdk.Manifest{Protocol: sdk.Protocol, ID: id, Name: id, Version: version, Executable: "backend"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "extension.json"), raw, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "backend"), []byte("prebuilt "+version), 0o700))
	return directory
}

func TestCatalogOriginPreservesPayloadLimitAndHashesGeneratedReceipt(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, root)
	stage := originPrebuilt(t, "sample", "1.0")
	for i := range 9998 {
		require.NoError(t, os.WriteFile(filepath.Join(stage, fmt.Sprintf("payload-%04d", i)), nil, 0o600))
	}
	entry := sdk.CatalogPackage{ID: "sample", Version: "1.0", FountainProto: sdk.Protocol, SHA256: strings.Repeat("a", 64)}
	require.NoError(t, WriteReceipt(stage, entry))
	_, err := store.InstallStaged(stage, InstallOptions{Entry: &entry})
	require.NoError(t, err, "generated receipt must not reduce the supported 10,000-file payload")
	require.NoError(t, RecoverPackage(root, "sample"))
	directory := filepath.Join(root, "sample")
	receiptPath := filepath.Join(directory, receiptFileName)
	receipt, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(receiptPath, append(receipt, '\n'), 0o600))
	require.ErrorIs(t, RecoverPackage(root, "sample"), errRecoveryUnavailable, "receipt bytes remain part of the origin witness")
	require.NoError(t, os.WriteFile(receiptPath, receipt, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "overflow"), nil, 0o600))
	_, err = inspectPackageOrigin(directory, catalogOrigin)
	require.ErrorContains(t, err, "exceeds 10000 files")
}

func TestOriginOnlyPackageIsDiscoveredAndFencedBeforeExecution(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, root)
	_, err := store.InstallPrebuilt(originPrebuilt(t, "sample", "1.0"), false)
	require.NoError(t, err)
	require.NoError(t, os.Remove(InstallationPath(root, "sample")))
	require.Equal(t, []string{"sample"}, RecoveryCandidates(root, nil, nil))
	require.NoError(t, ValidateKnownPackageOrigin(root, "sample"))
	backend := filepath.Join(root, "sample", "backend")
	require.NoError(t, os.WriteFile(backend, []byte("changed after publication"), 0o700))
	failures := store.RecoverInterrupted(RecoveryCandidates(root, nil, nil))
	require.ErrorIs(t, failures["sample"], errRecoveryUnavailable)
	require.ErrorIs(t, ValidateKnownPackageOrigin(root, "sample"), errRecoveryUnavailable)
	preserved, err := os.ReadFile(backend)
	require.NoError(t, err)
	require.Equal(t, "changed after publication", string(preserved))
	require.NoError(t, os.Remove(packageStatePath(root, "sample")))
	require.NoError(t, ValidateKnownPackageOrigin(root, "sample"), "unproven historical current packages keep their runtime compatibility")
}

func TestPrebuiltRecoveryUsesDurableReplacementWitness(t *testing.T) {
	for _, point := range []string{"before rename", "between renames", "after publish", "after backup removal"} {
		t.Run(point, func(t *testing.T) {
			root := t.TempDir()
			store := openTestStore(t, root)
			_, err := store.InstallPrebuilt(originPrebuilt(t, "sample", "1.0"), false)
			require.NoError(t, err)
			intent, err := store.SetDesired("sample", true)
			require.NoError(t, err)
			state, err := loadPackageState(root, "sample")
			require.NoError(t, err)
			stage, err := os.MkdirTemp(root, ".install-")
			require.NoError(t, err)
			require.NoError(t, copyPackage(originPrebuilt(t, "sample", "2.0"), stage))
			next, err := inspectPackageOrigin(stage, prebuiltOrigin)
			require.NoError(t, err)
			state.Replacement = &packageReplacement{Previous: state.Current, Next: next, Target: "sample", Backup: ".previous-sample"}
			require.NoError(t, writePackageState(root, state))
			expected := "1.0"
			if point != "before rename" {
				require.NoError(t, os.Rename(filepath.Join(root, "sample"), previousPackagePath(root, "sample")))
			}
			if point == "after publish" || point == "after backup removal" {
				require.NoError(t, os.Rename(stage, filepath.Join(root, "sample")))
				expected = "2.0"
			}
			if point == "after backup removal" {
				require.NoError(t, os.RemoveAll(previousPackagePath(root, "sample")))
			}
			require.NoError(t, RecoverPackage(root, "sample"))
			require.NoError(t, RecoverPackage(root, "sample"))
			manifest, err := sdk.LoadManifest(filepath.Join(root, "sample"))
			require.NoError(t, err)
			require.Equal(t, expected, manifest.Version)
			_, err = LoadReceipt(filepath.Join(root, "sample"))
			require.ErrorIs(t, err, os.ErrNotExist, "prebuilt recovery must not invent an archive receipt")
			preserved, err := LoadIntent(root, "sample")
			require.NoError(t, err)
			require.Equal(t, intent, preserved)
			state, err = loadPackageState(root, "sample")
			require.NoError(t, err)
			require.Nil(t, state.Replacement)
			require.Equal(t, prebuiltOrigin, state.Current.Kind)
			require.NoDirExists(t, previousPackagePath(root, "sample"))
		})
	}
}

func TestReplacementPreservesUnprovenLegacyAndMissingCatalogReceipt(t *testing.T) {
	t.Run("legacy receiptless current and backup", func(t *testing.T) {
		root := t.TempDir()
		store := openTestStore(t, root)
		_, err := store.InstallPrebuilt(originPrebuilt(t, "sample", "1.0"), false)
		require.NoError(t, err)
		require.NoError(t, os.Remove(packageStatePath(root, "sample")))
		require.NoError(t, RecoverPackage(root, "sample"), "normal legacy current state is left intact")
		_, err = store.InstallPrebuilt(originPrebuilt(t, "sample", "2.0"), true)
		require.ErrorIs(t, err, errRecoveryUnavailable)
		manifest, err := sdk.LoadManifest(filepath.Join(root, "sample"))
		require.NoError(t, err)
		require.Equal(t, "1.0", manifest.Version)
		require.NoError(t, os.Rename(filepath.Join(root, "sample"), previousPackagePath(root, "sample")))
		require.ErrorIs(t, RecoverPackage(root, "sample"), errRecoveryUnavailable)
		require.DirExists(t, previousPackagePath(root, "sample"))
		require.NoDirExists(t, filepath.Join(root, "sample"))
	})
	t.Run("catalog receipt removed", func(t *testing.T) {
		root := t.TempDir()
		store := openTestStore(t, root)
		server, entry := serveArchive(t, versionedArchive(t, "sample", "1.0", "", nil), "sample", "1.0")
		installAcquired(t, store, entry, testClient(t, server), false)
		require.NoError(t, os.Remove(filepath.Join(root, "sample", receiptFileName)))
		_, err := store.InstallPrebuilt(originPrebuilt(t, "sample", "2.0"), true)
		require.ErrorIs(t, err, errRecoveryUnavailable)
		state, err := loadPackageState(root, "sample")
		require.NoError(t, err)
		require.Equal(t, catalogOrigin, state.Current.Kind)
		require.Nil(t, state.Replacement)
	})
}

func TestLegacyCatalogReceiptMigratesOriginDuringRecovery(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, root)
	server, entry := serveArchive(t, versionedArchive(t, "sample", "1.0", "", nil), "sample", "1.0")
	installAcquired(t, store, entry, testClient(t, server), false)
	require.NoError(t, os.Remove(packageStatePath(root, "sample")))
	require.NoError(t, os.Rename(filepath.Join(root, "sample"), previousPackagePath(root, "sample")))
	require.NoError(t, RecoverPackage(root, "sample"))
	require.NoError(t, ValidateInstalled(root, entry))
	state, err := loadPackageState(root, "sample")
	require.NoError(t, err)
	require.Equal(t, catalogOrigin, state.Current.Kind)
	require.Equal(t, entry.SHA256, state.Current.Receipt.ArchiveSHA256)
}

func TestWitnessRefusesChangedPrebuiltBytesAndForgedPaths(t *testing.T) {
	for _, tamper := range []string{"backup bytes", "witness path"} {
		t.Run(tamper, func(t *testing.T) {
			root := t.TempDir()
			store := openTestStore(t, root)
			_, err := store.InstallPrebuilt(originPrebuilt(t, "sample", "1.0"), false)
			require.NoError(t, err)
			state, err := loadPackageState(root, "sample")
			require.NoError(t, err)
			state.Replacement = &packageReplacement{Previous: state.Current, Next: state.Current, Target: "sample", Backup: ".previous-sample"}
			if tamper == "witness path" {
				state.Replacement.Backup = "../unrelated"
			}
			require.NoError(t, writePackageState(root, state))
			require.NoError(t, os.Rename(filepath.Join(root, "sample"), previousPackagePath(root, "sample")))
			if tamper == "backup bytes" {
				require.NoError(t, os.WriteFile(filepath.Join(previousPackagePath(root, "sample"), "backend"), []byte("changed"), 0o700))
			}
			require.True(t, errors.Is(RecoverPackage(root, "sample"), errRecoveryUnavailable))
			require.DirExists(t, previousPackagePath(root, "sample"))
			require.NoDirExists(t, filepath.Join(root, "sample"))
		})
	}
}

func TestKnownCurrentOriginRemainsRequiredWithoutBackup(t *testing.T) {
	for _, kind := range []string{prebuiltOrigin, catalogOrigin, "catalog bytes"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			store := openTestStore(t, root)
			if kind == prebuiltOrigin {
				_, err := store.InstallPrebuilt(originPrebuilt(t, "sample", "1.0"), false)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "sample", "backend"), []byte("changed"), 0o700))
			} else {
				server, entry := serveArchive(t, versionedArchive(t, "sample", "1.0", "", nil), "sample", "1.0")
				installAcquired(t, store, entry, testClient(t, server), false)
				if kind == "catalog bytes" {
					require.NoError(t, os.WriteFile(filepath.Join(root, "sample", "backend"), []byte("changed"), 0o755))
				} else {
					require.NoError(t, os.Remove(filepath.Join(root, "sample", receiptFileName)))
				}
			}
			require.ErrorIs(t, RecoverPackage(root, "sample"), errRecoveryUnavailable)
			require.DirExists(t, filepath.Join(root, "sample"))
			require.NoDirExists(t, previousPackagePath(root, "sample"))
			require.NoError(t, os.Rename(filepath.Join(root, "sample"), previousPackagePath(root, "sample")))
			require.ErrorIs(t, RecoverPackage(root, "sample"), errRecoveryUnavailable, "a legacy-receipt fallback cannot bypass known current proof")
			require.DirExists(t, previousPackagePath(root, "sample"))
		})
	}
}

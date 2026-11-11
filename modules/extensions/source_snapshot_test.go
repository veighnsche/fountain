// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "forgejo.org/extension-sdk"

	"github.com/stretchr/testify/require"
)

func trustedPrepareFixture(t *testing.T) (string, sdk.Catalog, Snapshot, SourceTrust) {
	t.Helper()
	root := t.TempDir()
	target, snapshot, _ := prepareFixture(t, root)
	source := sdk.Catalog{Version: target.Version, Packages: slices.Clone(target.Packages)}
	for i := range source.Packages {
		receipt, err := LoadReceipt(filepath.Join(root, source.Packages[i].ID))
		require.NoError(t, err)
		source.Packages[i].Version, source.Packages[i].SHA256 = receipt.PackageVersion, receipt.ArchiveSHA256
	}
	installation, err := LoadInstallation(root, "soda")
	require.NoError(t, err)
	version := source.Packages[0].Version
	snapshot.Entries[0].InstallationID, snapshot.Entries[0].InstalledVersion = &installation, &version
	snapshot.Entries[0].PackageSHA256 = entrySHA(t, root, "soda")
	tailnetInstallation, err := LoadInstallation(root, "tailnet")
	require.NoError(t, err)
	require.NoError(t, BindIntentController(root, "tailnet", tailnetInstallation, "soda"))
	return root, target, snapshot, SourceTrust{Catalog: source, CatalogSHA256: snapshot.CatalogSHA256, RequiredIDs: []string{"soda"}, ControllerID: "soda"}
}

func TestTrustedPreparationUsesSourceCatalogIndependentlyOfTarget(t *testing.T) {
	root, target, snapshot, trust := trustedPrepareFixture(t)
	require.NoError(t, VerifySourceSnapshot(root, snapshot, trust))
	wrongSource := trust
	wrongSource.Catalog = target
	require.Error(t, VerifySourceSnapshot(root, snapshot, wrongSource), "target package pins cannot prove the source release")
	before, err := os.ReadFile(IntentPath(root, "tailnet"))
	require.NoError(t, err)
	store := openTestStore(t, root)
	prepared, err := store.PrepareReleaseFromSource(target, snapshot, []string{"soda"}, trust)
	require.NoError(t, err)
	require.Len(t, prepared, 2)
	require.NoError(t, ValidateInstalled(root, target.Packages[0]))
	require.NoError(t, ValidateInstalled(root, target.Packages[1]))
	after, err := os.ReadFile(IntentPath(root, "tailnet"))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestSourceSnapshotVerifiesRequiredPendingAndCompleteLiveScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string, *Snapshot, *SourceTrust)
	}{
		{"catalog SHA", func(_ *testing.T, _ string, snapshot *Snapshot, _ *SourceTrust) {
			snapshot.CatalogSHA256 = strings.Repeat("d", 64)
		}},
		{"required row omitted", func(_ *testing.T, _ string, snapshot *Snapshot, _ *SourceTrust) {
			snapshot.Entries = snapshot.Entries[1:]
		}},
		{"required UUID missing", func(_ *testing.T, _ string, snapshot *Snapshot, _ *SourceTrust) {
			snapshot.Entries[0].InstallationID = nil
		}},
		{"required pin changed", func(_ *testing.T, _ string, snapshot *Snapshot, _ *SourceTrust) {
			value := strings.Repeat("a", 64)
			snapshot.Entries[0].PackageSHA256 = &value
		}},
		{"pending operation changed", func(_ *testing.T, _ string, snapshot *Snapshot, _ *SourceTrust) {
			value := IntentActivate
			snapshot.Entries[1].PendingOperation = &value
		}},
		{"live optional omitted", func(_ *testing.T, _ string, snapshot *Snapshot, _ *SourceTrust) {
			snapshot.Entries = snapshot.Entries[:2]
		}},
		{"required flag forged", func(_ *testing.T, _ string, snapshot *Snapshot, _ *SourceTrust) { snapshot.Entries[1].Required = true }},
		{"unproven installed origin", func(t *testing.T, root string, _ *Snapshot, _ *SourceTrust) {
			require.NoError(t, os.Remove(packageStatePath(root, "other")))
			require.NoError(t, os.Remove(filepath.Join(root, "other", receiptFileName)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _, snapshot, trust := trustedPrepareFixture(t)
			tc.change(t, root, &snapshot, &trust)
			require.Error(t, VerifySourceSnapshot(root, snapshot, trust))
		})
	}
}

func TestSourceSnapshotPreservesControllerAttributionAfterCatalogRemoval(t *testing.T) {
	root, _, snapshot, trust := trustedPrepareFixture(t)
	trust.Catalog.Packages = trust.Catalog.Packages[:2]
	trust.CatalogSHA256, snapshot.CatalogSHA256 = strings.Repeat("d", 64), strings.Repeat("d", 64)
	require.NoError(t, VerifySourceSnapshot(root, snapshot, trust), "retained host attribution proves the removed package's controller")
	require.NoError(t, os.Remove(IntentControllerPath(root, "other")))
	require.Error(t, VerifySourceSnapshot(root, snapshot, trust), "legacy removed rows without attribution remain fenced")
	installation, err := LoadInstallation(root, "other")
	require.NoError(t, err)
	require.NoError(t, BindIntentController(root, "other", installation, "another-controller"))
	snapshot.Entries = snapshot.Entries[:2]
	require.NoError(t, VerifySourceSnapshot(root, snapshot, trust), "a known other controller's retained row is excluded")
}

func TestTrustedPreparationStagesEveryTargetBeforeReplacing(t *testing.T) {
	root, target, snapshot, trust := trustedPrepareFixture(t)
	before, err := sdk.LoadManifest(filepath.Join(root, "soda"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(cachePath(root, target.Packages[1].SHA256)))
	store := openTestStore(t, root)
	_, err = store.PrepareReleaseFromSource(target, snapshot, []string{"soda"}, trust)
	require.ErrorContains(t, err, "not cached")
	after, err := sdk.LoadManifest(filepath.Join(root, "soda"))
	require.NoError(t, err)
	require.Equal(t, before.Version, after.Version)
	require.NoDirExists(t, previousPackagePath(root, "soda"))
}

func TestSourceSnapshotExcludesMalformedKnownOtherControllerState(t *testing.T) {
	root, _, snapshot, trust := trustedPrepareFixture(t)
	store := openTestStore(t, root)
	server, controller := serveArchive(t, versionedArchive(t, "alternate", "1.0", "", nil), "alternate", "1.0")
	controller.Required = true
	installAcquired(t, store, controller, testClient(t, server), false)
	trust.Catalog.Packages = append(trust.Catalog.Packages, controller)
	trust.RequiredIDs = append(trust.RequiredIDs, controller.ID)
	installation, err := LoadInstallation(root, controller.ID)
	require.NoError(t, err)
	snapshot.Entries = append(snapshot.Entries, SnapshotEntry{PackageID: controller.ID, InstallationID: &installation,
		Generation: "0", DesiredEnabled: true, Required: true, InstalledVersion: &controller.Version, PackageSHA256: &controller.SHA256})
	trust.Catalog.Packages = append(trust.Catalog.Packages, sdk.CatalogPackage{
		ID: "foreign", Version: "1.0", URL: "https://example.invalid/foreign.tar.gz", SHA256: strings.Repeat("f", 64), Size: 10,
		FountainProto: sdk.Protocol, BrowserContract: sdk.BrowserContractVersion, LifecycleController: controller.ID,
	})
	require.NoError(t, os.WriteFile(IntentPath(root, "foreign"), []byte("malformed other-controller state"), 0o600))
	require.NoError(t, VerifySourceSnapshot(root, snapshot, trust))
}

func TestSourceSnapshotRefusesContradictoryRequiredLiveIntent(t *testing.T) {
	for _, state := range []string{"disabled", "pending", "malformed", "affirmative"} {
		t.Run(state, func(t *testing.T) {
			root, _, snapshot, trust := trustedPrepareFixture(t)
			installation, err := LoadInstallation(root, "soda")
			require.NoError(t, err)
			var pending *string
			if state == "pending" {
				pending = new(IntentActivate)
			}
			_, err = StoreIntent(root, "soda", installation, state != "disabled", pending, "0")
			require.NoError(t, err)
			if state == "malformed" {
				require.NoError(t, os.WriteFile(IntentPath(root, "soda"), []byte(`{"schema":`), 0o600))
			}
			err = VerifySourceSnapshot(root, snapshot, trust)
			if state == "affirmative" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

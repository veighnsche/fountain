// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func stoppedLockFixture(t *testing.T) (string, string, RefLockRetirementProof) {
	t.Helper()
	repository := t.TempDir()
	require.NoError(t, InitRepository(t.Context(), repository, true, "sha1"))
	proof := RefLockRetirementProof{
		StoppedDomain: true, InstallationID: "installation", OperationID: "merge-1",
		Owner: "cond:installation/merge-1", Generation: 3, Ref: "refs/heads/main", NewOID: strings.Repeat("a", 40), ObjectFormat: "sha1",
	}
	path := filepath.Join(repository, "refs", "heads", "main.lock")
	require.NoError(t, os.WriteFile(path, []byte(proof.NewOID+"\n"), 0o600))
	return repository, path, proof
}

func TestRetireStoppedRefLockRequiresExactOperationAndContents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string, *RefLockRetirementProof)
	}{
		{"live domain", func(_ *testing.T, _ string, proof *RefLockRetirementProof) { proof.StoppedDomain = false }},
		{"different operation", func(_ *testing.T, _ string, proof *RefLockRetirementProof) { proof.OperationID = "merge-2" }},
		{"missing generation", func(_ *testing.T, _ string, proof *RefLockRetirementProof) { proof.Generation = 0 }},
		{"different OID", func(_ *testing.T, _ string, proof *RefLockRetirementProof) { proof.NewOID = strings.Repeat("b", 40) }},
		{"abbreviated OID lock", func(t *testing.T, path string, proof *RefLockRetirementProof) {
			proof.NewOID = "abcd"
			require.NoError(t, os.WriteFile(path, []byte(proof.NewOID+"\n"), 0o600))
		}},
		{"wrong object format", func(_ *testing.T, _ string, proof *RefLockRetirementProof) {
			proof.ObjectFormat, proof.NewOID = "sha256", strings.Repeat("a", 64)
		}},
		{"path traversal", func(_ *testing.T, _ string, proof *RefLockRetirementProof) { proof.Ref = "refs/heads/../../outside" }},
		{"extra lock bytes", func(t *testing.T, path string, proof *RefLockRetirementProof) {
			require.NoError(t, os.WriteFile(path, []byte(proof.NewOID+"\nextra"), 0o600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository, path, proof := stoppedLockFixture(t)
			tc.change(t, path, &proof)
			require.Error(t, RetireStoppedRefLock(t.Context(), repository, proof))
			require.FileExists(t, path)
		})
	}
	t.Run("exact stopped files lock", func(t *testing.T) {
		repository, path, proof := stoppedLockFixture(t)
		require.NoError(t, RetireStoppedRefLock(t.Context(), repository, proof))
		require.NoFileExists(t, path)
		require.NoError(t, RetireStoppedRefLock(t.Context(), repository, proof), "an already absent exact lock needs no deletion")
	})
	t.Run("pre-admission capture has no lock to retire", func(t *testing.T) {
		repository, path, proof := stoppedLockFixture(t)
		proof.NewOID = ""
		require.ErrorIs(t, RetireStoppedRefLock(t.Context(), repository, proof), ErrRefLockRetirementEvidence)
		require.FileExists(t, path, "without an admitted OID an existing lock is never attributable")
		require.NoError(t, os.Remove(path))
		require.NoError(t, RetireStoppedRefLock(t.Context(), repository, proof))
	})
	t.Run("SHA256 files lock", func(t *testing.T) {
		if ObjectFormatFromName("sha256") == nil {
			t.Skip("configured Git does not support SHA256 repositories")
		}
		repository := t.TempDir()
		require.NoError(t, InitRepository(t.Context(), repository, true, "sha256"))
		proof := RefLockRetirementProof{
			StoppedDomain: true, InstallationID: "installation", OperationID: "merge-1",
			Owner: "cond:installation/merge-1", Generation: 3, Ref: "refs/heads/main", NewOID: strings.Repeat("a", 64), ObjectFormat: "sha256",
		}
		path := filepath.Join(repository, "refs", "heads", "main.lock")
		require.NoError(t, os.WriteFile(path, []byte(proof.NewOID+"\n"), 0o600))
		require.NoError(t, RetireStoppedRefLock(t.Context(), repository, proof))
		require.NoFileExists(t, path)
	})
}

func TestRetireStoppedRefLockRefusesUnsupportedBackendAndSymlinks(t *testing.T) {
	t.Run("unsupported ref storage", func(t *testing.T) {
		repository, path, proof := stoppedLockFixture(t)
		_, _, err := NewCommand(t.Context(), "config", "--local", "extensions.refStorage", "unknown-backend").RunStdString(&RunOpts{Dir: repository})
		require.NoError(t, err)
		require.ErrorIs(t, RetireStoppedRefLock(t.Context(), repository, proof), ErrRefLockRetirementBackend)
		require.FileExists(t, path)
	})
	for _, location := range []string{"lock", "ref directory"} {
		t.Run(location, func(t *testing.T) {
			repository, path, proof := stoppedLockFixture(t)
			outside := t.TempDir()
			outsideLock := filepath.Join(outside, "main.lock")
			require.NoError(t, os.WriteFile(outsideLock, []byte(proof.NewOID+"\n"), 0o600))
			if location == "lock" {
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink(outsideLock, path))
			} else {
				require.NoError(t, os.RemoveAll(filepath.Dir(path)))
				require.NoError(t, os.Symlink(outside, filepath.Dir(path)))
			}
			require.ErrorIs(t, RetireStoppedRefLock(t.Context(), repository, proof), ErrRefLockRetirementEvidence)
			require.FileExists(t, outsideLock)
		})
	}
}

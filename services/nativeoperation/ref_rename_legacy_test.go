// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"path/filepath"
	"testing"

	"forgejo.org/modules/git"

	"github.com/hashicorp/go-version"
	"github.com/stretchr/testify/require"
)

func TestRenameHeadsSupportsMinimumGitPorcelain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bare.git")
	require.NoError(t, git.InitRepository(t.Context(), path, true, "sha1"))
	previous := git.GitVersion
	git.GitVersion = version.Must(version.NewVersion("2.34.1"))
	t.Cleanup(func() { git.GitVersion = previous })
	heads, err := renameHeads(t.Context(), path)
	require.NoError(t, err)
	require.Len(t, heads, 1)
	require.Contains(t, heads, path)
	require.NotEmpty(t, heads[path])
}

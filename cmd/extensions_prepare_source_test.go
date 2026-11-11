// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestExtensionPreparationRequiresIndependentSourceTrust(t *testing.T) {
	for _, flags := range [][]string{
		{"--offline", "--catalog", "/target.json", "--snapshot", "/snapshot.json"},
		{"--offline", "--catalog", "/target.json", "--snapshot", "/snapshot.json", "--source-catalog", "/source.json"},
	} {
		app := &cli.Command{Commands: []*cli.Command{cmdExtensions()}}
		args := append([]string{"forgejo", "extensions", "prepare"}, flags...)
		require.ErrorContains(t, app.Run(t.Context(), args), "--source-catalog, --controller")
	}
}

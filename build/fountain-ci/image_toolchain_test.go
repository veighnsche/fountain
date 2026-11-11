// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package fountainci

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestImageToolchainPinSupportsPOSIXShell(t *testing.T) {
	script := filepath.Join(releaseInputSourceRoot(t), "tools", "fountain-toolchain.sh")
	for _, test := range []struct {
		name, sdk, want string
	}{
		{"matching root and SDK", "toolchain go1.27.3\n", "go1.27.3\n"},
		{"SDK mismatch", "toolchain go1.27.2\n", ""},
		{"missing SDK pin", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "source with spaces")
			releaseInputWrite(t, filepath.Join(root, "go.mod"), "module fixture\ngo 1.27.0\ntoolchain go1.27.3 // exact build compiler\n")
			releaseInputWrite(t, filepath.Join(root, "sdk", "go.mod"), "module fixture/sdk\ngo 1.27.0\n"+test.sdk)
			cmd := exec.CommandContext(t.Context(), "sh", "-euc", `. "$1"; fountain_go_toolchain "$2"`, "image-toolchain-fixture", script, root)
			cmd.Env = append(os.Environ(), "ENV=", "BASH_ENV=")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (test.want != "") || string(output) != test.want {
				t.Fatalf("POSIX shell toolchain = %q, %v; want %q", output, err, test.want)
			}
		})
	}
}

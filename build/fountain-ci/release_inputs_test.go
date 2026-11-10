// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package fountainci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func releaseInputSourceRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func releaseInputWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseArtifactNamesRemainCompatible(t *testing.T) {
	script := filepath.Join(releaseInputSourceRoot(t), "tools", "fountain-release-names.sh")
	cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-euc", `
    source "$1"
    fountain_release_names 15.0.10-fountain.3
    printf '%s\n' "$fountain_release_version" "$fountain_full_version" "$fountain_source_root" "$fountain_source_archive" "$fountain_prepared_root" "$fountain_prepared_archive" "$fountain_platform" "$fountain_binary" "$fountain_binary_archive"
    for value in '' v15.0.10-fountain.3 15.0.10 15.0.10-fountain.0 '15.0.10-fountain.3/escape'; do
      if fountain_release_names "$value"; then exit 99; fi
    done
  `, "names-fixture", script)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "BASH_ENV=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release names: %v: %s", err, output)
	}
	want := "15.0.10-fountain.3\n15.0.10-fountain.3+gitea-1.22.0\nfountain-15.0.10-fountain.3\nfountain-15.0.10-fountain.3-source.tar.gz\nforgejo-src-15.0.10-fountain.3+gitea-1.22.0\nforgejo-src-15.0.10-fountain.3+gitea-1.22.0.tar.gz\nlinux/amd64\nfountain-15.0.10-fountain.3-linux-amd64\nfountain-15.0.10-fountain.3-linux-amd64.gz\n"
	if string(output) != want {
		t.Fatalf("published names changed: %s", output)
	}
}

func TestReleaseToolchainUsesRootAndSDKDirectives(t *testing.T) {
	script := filepath.Join(releaseInputSourceRoot(t), "tools", "fountain-toolchain.sh")
	for _, test := range []struct {
		name, root, sdk string
		want            string
	}{
		{"matching selected version", "toolchain go1.26.10\n", "toolchain go1.26.10\n", "go1.26.10\n"},
		{"new selected version and comments", " toolchain\tgo1.27.3 // selected compiler\n", "toolchain go1.27.3\n", "go1.27.3\n"},
		{"SDK mismatch", "toolchain go1.26.10\n", "toolchain go1.26.9\n", ""},
		{"missing root pin", "", "toolchain go1.26.10\n", ""},
		{"missing SDK pin", "toolchain go1.26.10\n", "", ""},
		{"duplicate directive", "toolchain go1.26.10\ntoolchain go1.26.10\n", "toolchain go1.26.10\n", ""},
		{"invalid directive", "toolchain default\n", "toolchain default\n", ""},
		{"extra directive fields", "toolchain go1.26.10 extra\n", "toolchain go1.26.10\n", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			releaseInputWrite(t, filepath.Join(root, "go.mod"), "module fixture\ngo 1.26.0\n"+test.root)
			releaseInputWrite(t, filepath.Join(root, "sdk", "go.mod"), "module fixture/sdk\ngo 1.26.0\n"+test.sdk)
			releaseInputWrite(t, filepath.Join(root, "third_party", "go-chi-session", "go.mod"), "module session\ngo 1.25.0\ntoolchain go1.25.14\n")
			cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-euc", `source "$1"; fountain_go_toolchain "$2"`, "toolchain-fixture", script, root)
			cmd.Env = append(os.Environ(), "BASH_ENV=")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (test.want != "") || string(output) != test.want {
				t.Fatalf("toolchain = %q, %v; want %q", output, err, test.want)
			}
		})
	}
}

func TestReleaseMetadataUsesCompiledPreparedSDKAndPreservesProvenance(t *testing.T) {
	repository := releaseInputSourceRoot(t)
	toolchainScript := filepath.Join(repository, "tools", "fountain-toolchain.sh")
	pinCommand := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-euc", `source "$1"; fountain_go_toolchain "$2"`, "pin-fixture", toolchainScript, repository)
	pinCommand.Env = append(os.Environ(), "BASH_ENV=")
	pin, err := pinCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	commandSource, err := os.ReadFile(filepath.Join(repository, "sdk", "cmd", "fountain-metadata", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, wrongCommit := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign-source-commit=%t", wrongCommit), func(t *testing.T) {
			fixture := t.TempDir()
			out := filepath.Join(fixture, "release")
			source := filepath.Join(out, "source")
			files := map[string]string{
				"go.mod":     "module fixture\ngo 1.26.0\ntoolchain " + strings.TrimSpace(string(pin)) + "\n",
				"sdk/go.mod": "module forgejo.org/extension-sdk\ngo 1.26.0\ntoolchain " + strings.TrimSpace(string(pin)) + "\n",
				// Typed grouped declarations have no compatible source-text scrape.
				"sdk/versions.go":                   "package extensions\nconst (\n Protocol int = 3\n BrowserContractVersion int = 7\n)\n",
				"sdk/cmd/fountain-metadata/main.go": string(commandSource),
			}
			for name, body := range files {
				releaseInputWrite(t, filepath.Join(source, name), body)
			}
			git := func(args ...string) []byte {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Dir = source
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, output)
				}
				return output
			}
			git("init", "-q")
			git("add", ".")
			git("-c", "user.name=release-input-fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "Exact prepared SDK fixture")
			commit := strings.TrimSpace(string(git("rev-parse", "HEAD")))
			const version = "15.0.10-fountain.3"
			const sourceName = "fountain-15.0.10-fountain.3-source.tar.gz"
			const preparedRoot = "forgejo-src-15.0.10-fountain.3+gitea-1.22.0"
			const preparedName = preparedRoot + ".tar.gz"
			var archive bytes.Buffer
			compressed := gzip.NewWriter(&archive)
			if _, err := compressed.Write(git("archive", "--format=tar", "--prefix=fountain-"+version+"/", commit)); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, sourceName), archive.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			archive.Reset()
			compressed = gzip.NewWriter(&archive)
			prepared := tar.NewWriter(compressed)
			preparedFiles := map[string]string{"VERSION": version + "+gitea-1.22.0\n", "vendor/modules.txt": "# fixture vendor\n"}
			for name, body := range files {
				preparedFiles[name] = body
			}
			for name, body := range preparedFiles {
				if err := prepared.WriteHeader(&tar.Header{Name: preparedRoot + "/" + name, Mode: 0o600, Size: int64(len(body))}); err != nil {
					t.Fatal(err)
				}
				if _, err := prepared.Write([]byte(body)); err != nil {
					t.Fatal(err)
				}
			}
			if err := prepared.Close(); err != nil {
				t.Fatal(err)
			}
			if err := compressed.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, preparedName), archive.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			const config = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
			const digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
			releaseInputWrite(t, filepath.Join(out, "image-id"), config+"\n")
			releaseInputWrite(t, filepath.Join(out, "LICENSE"), "fixture license\n")
			releaseInputWrite(t, filepath.Join(out, "fountain-"+version+"-linux-amd64.gz"), "fixture binary\n")
			var sums strings.Builder
			for _, name := range []string{sourceName, preparedName, "LICENSE", "fountain-" + version + "-linux-amd64.gz"} {
				body, err := os.ReadFile(filepath.Join(out, name))
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(body), name)
			}
			releaseInputWrite(t, filepath.Join(out, "SHA256SUMS"), sums.String())
			bin := filepath.Join(fixture, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(runtime.GOROOT(), "bin", "go"), filepath.Join(bin, "go")); err != nil {
				t.Fatal(err)
			}
			caller := filepath.Join(fixture, "unrelated-caller")
			releaseInputWrite(t, filepath.Join(caller, "sdk", "manifest.go"), "package extensions\nconst Protocol = 99\n")
			requested := commit
			if wrongCommit {
				requested = strings.Repeat("a", 40)
			}
			cmd := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", filepath.Join(repository, "tools", "fountain-release-metadata.sh"), out, version, requested, "ghcr.io/fixture/fountain", digest)
			cmd.Dir = caller
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GOPROXY=off", "BASH_ENV=")
			output, err := cmd.CombinedOutput()
			if wrongCommit {
				if err == nil {
					t.Fatalf("metadata accepted another source commit: %s", output)
				}
				if _, err := os.Stat(filepath.Join(out, "release.json")); !os.IsNotExist(err) {
					t.Fatal("foreign source commit produced release metadata")
				}
				return
			}
			if err != nil {
				t.Fatalf("metadata: %v: %s", err, output)
			}
			body, err := os.ReadFile(filepath.Join(out, "release.json"))
			if err != nil {
				t.Fatal(err)
			}
			var metadata map[string]any
			if err := json.Unmarshal(body, &metadata); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"schema": float64(1), "version": version, "source_commit": commit, "source_archive": sourceName, "prepared_source": preparedName,
				"image": "ghcr.io/fixture/fountain@" + digest, "image_config_digest": config, "platform": "linux/amd64",
				"extension_protocol": float64(3), "browser_contract": float64(7),
			}
			for key, value := range want {
				if metadata[key] != value {
					t.Errorf("metadata[%s] = %v, want %v", key, metadata[key], value)
				}
			}
			for key, name := range map[string]string{"source_sha256": sourceName, "prepared_source_sha256": preparedName} {
				body, err := os.ReadFile(filepath.Join(out, name))
				if err != nil {
					t.Fatal(err)
				}
				if metadata[key] != fmt.Sprintf("%x", sha256.Sum256(body)) {
					t.Errorf("metadata[%s] does not identify its exact artifact", key)
				}
			}
			if len(metadata) != len(want)+2 {
				t.Errorf("release metadata schema changed: %s", body)
			}
		})
	}
}

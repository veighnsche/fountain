// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Host-owned filesystem recovery evidence.
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/extension-sdk/wirejson"
)

type packageOrigin struct {
	Kind       string   `json:"kind"`
	PackageID  string   `json:"package_id"`
	Version    string   `json:"version"`
	TreeSHA256 string   `json:"tree_sha256"`
	Receipt    *Receipt `json:"receipt"`
}

const (
	prebuiltOrigin           = "prebuilt"
	catalogOrigin            = "catalog"
	maximumPackageStateBytes = 4 << 10
)

type packageReplacement struct {
	Previous packageOrigin `json:"previous"`
	Next     packageOrigin `json:"next"`
	Target   string        `json:"target"`
	Backup   string        `json:"backup"`
}

// packageState is host-created origin evidence, outside package contents. A
// replacement is durable before either fixed-path rename can take place.
type packageState struct {
	Schema      int                 `json:"schema"`
	PackageID   string              `json:"package_id"`
	Current     packageOrigin       `json:"current"`
	Replacement *packageReplacement `json:"replacement"`
}

func packageStatePath(root, id string) string {
	return filepath.Join(root, ".package-state", id+".json")
}

// ValidateKnownPackageOrigin checks host-recorded package bytes before execution.
// Historical current packages without an origin record retain their existing
// runtime admission; a pending replacement must be recovered by the store first.
func ValidateKnownPackageOrigin(root, id string) error {
	if !sdk.ValidID(id) {
		return errRecoveryUnavailable
	}
	state, err := loadPackageState(root, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || state.Replacement != nil {
		return errRecoveryUnavailable
	}
	return state.Current.verify(filepath.Join(root, id))
}

func packageTreeDigest(directory string) (string, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	sum := sha256.New()
	var total int64
	files := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || name == "." {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("package contains a non-regular file")
		}
		if name == receiptFileName {
			// The installer adds this bounded evidence after archive limits
			// have been checked. It still participates in the tree digest.
			if info.Size() > maximumIntentBytes {
				return errors.New("extension package receipt is invalid")
			}
		} else {
			files++
			total += info.Size()
			if files > 10000 || total > sdk.MaximumArchiveBytes {
				return errors.New("package exceeds 10000 files or 512 MiB")
			}
		}
		_, _ = fmt.Fprintf(sum, "%d:%s:%o:%d:", len(name), name, info.Mode().Perm(), info.Size())
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		n, readErr := io.Copy(sum, io.LimitReader(file, info.Size()+1))
		closeErr := file.Close()
		if n != info.Size() {
			return errors.New("package changed while reading origin")
		}
		return errors.Join(readErr, closeErr)
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func inspectPackageOrigin(directory, kind string) (packageOrigin, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return packageOrigin{}, errRecoveryUnavailable
	}
	manifest, err := sdk.LoadManifest(directory)
	if err != nil {
		return packageOrigin{}, err
	}
	if err := checkPackageFiles(directory, manifest); err != nil {
		return packageOrigin{}, err
	}
	origin := packageOrigin{Kind: kind, PackageID: manifest.ID, Version: manifest.Version}
	switch kind {
	case catalogOrigin:
		receipt, err := LoadReceipt(directory)
		if err != nil {
			return packageOrigin{}, err
		}
		if receipt.PackageID != manifest.ID || receipt.PackageVersion != manifest.Version || !packageSHA256(receipt.ArchiveSHA256) {
			return packageOrigin{}, errRecoveryUnavailable
		}
		origin.Receipt = &receipt
	case prebuiltOrigin:
		if _, err := os.Lstat(filepath.Join(directory, receiptFileName)); !errors.Is(err, os.ErrNotExist) {
			return packageOrigin{}, errRecoveryUnavailable
		}
	default:
		return packageOrigin{}, errRecoveryUnavailable
	}
	origin.TreeSHA256, err = packageTreeDigest(directory)
	return origin, err
}

func packageSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == hex.EncodeToString(decoded)
}

func (origin packageOrigin) valid(id string) bool {
	if origin.PackageID != id || origin.Version == "" || !packageSHA256(origin.TreeSHA256) {
		return false
	}
	switch origin.Kind {
	case prebuiltOrigin:
		return origin.Receipt == nil
	case catalogOrigin:
		return origin.Receipt != nil && origin.Receipt.Schema == 1 && origin.Receipt.PackageID == id && origin.Receipt.PackageVersion == origin.Version && packageSHA256(origin.Receipt.ArchiveSHA256)
	}
	return false
}

func (origin packageOrigin) same(other packageOrigin) bool {
	if origin.Kind != other.Kind || origin.PackageID != other.PackageID || origin.Version != other.Version || origin.TreeSHA256 != other.TreeSHA256 {
		return false
	}
	if origin.Receipt == nil || other.Receipt == nil {
		return origin.Receipt == nil && other.Receipt == nil
	}
	return *origin.Receipt == *other.Receipt
}

func (origin packageOrigin) verify(directory string) error {
	actual, err := inspectPackageOrigin(directory, origin.Kind)
	if err != nil || !origin.same(actual) {
		return errRecoveryUnavailable
	}
	return nil
}

func loadPackageState(root, id string) (packageState, error) {
	path := packageStatePath(root, id)
	info, err := os.Lstat(path)
	if err != nil {
		return packageState{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return packageState{}, errRecoveryUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return packageState{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return packageState{}, errRecoveryUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximumPackageStateBytes+1))
	if err != nil || len(raw) > maximumPackageStateBytes {
		return packageState{}, errRecoveryUnavailable
	}
	if _, err := wirejson.ObjectFields(raw, "package origin"); err != nil {
		return packageState{}, errRecoveryUnavailable
	}
	var state packageState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF || state.Schema != 1 || state.PackageID != id || !state.Current.valid(id) {
		return packageState{}, errRecoveryUnavailable
	}
	if replacement := state.Replacement; replacement != nil {
		if !replacement.Previous.valid(id) || !replacement.Next.valid(id) || !replacement.Previous.same(state.Current) || replacement.Target != id || replacement.Backup != ".previous-"+id {
			return packageState{}, errRecoveryUnavailable
		}
	}
	return state, nil
}

func writePackageState(root string, state packageState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := packageStatePath(root, state.PackageID)
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".origin-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	if err := fsyncDirectory(directory); err != nil {
		return err
	}
	return fsyncDirectory(root)
}

func provenPackageOrigin(root, id, directory string) (packageOrigin, error) {
	state, err := loadPackageState(root, id)
	if err == nil {
		if state.Replacement != nil || state.Current.verify(directory) != nil {
			return packageOrigin{}, errRecoveryUnavailable
		}
		return state.Current, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return packageOrigin{}, err
	}
	// Existing schema-1 catalog receipts are positive origin evidence. A
	// receiptless legacy directory cannot prove that it was prebuilt.
	origin, err := inspectPackageOrigin(directory, catalogOrigin)
	if err != nil || origin.PackageID != id {
		return packageOrigin{}, errRecoveryUnavailable
	}
	return origin, nil
}

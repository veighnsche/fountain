// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package git

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"forgejo.org/modules/nativeoperation"
)

var (
	ErrRefLockRetirementEvidence = errors.New("stopped reference lock evidence differs")
	ErrRefLockRetirementBackend  = errors.New("reference lock backend or format is unsupported")
)

// RefLockRetirementProof carries the stopped-domain outcome already established
// by the operation owner. This adapter checks storage identity; it cannot
// establish journal authority or authorize a live writer to unlock Git.
type RefLockRetirementProof struct {
	StoppedDomain  bool
	InstallationID string
	OperationID    string
	Owner          string
	Generation     int64
	Ref            string
	NewOID         string
	ObjectFormat   string
}

func (proof RefLockRetirementProof) valid() bool {
	format := ObjectFormatFromName(proof.ObjectFormat)
	return proof.StoppedDomain && proof.InstallationID != "" && proof.OperationID != "" &&
		proof.Owner == nativeoperation.ConditionalOwner(proof.InstallationID, proof.OperationID) && proof.Generation > 0 &&
		strings.HasPrefix(proof.Ref, BranchPrefix) && IsValidRefPattern(proof.Ref) &&
		!strings.Contains(proof.Ref, "*") && format != nil && (proof.NewOID == "" ||
		len(proof.NewOID) == format.FullLength() && format.IsValid(proof.NewOID) && !IsEmptyCommitID(proof.NewOID, format))
}

// filesRefStorage checks the actual repository using commands supported by
// Git 2.34. An absent extensions.refStorage means the original files backend;
// unknown extensions, linked worktrees and unsupported object formats refuse.
func filesRefStorage(ctx context.Context, repository, objectFormat string) error {
	opts := &RunOpts{Dir: repository}
	bare, _, err := NewCommand(ctx, "rev-parse", "--is-bare-repository").RunStdString(opts)
	if err != nil || strings.TrimSpace(bare) != "true" {
		return ErrRefLockRetirementBackend
	}
	gitDir, _, err := NewCommand(ctx, "rev-parse", "--absolute-git-dir").RunStdString(opts)
	if err != nil || filepath.Clean(strings.TrimSpace(gitDir)) != repository {
		return ErrRefLockRetirementBackend
	}
	common, _, err := NewCommand(ctx, "rev-parse", "--git-common-dir").RunStdString(opts)
	if err != nil {
		return ErrRefLockRetirementBackend
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(repository, common)
	}
	if filepath.Clean(common) != repository {
		return ErrRefLockRetirementBackend
	}
	backend, _, err := NewCommand(ctx, "config", "--local", "--get-all", "extensions.refStorage").RunStdString(opts)
	if err != nil && !IsErrorExitCode(err, 1) {
		return ErrRefLockRetirementBackend
	}
	backend = strings.TrimSpace(backend)
	if backend != "" && backend != "files" {
		return ErrRefLockRetirementBackend
	}
	format, _, err := NewCommand(ctx, "rev-parse", "--show-object-format").RunStdString(opts)
	if err != nil || strings.TrimSpace(format) != objectFormat {
		return ErrRefLockRetirementBackend
	}
	return nil
}

// RetireStoppedRefLock removes only one attributable files-backend lock with
// the exact admitted OID and inode. No PID, age or guessed backend path is proof.
func RetireStoppedRefLock(ctx context.Context, repository string, proof RefLockRetirementProof) error {
	if !proof.valid() {
		return ErrRefLockRetirementEvidence
	}
	repository, err := filepath.Abs(repository)
	if err != nil {
		return err
	}
	info, err := os.Lstat(repository)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrRefLockRetirementEvidence
	}
	if err := filesRefStorage(ctx, repository, proof.ObjectFormat); err != nil {
		return err
	}
	root, err := os.OpenRoot(repository)
	if err != nil {
		return err
	}
	defer root.Close()
	parts := strings.Split(proof.Ref, "/")
	parent := ""
	for _, part := range parts[:len(parts)-1] {
		parent = filepath.Join(parent, part)
		entry, err := root.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || !entry.IsDir() || entry.Mode()&os.ModeSymlink != 0 {
			return ErrRefLockRetirementEvidence
		}
	}
	path := filepath.FromSlash(proof.Ref) + ".lock"
	info, err = root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || proof.NewOID == "" {
		return ErrRefLockRetirementEvidence
	}
	file, err := root.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return ErrRefLockRetirementEvidence
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(len(proof.NewOID)+2)))
	if err != nil || string(data) != proof.NewOID+"\n" {
		return ErrRefLockRetirementEvidence
	}
	current, err := root.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return ErrRefLockRetirementEvidence
	}
	if err := root.Remove(path); err != nil {
		return err
	}
	directory, err := root.Open(parent)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

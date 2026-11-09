// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
)

const (
	maxSourcePaths     = extension.MaxRepositorySourcePaths
	maxSourceFileBytes = extension.MaxRepositorySourceFile
	maxSourceTotal     = extension.MaxRepositorySourceTotal
	maxSourceBundle    = extension.MaxRepositorySourceBundle
)

// ReadRepositorySource reads exact bounded native Git objects. Its caller must
// check current account/repository read authority before and after I/O. Selected
// writers are observations only; they grant no execution permission.
func ReadRepositorySource(ctx context.Context, request extension.CallbackRequest) (*extension.RepositorySource, int) {
	if extension.ValidateRepositorySourceRequest(request) != nil {
		return nil, http.StatusBadRequest
	}
	repository, err := repo_model.GetRepositoryByID(ctx, sourceID(request.RepositoryID))
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	native, err := git.OpenRepository(ctx, repository.RepoPath())
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	defer native.Close()
	tip, err := native.GetBranchCommit(repository.DefaultBranch)
	if err != nil {
		return nil, http.StatusNotFound
	}
	commit := tip
	if request.SourceCommit != "" {
		commit, err = native.GetCommit(request.SourceCommit)
		if err != nil || commit.ID.String() != request.SourceCommit {
			return nil, http.StatusNotFound
		}
		if commit.ID.String() != tip.ID.String() {
			reachable, err := tip.HasPreviousCommit(commit.ID)
			if err != nil {
				return nil, http.StatusServiceUnavailable
			}
			if !reachable {
				return nil, http.StatusNotFound
			}
		}
	}
	result := &extension.RepositorySource{
		RepositoryID: request.RepositoryID, Commit: commit.ID.String(),
		Files:   make(map[string][]byte, len(request.SourcePaths)),
		Writers: make(map[string]bool, len(request.SourceActors)),
	}
	total := 0
	for _, name := range request.SourcePaths {
		entry, err := commit.GetTreeEntryByPath(name)
		if err != nil || (!entry.IsRegular() && !entry.IsExecutable()) {
			return nil, http.StatusNotFound
		}
		blob := entry.Blob()
		if blob.Size() > maxSourceFileBytes {
			return nil, http.StatusRequestEntityTooLarge
		}
		reader, err := blob.DataAsync()
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxSourceFileBytes+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			return nil, http.StatusServiceUnavailable
		}
		total += len(data)
		if len(data) > maxSourceFileBytes || total > maxSourceTotal {
			return nil, http.StatusRequestEntityTooLarge
		}
		result.Files[name] = data
	}
	bundle := sourceBundleWriter{}
	if err := native.CreateBundle(ctx, result.Commit, &bundle); err != nil {
		if bundle.exceeded {
			return nil, http.StatusRequestEntityTooLarge
		}
		return nil, http.StatusServiceUnavailable
	}
	result.Bundle = bundle.Bytes()
	for _, actor := range request.SourceActors {
		user, err := user_model.GetUserByID(ctx, sourceID(actor))
		if user_model.IsErrUserNotExist(err) || err == nil && !user.IsAccessAllowed(ctx) {
			result.Writers[actor] = false
			continue
		}
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		permission, err := access_model.GetUserRepoPermission(ctx, repository, user)
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		result.Writers[actor] = permission.CanWrite(unit.TypeCode)
	}
	return result, http.StatusOK
}

type sourceBundleWriter struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (writer *sourceBundleWriter) Write(data []byte) (int, error) {
	if len(data) > maxSourceBundle-writer.buffer.Len() {
		writer.exceeded = true
		return 0, io.ErrShortWrite
	}
	return writer.buffer.Write(data)
}

func (writer *sourceBundleWriter) Bytes() []byte { return writer.buffer.Bytes() }

func sourceID(value string) int64 {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
		return 0
	}
	return id
}

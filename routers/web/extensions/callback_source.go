// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"net/http"

	extension "forgejo.org/extension-sdk"
	repository_service "forgejo.org/services/repository"
)

const (
	maxSourcePaths     = extension.MaxRepositorySourcePaths
	maxSourceFileBytes = extension.MaxRepositorySourceFile
)

func resolveRepositorySource(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositorySource, int) {
	if _, status := resolveRepository(ctx, admission, request.RepositoryID); status != http.StatusOK {
		return nil, status
	}
	source, status := repository_service.ReadRepositorySource(ctx, request)
	if status != http.StatusOK {
		return nil, status
	}
	if _, status := resolveRepository(ctx, admission, request.RepositoryID); status != http.StatusOK {
		return nil, status
	}
	return source, http.StatusOK
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repository

import (
	"context"
	"fmt"

	repo_model "forgejo.org/models/repo"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

// SetArchiveState owns one archive-state change shared by web and API callers.
func SetArchiveState(ctx context.Context, repo *repo_model.Repository, isArchived bool) error {
	return nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
		nativeoperation_service.FamilyRepoSettings, fmt.Sprintf("%d/archive", repo.ID),
		nativeoperation_service.Scope{Family: nativeoperation_service.FamilyRepoSettings, RepositoryID: repo.ID},
		func(owned context.Context) error { return repo_model.SetArchiveRepoState(owned, repo, isArchived) })
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
)

func resolveIssueCreateSnapshot(ctx context.Context, admission *callbackAdmission, repository string) (*extension.RepositoryIssueCreateSnapshot, int) {
	first, revision, status := observeCallbackSnapshot(ctx, func(ctx context.Context) (*extension.RepositoryIssueCreateSnapshot, int) {
		return readIssueCreateSnapshot(ctx, admission, repository)
	}, equalSnapshot[extension.RepositoryIssueCreateSnapshot], nil, []int{http.StatusNotFound})
	if status != http.StatusOK {
		return nil, status
	}
	first.NativeRevision = revision
	return first, http.StatusOK
}

func readIssueCreateSnapshot(ctx context.Context, admission *callbackAdmission, repository string) (*extension.RepositoryIssueCreateSnapshot, int) {
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	repo, err := repo_model.GetRepositoryByID(ctx, mustParsePositiveID(repository))
	if repo_model.IsErrRepoNotExist(err) {
		return nil, http.StatusNotFound
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, user)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	// Match ordinary native issue creation: a reader may create an issue.
	// The native issue service independently refuses blocks by the repo owner.
	if unit.TypeIssues.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, unit.TypeIssues) || !permission.CanRead(unit.TypeIssues) {
		return nil, http.StatusNotFound
	}
	action := capabilityFact("allowed", "issue_create")
	if user_model.IsBlocked(ctx, repo.OwnerID, user.ID) {
		action = capabilityFact("denied", "issue_creator_blocked")
	}
	if repo.IsArchived {
		action = capabilityFact("denied", "repository_archived")
	}
	return &extension.RepositoryIssueCreateSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), Action: action}, http.StatusOK
}

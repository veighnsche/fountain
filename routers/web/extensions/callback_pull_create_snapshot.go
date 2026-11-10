// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
)

func readPullCreateSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryPullCreateSnapshotRequest) (*extension.RepositoryPullCreateSnapshot, int) {
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	base, err := repo_model.GetRepositoryByID(ctx, mustParsePositiveID(repository))
	if repo_model.IsErrRepoNotExist(err) {
		return nil, http.StatusNotFound
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	head, err := repo_model.GetRepositoryByID(ctx, mustParsePositiveID(q.HeadRepositoryID))
	if repo_model.IsErrRepoNotExist(err) {
		return nil, http.StatusNotFound
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	basePermission, err := access_model.GetUserRepoPermission(ctx, base, user)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	// Match native explicit-repository comparison: reading base Code/Pulls and
	// head Code is sufficient. A writer role or owned fork is not required.
	if !base.AllowsPulls(ctx) {
		return nil, http.StatusNotFound
	}
	for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
		if kind.UnitGlobalDisabled() || !base.UnitEnabled(ctx, kind) || !basePermission.CanRead(kind) {
			return nil, http.StatusNotFound
		}
	}
	headPermission, err := access_model.GetUserRepoPermission(ctx, head, user)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if !head.UnitEnabled(ctx, unit.TypeCode) || !headPermission.CanRead(unit.TypeCode) {
		return nil, http.StatusNotFound
	}
	if base.ObjectFormatName != head.ObjectFormatName {
		return nil, http.StatusUnprocessableEntity
	}
	baseGit, err := git.OpenRepository(ctx, base.RepoPath())
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	defer baseGit.Close()
	headGit, err := git.OpenRepository(ctx, head.RepoPath())
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	defer headGit.Close()
	baseOID := capabilityCommit(baseGit, git.BranchPrefix+q.BaseBranch)
	headOID := capabilityCommit(headGit, git.BranchPrefix+q.HeadBranch)
	if baseOID == "" || headOID == "" {
		return nil, http.StatusNotFound
	}
	action := capabilityFact("allowed", "native_pull_create_reads_passed")
	if base.ID == head.ID && q.BaseBranch == q.HeadBranch {
		action = capabilityFact("denied", "same_pull_source_and_target")
	}
	_, err = issues_model.GetUnmergedPullRequest(ctx, head.ID, base.ID, q.HeadBranch, q.BaseBranch, issues_model.PullRequestFlowGithub)
	if err == nil {
		action = capabilityFact("denied", "pull_already_exists")
	} else if !issues_model.IsErrPullRequestNotExist(err) {
		return nil, http.StatusServiceUnavailable
	}
	if user_model.IsBlocked(ctx, base.OwnerID, user.ID) {
		action = capabilityFact("denied", "pull_creator_blocked")
	}
	if base.IsArchived {
		action = capabilityFact("denied", "repository_archived")
	}
	if _, status := resolveCurrentUser(ctx, admission); status != http.StatusOK {
		return nil, status
	}
	if capabilityCommit(baseGit, git.BranchPrefix+q.BaseBranch) != baseOID || capabilityCommit(headGit, git.BranchPrefix+q.HeadBranch) != headOID {
		return nil, http.StatusConflict
	}
	return &extension.RepositoryPullCreateSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), HeadRepositoryID: q.HeadRepositoryID, BaseBranch: q.BaseBranch, HeadBranch: q.HeadBranch, BaseOID: baseOID, HeadOID: headOID, Action: action}, http.StatusOK
}

func resolvePullCreateSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryPullCreateSnapshot, int) {
	first, revision, status := observeCallbackSnapshot(ctx, func(ctx context.Context) (*extension.RepositoryPullCreateSnapshot, int) {
		return readPullCreateSnapshot(ctx, admission, request.RepositoryID, *request.PullCreateSnapshot)
	}, equalSnapshot[extension.RepositoryPullCreateSnapshot], func(ctx context.Context) int { _, status := resolveCurrentUser(ctx, admission); return status }, []int{http.StatusNotFound, http.StatusUnprocessableEntity})
	if status != http.StatusOK {
		return nil, status
	}
	first.NativeRevision = revision
	return first, http.StatusOK
}

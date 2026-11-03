// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/git"
	nativeoperation_service "forgejo.org/services/nativeoperation"
	pull_service "forgejo.org/services/pull"
)

func readManualMergeSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryManualMergeSnapshotRequest) (*extension.RepositoryManualMergeSnapshot, int) {
	pr, status := pullFilesParent(ctx, admission, repository, q.Index)
	if status != http.StatusOK {
		return nil, status
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	repo, err := repo_model.GetRepositoryByID(ctx, pr.BaseRepoID)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, user)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
		if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
			return nil, http.StatusNotFound
		}
	}
	if !repo.AllowsPulls(ctx) {
		return nil, http.StatusNotFound
	}
	native, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	defer native.Close()
	base := capabilityCommit(native, git.BranchPrefix+pr.BaseBranch)
	if base == "" {
		return nil, http.StatusServiceUnavailable
	}
	commit, err := pull_service.ReadManualMergeCommit(native, repo.ObjectFormatName, pr.BaseBranch, q.CommitOID)
	if errors.Is(err, pull_service.ErrManualMergeCommit) || err == nil && commit.ID.String() != q.CommitOID {
		return nil, http.StatusUnprocessableEntity
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	pr.BaseRepo, pr.Issue.Repo = repo, repo
	prUnit, err := repo.GetUnit(ctx, unit.TypePullRequests)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	action := capabilityFact("allowed", "native_manual_merge_checks_passed")
	if !prUnit.PullRequestsConfig().IsMergeStyleAllowed(repo_model.MergeStyleManuallyMerged) {
		action = capabilityFact("unsupported", "manual_merge_method_disabled")
	} else {
		err := pull_service.CheckPullMergeable(ctx, user, &permission, pr, pull_service.MergeCheckTypeManually, false)
		switch {
		case errors.Is(err, pull_service.ErrHasMerged):
			action = capabilityFact("denied", "pull_merged")
		case errors.Is(err, pull_service.ErrIsClosed):
			action = capabilityFact("denied", "pull_closed")
		case errors.Is(err, pull_service.ErrUserNotAllowedToMerge):
			action = capabilityFact("denied", "merge_permission_denied")
		case err != nil:
			action = capabilityFact("unavailable", "manual_merge_checks_unavailable")
		}
	}
	if repo.IsArchived {
		action = capabilityFact("denied", "repository_archived")
	}
	if _, status := resolveCurrentUser(ctx, admission); status != http.StatusOK {
		return nil, status
	}
	if capabilityCommit(native, git.BranchPrefix+pr.BaseBranch) != base {
		return nil, http.StatusConflict
	}
	return &extension.RepositoryManualMergeSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), PullID: strconv.FormatInt(pr.ID, 10), IssueID: strconv.FormatInt(pr.IssueID, 10), Index: q.Index, BaseBranch: pr.BaseBranch, BaseOID: base, CommitOID: commit.ID.String(), IsClosed: pr.Issue.IsClosed, HasMerged: pr.HasMerged, Action: action}, http.StatusOK
}

func resolveManualMergeSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryManualMergeSnapshot, int) {
	before, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil || !before.Idle {
		return nil, http.StatusServiceUnavailable
	}
	first, status := readManualMergeSnapshot(ctx, admission, request.RepositoryID, *request.ManualMergeSnapshot)
	if status != http.StatusOK {
		return nil, status
	}
	second, status := readManualMergeSnapshot(ctx, admission, request.RepositoryID, *request.ManualMergeSnapshot)
	if status == http.StatusNotFound || status == http.StatusUnprocessableEntity {
		return nil, http.StatusConflict
	}
	if status != http.StatusOK {
		return nil, status
	}
	if _, status := resolveCurrentUser(ctx, admission); status != http.StatusOK {
		return nil, status
	}
	after, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if !after.Idle || before.Revision != after.Revision || *first != *second {
		return nil, http.StatusConflict
	}
	first.NativeRevision = before.Revision
	return first, http.StatusOK
}

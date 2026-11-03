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
	"forgejo.org/modules/git"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

type mergeSnapshotFacts struct {
	Snapshot                                     extension.RepositoryMergeSnapshot
	HeadRepositoryID, PosterID, OriginalAuthorID int64
	HeadBranch                                   string
}

func readMergeSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryMergeSnapshotRequest) (*mergeSnapshotFacts, int) {
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
	head, base := capabilityCommit(native, pr.GetGitRefName()), capabilityCommit(native, git.BranchPrefix+pr.BaseBranch)
	if head == "" || base == "" {
		return nil, http.StatusServiceUnavailable
	}
	mergeBase, err := native.GetMergeBaseSimple(base, head)
	if err != nil || !extension.ValidPullDiffCommit(mergeBase) {
		return nil, http.StatusServiceUnavailable
	}
	pr.BaseRepo, pr.Issue.Repo = repo, repo
	action := normalMergeCapability(ctx, repo, user, permission, pr, head, base)
	prUnit, err := repo.GetUnit(ctx, unit.TypePullRequests)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if !prUnit.PullRequestsConfig().IsMergeStyleAllowed(repo_model.MergeStyle(q.Method)) {
		action = capabilityFact("unsupported", "merge_method_disabled")
	}
	if action.State == "allowed" && q.Method == string(repo_model.MergeStyleFastForwardOnly) && mergeBase != base {
		action = capabilityFact("denied", "not_fast_forward")
	}
	if repo.IsArchived {
		action = capabilityFact("denied", "repository_archived")
	}
	if capabilityCommit(native, pr.GetGitRefName()) != head || capabilityCommit(native, git.BranchPrefix+pr.BaseBranch) != base {
		return nil, http.StatusConflict
	}
	p := extension.RepositoryMergeSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), PullID: strconv.FormatInt(pr.ID, 10), IssueID: strconv.FormatInt(pr.IssueID, 10), Index: q.Index, Method: q.Method, BaseBranch: pr.BaseBranch, HeadOID: head, BaseOID: base, MergeBaseOID: mergeBase, IsClosed: pr.Issue.IsClosed, HasMerged: pr.HasMerged, Action: action}
	return &mergeSnapshotFacts{Snapshot: p, HeadRepositoryID: pr.HeadRepoID, HeadBranch: pr.HeadBranch, PosterID: pr.Issue.PosterID, OriginalAuthorID: pr.Issue.OriginalAuthorID}, http.StatusOK
}

func resolveMergeSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryMergeSnapshot, int) {
	before, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil || !before.Idle {
		return nil, http.StatusServiceUnavailable
	}
	first, status := readMergeSnapshot(ctx, admission, request.RepositoryID, *request.MergeSnapshot)
	if status != http.StatusOK {
		return nil, status
	}
	second, status := readMergeSnapshot(ctx, admission, request.RepositoryID, *request.MergeSnapshot)
	if status == http.StatusNotFound {
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
	first.Snapshot.NativeRevision = before.Revision
	return &first.Snapshot, http.StatusOK
}

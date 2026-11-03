// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"strconv"

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	pull_service "forgejo.org/services/pull"
)

var ErrHumanMergeStage = errors.New("human merge preparation no longer matches live native target")

// PrepareHumanMerge computes a private local result under the caller's exact
// held native revision and live human admission. It performs no LFS/ref push,
// metadata completion, journal allocation or notification. The caller MUST Close
// the result. Future prepared-push/journal consumers must re-admit before effects;
// a successful local computation is never a successful merge.
func PrepareHumanMerge(ctx context.Context, actorID int64, want sdk.RepositoryMergeSnapshot, message string, admit HumanReviewAdmission) (*pull_service.PreparedExactMerge, error) {
	q := sdk.RepositoryMergeSnapshotRequest{Index: want.Index, Method: want.Method}
	if actorID <= 0 || strconv.FormatInt(actorID, 10) != want.ActorID || admit == nil || !sdk.ValidRepositoryMergeSnapshot(&want, want.RepositoryID, want.ActorID, q) || want.Action.State != "allowed" || want.IsClosed || want.HasMerged || !sdk.ValidHumanMergeMessage(want.Method, message) {
		return nil, ErrHumanMergeStage
	}
	check := func() (*issues_model.PullRequest, *user_model.User, error) {
		return checkHumanMergeTarget(ctx, actorID, want, admit)
	}
	first, actor, err := check()
	if err != nil {
		return nil, err
	}
	prepared, err := pull_service.PrepareExactMerge(ctx, first, actor, repo_model.MergeStyle(want.Method), want.HeadOID, want.BaseOID, message)
	if err != nil {
		return nil, err
	}
	second, _, err := check()
	if err != nil {
		prepared.Close()
		return nil, err
	}
	if first.HeadRepoID != second.HeadRepoID || first.HeadBranch != second.HeadBranch || first.Issue.PosterID != second.Issue.PosterID || first.Issue.OriginalAuthorID != second.Issue.OriginalAuthorID {
		prepared.Close()
		return nil, ErrHumanMergeStage
	}
	return prepared, nil
}

func checkHumanMergeTarget(ctx context.Context, actorID int64, want sdk.RepositoryMergeSnapshot, admit HumanReviewAdmission) (*issues_model.PullRequest, *user_model.User, error) {
	if err := requireHumanPrimaryOwner(ctx, want.NativeRevision); err != nil {
		return nil, nil, err
	}
	if err := admitHumanPrimary(ctx, admit); err != nil {
		return nil, nil, err
	}
	actor, err := user_model.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, nil, err
	}
	repo, err := repo_model.GetRepositoryByID(ctx, nativeCommentID(want.RepositoryID))
	if err != nil {
		return nil, nil, err
	}
	pr, err := issues_model.GetPullRequestByID(ctx, nativeCommentID(want.PullID))
	if err != nil {
		return nil, nil, err
	}
	if pr.BaseRepoID != repo.ID || pr.Index != nativeCommentID(want.Index) || pr.IssueID != nativeCommentID(want.IssueID) || pr.BaseBranch != want.BaseBranch || pr.HasMerged || repo.IsArchived || !repo.AllowsPulls(ctx) {
		return nil, nil, ErrHumanMergeStage
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return nil, nil, err
	}
	if pr.Issue.IsClosed || !pr.Issue.IsPull || pr.Issue.RepoID != repo.ID || pr.Issue.Index != pr.Index {
		return nil, nil, ErrHumanMergeStage
	}
	pr.BaseRepo, pr.Issue.Repo = repo, repo
	permission, err := access_model.GetUserRepoPermission(ctx, repo, actor)
	if err != nil {
		return nil, nil, err
	}
	for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
		if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
			return nil, nil, ErrHumanMergeStage
		}
	}
	prUnit, err := repo.GetUnit(ctx, unit.TypePullRequests)
	if err != nil {
		return nil, nil, err
	}
	if !prUnit.PullRequestsConfig().IsMergeStyleAllowed(repo_model.MergeStyle(want.Method)) {
		return nil, nil, ErrHumanMergeStage
	}
	if err := pull_service.CheckPullMergeable(ctx, actor, &permission, pr, pull_service.MergeCheckTypeGeneral, false); err != nil {
		return nil, nil, err
	}
	native, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		return nil, nil, err
	}
	defer native.Close()
	head, err := native.GetRefCommitID(pr.GetGitRefName())
	if err != nil || head != want.HeadOID {
		return nil, nil, ErrHumanMergeStage
	}
	base, err := native.GetRefCommitID(git.BranchPrefix + pr.BaseBranch)
	if err != nil || base != want.BaseOID {
		return nil, nil, ErrHumanMergeStage
	}
	mergeBase, err := native.GetMergeBaseSimple(base, head)
	if err != nil || mergeBase != want.MergeBaseOID || want.Method == "fast-forward-only" && mergeBase != base {
		return nil, nil, ErrHumanMergeStage
	}
	if err := pr.LoadHeadRepo(ctx); err != nil || pr.HeadRepo == nil {
		return nil, nil, ErrHumanMergeStage
	}
	source, err := git.OpenRepository(ctx, pr.HeadRepo.RepoPath())
	if err != nil {
		return nil, nil, err
	}
	defer source.Close()
	sourceRef := git.BranchPrefix + pr.HeadBranch
	if pr.Flow == issues_model.PullRequestFlowAGit {
		sourceRef = pr.GetGitRefName()
	}
	sourceHead, err := source.GetRefCommitID(sourceRef)
	if err != nil || sourceHead != head {
		return nil, nil, ErrHumanMergeStage
	}
	return pr, actor, nil
}

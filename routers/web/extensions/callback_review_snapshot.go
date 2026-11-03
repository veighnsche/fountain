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
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

// Compare private source/author facts as well as the public preparation without
// disclosing a private fork's repository ID or branch in the SDK response.
type reviewSnapshotFacts struct {
	Snapshot                                     extension.RepositoryReviewSnapshot
	HeadRepositoryID, PosterID, OriginalAuthorID int64
	HeadBranch                                   string
}

func resolveReviewSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryReviewSnapshot, int) {
	before, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil || !before.Idle {
		return nil, http.StatusServiceUnavailable
	}
	first, status := readReviewSnapshot(ctx, admission, request.RepositoryID, *request.ReviewSnapshot)
	if status != http.StatusOK {
		return nil, status
	}
	second, status := readReviewSnapshot(ctx, admission, request.RepositoryID, *request.ReviewSnapshot)
	if status == http.StatusNotFound {
		return nil, http.StatusConflict
	}
	if status != http.StatusOK {
		return nil, status
	}
	after, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if !after.Idle || after.Revision != before.Revision || *first != *second {
		return nil, http.StatusConflict
	}
	first.Snapshot.NativeRevision = before.Revision
	return &first.Snapshot, http.StatusOK
}

func readReviewSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryReviewSnapshotRequest) (*reviewSnapshotFacts, int) {
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
	if capabilityCommit(native, pr.GetGitRefName()) != head || capabilityCommit(native, git.BranchPrefix+pr.BaseBranch) != base {
		return nil, http.StatusConflict
	}
	issue := pr.Issue
	snapshot := extension.RepositoryReviewSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), PullID: strconv.FormatInt(pr.ID, 10), IssueID: strconv.FormatInt(issue.ID, 10), Index: q.Index, BaseBranch: pr.BaseBranch, HeadOID: head, BaseOID: base, MergeBaseOID: mergeBase, IsClosed: issue.IsClosed, HasMerged: pr.HasMerged, IsAuthor: issue.IsPoster(user.ID),
		CommentAction: capabilityFact("allowed", "review_comment"), DraftAction: capabilityFact("allowed", "review_start_draft"), ApproveAction: capabilityFact("allowed", "review_approve"), RequestChangesAction: capabilityFact("allowed", "review_request_changes")}
	pending, err := issues_model.GetCurrentReview(ctx, user, issue)
	if err != nil && !issues_model.IsErrReviewNotExist(err) {
		return nil, http.StatusServiceUnavailable
	}
	if pending != nil {
		snapshot.PendingReviewID = strconv.FormatInt(pending.ID, 10)
		snapshot.PendingCommitID = pending.CommitID
		snapshot.DraftAction = capabilityFact("allowed", "review_continue_draft")
	}
	if snapshot.IsAuthor {
		snapshot.ApproveAction = capabilityFact("denied", "self_review")
		snapshot.RequestChangesAction = capabilityFact("denied", "self_review")
	}
	if user_model.IsBlockedMultiple(ctx, []int64{repo.OwnerID, issue.PosterID}, user.ID) {
		snapshot.CommentAction = capabilityFact("denied", "review_actor_blocked")
		snapshot.DraftAction = snapshot.CommentAction
		snapshot.ApproveAction = snapshot.CommentAction
		snapshot.RequestChangesAction = snapshot.CommentAction
	}
	if repo.IsArchived {
		snapshot.CommentAction = capabilityFact("denied", "repository_archived")
		snapshot.DraftAction = snapshot.CommentAction
		snapshot.ApproveAction = snapshot.CommentAction
		snapshot.RequestChangesAction = snapshot.CommentAction
	}
	return &reviewSnapshotFacts{Snapshot: snapshot, HeadRepositoryID: pr.HeadRepoID, HeadBranch: pr.HeadBranch, PosterID: issue.PosterID, OriginalAuthorID: issue.OriginalAuthorID}, http.StatusOK
}

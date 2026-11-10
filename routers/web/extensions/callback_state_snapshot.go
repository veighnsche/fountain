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
)

func resolveStateSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryStateSnapshot, int) {
	first, revision, status := observeCallbackSnapshot(ctx, func(ctx context.Context) (*extension.RepositoryStateSnapshot, int) {
		return readStateSnapshot(ctx, admission, request.RepositoryID, *request.StateSnapshot)
	}, equalSnapshot[extension.RepositoryStateSnapshot], nil, []int{http.StatusNotFound})
	if status != http.StatusOK {
		return nil, status
	}
	first.NativeRevision = revision
	return first, http.StatusOK
}

func readStateSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryStateSnapshotRequest) (*extension.RepositoryStateSnapshot, int) {
	issue, subjectID, status := issueOrPullParent(ctx, admission, repository, extension.RepositoryCommentsRequest{Subject: q.Subject, Index: q.Index})
	if status != http.StatusOK {
		return nil, status
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	repo, err := repo_model.GetRepositoryByID(ctx, issue.RepoID)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, user)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	facts := issues_model.ReadDiscussionActionFacts(ctx, repo, issue, user, permission)
	if !facts.Readable {
		return nil, http.StatusNotFound
	}
	hasMerged := false
	if issue.IsPull {
		if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil {
			return nil, http.StatusServiceUnavailable
		}
		hasMerged = issue.PullRequest.HasMerged
	}
	closeBlocker, reopenBlocker := facts.StateActions(hasMerged)
	if closeBlocker == issues_model.NativeActionAllowed {
		closeBlocker, err = issues_model.CheckNativeCloseDependencies(ctx, repo, issue)
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
	}
	close, reopen := discussionStateCapability(closeBlocker, "discussion_open"), discussionStateCapability(reopenBlocker, "discussion_closed")
	return &extension.RepositoryStateSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), Subject: q.Subject, SubjectID: subjectID, IssueID: strconv.FormatInt(issue.ID, 10), Index: q.Index, IsClosed: issue.IsClosed, HasMerged: hasMerged, CloseAction: close, ReopenAction: reopen}, http.StatusOK
}

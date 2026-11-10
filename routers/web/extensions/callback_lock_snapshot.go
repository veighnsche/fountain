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

func resolveLockSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryLockSnapshot, int) {
	first, revision, status := observeCallbackSnapshot(ctx, func(ctx context.Context) (*extension.RepositoryLockSnapshot, int) {
		return readLockSnapshot(ctx, admission, request.RepositoryID, *request.LockSnapshot)
	}, equalSnapshot[extension.RepositoryLockSnapshot], nil, []int{http.StatusNotFound})
	if status != http.StatusOK {
		return nil, status
	}
	first.NativeRevision = revision
	return first, http.StatusOK
}

func readLockSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryLockSnapshotRequest) (*extension.RepositoryLockSnapshot, int) {
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
	lockBlocker, unlockBlocker := facts.LockActions()
	lock, unlock := discussionLockCapability(lockBlocker, "discussion_unlocked"), discussionLockCapability(unlockBlocker, "discussion_locked")
	return &extension.RepositoryLockSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), Subject: q.Subject, SubjectID: subjectID, IssueID: strconv.FormatInt(issue.ID, 10), Index: q.Index, IsLocked: issue.IsLocked, LockAction: lock, UnlockAction: unlock}, http.StatusOK
}

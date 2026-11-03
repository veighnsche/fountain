// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

func resolveLockSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryLockSnapshot, int) {
	before, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil || !before.Idle {
		return nil, http.StatusServiceUnavailable
	}
	first, status := readLockSnapshot(ctx, admission, request.RepositoryID, *request.LockSnapshot)
	if status != http.StatusOK {
		return nil, status
	}
	second, status := readLockSnapshot(ctx, admission, request.RepositoryID, *request.LockSnapshot)
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
	first.NativeRevision = before.Revision
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
	kind := unit.TypeIssues
	if issue.IsPull {
		kind = unit.TypePullRequests
	}
	if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
		return nil, http.StatusNotFound
	}
	lock, unlock := capabilityFact("denied", "lock_writer_required"), capabilityFact("denied", "lock_writer_required")
	// Match the native lock/unlock routes' RequireRepoWriterOr(Issues, Pulls),
	// after GetActionIssue's own subject-read check. Authorship is insufficient;
	// closed or merged state does not itself remove a writer's lock permission.
	if permission.CanAccessAny(perm.AccessModeWrite, unit.TypeIssues, unit.TypePullRequests) {
		lock = capabilityFact("allowed", "discussion_unlocked")
		unlock = capabilityFact("denied", "discussion_not_locked")
		if issue.IsLocked {
			lock, unlock = capabilityFact("denied", "discussion_already_locked"), capabilityFact("allowed", "discussion_locked")
		}
	}
	if repo.IsArchived {
		lock, unlock = capabilityFact("denied", "repository_archived"), capabilityFact("denied", "repository_archived")
	}
	return &extension.RepositoryLockSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), Subject: q.Subject, SubjectID: subjectID, IssueID: strconv.FormatInt(issue.ID, 10), Index: q.Index, IsLocked: issue.IsLocked, LockAction: lock, UnlockAction: unlock}, http.StatusOK
}

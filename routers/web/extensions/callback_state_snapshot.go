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
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

func resolveStateSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryStateSnapshot, int) {
	before, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil || !before.Idle {
		return nil, http.StatusServiceUnavailable
	}
	first, status := readStateSnapshot(ctx, admission, request.RepositoryID, *request.StateSnapshot)
	if status != http.StatusOK {
		return nil, status
	}
	second, status := readStateSnapshot(ctx, admission, request.RepositoryID, *request.StateSnapshot)
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
	kind := unit.TypeIssues
	if issue.IsPull {
		kind = unit.TypePullRequests
	}
	if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
		return nil, http.StatusNotFound
	}
	// Match native EditIssue/EditPullRequest direct state permission and the
	// status model's block/dependency checks. A comment lock does not gate the
	// direct state API; imported poster identity does not confer authorship.
	close, reopen := capabilityFact("denied", "state_author_or_writer_required"), capabilityFact("denied", "state_author_or_writer_required")
	if issue.IsPoster(user.ID) || permission.CanWriteIssuesOrPulls(issue.IsPull) {
		close, reopen = capabilityFact("allowed", "discussion_open"), capabilityFact("denied", "discussion_not_closed")
		if issue.IsClosed {
			close, reopen = capabilityFact("denied", "discussion_already_closed"), capabilityFact("allowed", "discussion_closed")
		}
	}
	hasMerged := false
	if issue.IsPull {
		if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil {
			return nil, http.StatusServiceUnavailable
		}
		hasMerged = issue.PullRequest.HasMerged
		if hasMerged {
			close, reopen = capabilityFact("denied", "pull_already_merged"), capabilityFact("denied", "pull_already_merged")
		}
	}
	if user_model.IsBlockedMultiple(ctx, []int64{repo.OwnerID, issue.PosterID}, user.ID) {
		close, reopen = capabilityFact("denied", "state_actor_blocked"), capabilityFact("denied", "state_actor_blocked")
	}
	if repo.IsArchived {
		close, reopen = capabilityFact("denied", "repository_archived"), capabilityFact("denied", "repository_archived")
	}
	if close.State == "allowed" && repo.IsDependenciesEnabled(ctx) {
		clear, err := issues_model.IssueNoDependenciesLeft(ctx, issue)
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		if !clear {
			close = capabilityFact("denied", "open_dependencies")
		}
	}
	return &extension.RepositoryStateSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), Subject: q.Subject, SubjectID: subjectID, IssueID: strconv.FormatInt(issue.ID, 10), Index: q.Index, IsClosed: issue.IsClosed, HasMerged: hasMerged, CloseAction: close, ReopenAction: reopen}, http.StatusOK
}

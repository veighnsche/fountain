// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	extension "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

type threadSnapshotFacts struct {
	Snapshot extension.RepositoryThreadSnapshot
	Review   reviewSnapshotFacts
}

func readThreadSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryThreadSnapshotRequest) (*threadSnapshotFacts, int) {
	// Reuse the existing native PR/Git/reader owner, including the base repository's
	// hidden PR ref. Resolution uses its own native policy, never review-event policy.
	parent, status := readReviewSnapshot(ctx, admission, repository, extension.RepositoryReviewSnapshotRequest{Index: q.Index})
	if status != http.StatusOK {
		return nil, status
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	issue, err := issues_model.GetIssueByID(ctx, mustParsePositiveID(parent.Snapshot.IssueID))
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if issue.RepoID != mustParsePositiveID(repository) || !issue.IsPull {
		return nil, http.StatusConflict
	}
	evidence, err := nativeoperation_service.ReadHumanThreadEvidence(ctx, issue, user, mustParsePositiveID(q.ThreadID))
	if err != nil {
		switch {
		case errors.Is(err, nativeoperation_service.ErrHumanThreadMissing):
			return nil, http.StatusNotFound
		case errors.Is(err, nativeoperation_service.ErrHumanThreadTarget):
			return nil, http.StatusUnprocessableEntity
		case errors.Is(err, nativeoperation_service.ErrHumanThreadChanged):
			return nil, http.StatusConflict
		case errors.Is(err, nativeoperation_service.ErrHumanThreadTooLarge):
			return nil, http.StatusRequestEntityTooLarge
		default:
			return nil, http.StatusServiceUnavailable
		}
	}
	allowed, err := issues_model.CanMarkConversation(ctx, issue, user)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	resolve, unresolve := capabilityFact("denied", "thread_author_writer_or_official_reviewer_required"), capabilityFact("denied", "thread_author_writer_or_official_reviewer_required")
	if allowed {
		resolve, unresolve = capabilityFact("allowed", "thread_resolve"), capabilityFact("allowed", "thread_unresolve")
	}
	repo, err := repo_model.GetRepositoryByID(ctx, issue.RepoID)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if repo.IsArchived {
		resolve, unresolve = capabilityFact("denied", "repository_archived"), capabilityFact("denied", "repository_archived")
	}
	return &threadSnapshotFacts{Snapshot: extension.RepositoryThreadSnapshot{Review: parent.Snapshot, Thread: evidence, ResolveAction: resolve, UnresolveAction: unresolve}, Review: *parent}, http.StatusOK
}

func resolveThreadSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryThreadSnapshot, int) {
	first, revision, status := observeCallbackSnapshot(ctx, func(ctx context.Context) (*threadSnapshotFacts, int) {
		return readThreadSnapshot(ctx, admission, request.RepositoryID, *request.ThreadSnapshot)
	}, func(a, b *threadSnapshotFacts) bool { return reflect.DeepEqual(a, b) }, func(ctx context.Context) int { _, status := resolveCurrentUser(ctx, admission); return status }, []int{http.StatusNotFound, http.StatusUnprocessableEntity})
	if status != http.StatusOK {
		return nil, status
	}
	first.Snapshot.Review.NativeRevision = revision
	return &first.Snapshot, http.StatusOK
}

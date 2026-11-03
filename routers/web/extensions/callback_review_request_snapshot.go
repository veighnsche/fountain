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
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

type reviewRequestSnapshotFacts struct {
	Snapshot extension.RepositoryReviewRequestSnapshot
	Parent   reviewSnapshotFacts
}

func readReviewRequestSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryReviewRequestSnapshotRequest) (*reviewRequestSnapshotFacts, int) {
	parent, status := readReviewSnapshot(ctx, admission, repository, extension.RepositoryReviewSnapshotRequest{Index: q.Index})
	if status != http.StatusOK {
		return nil, status
	}
	actor, status := resolveCurrentUser(ctx, admission)
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
	selection, err := nativeoperation_service.ReadHumanReviewRequestSelection(ctx, actor, issue, q.ReviewerKind, mustParsePositiveID(q.ReviewerID))
	if errors.Is(err, nativeoperation_service.ErrHumanReviewRequestMissing) {
		return nil, http.StatusNotFound
	}
	if errors.Is(err, nativeoperation_service.ErrHumanReviewRequestTooLarge) {
		return nil, http.StatusRequestEntityTooLarge
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	return &reviewRequestSnapshotFacts{Snapshot: extension.RepositoryReviewRequestSnapshot{Review: parent.Snapshot, ReviewerKind: q.ReviewerKind, ReviewerID: q.ReviewerID, Requests: selection.Requests, AddAction: selection.AddAction, RemoveAction: selection.RemoveAction}, Parent: *parent}, http.StatusOK
}

func resolveReviewRequestSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryReviewRequestSnapshot, int) {
	before, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil || !before.Idle {
		return nil, http.StatusServiceUnavailable
	}
	first, status := readReviewRequestSnapshot(ctx, admission, request.RepositoryID, *request.ReviewRequestSnapshot)
	if status != http.StatusOK {
		return nil, status
	}
	second, status := readReviewRequestSnapshot(ctx, admission, request.RepositoryID, *request.ReviewRequestSnapshot)
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
	if !after.Idle || after.Revision != before.Revision || !reflect.DeepEqual(first, second) {
		return nil, http.StatusConflict
	}
	first.Snapshot.Review.NativeRevision = before.Revision
	return &first.Snapshot, http.StatusOK
}

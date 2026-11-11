// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"net/http"

	extension "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

type inlineSnapshotFacts struct {
	Review reviewSnapshotFacts
	Proof  nativeoperation_service.HumanInlineProof
}

func readInlineSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryInlineSnapshotRequest) (*inlineSnapshotFacts, int) {
	review, status := readReviewSnapshot(ctx, admission, repository, extension.RepositoryReviewSnapshotRequest{Index: q.Index})
	if status != http.StatusOK {
		return nil, status
	}
	if q.Anchor.Base != review.Snapshot.MergeBaseOID || q.Anchor.Head != review.Snapshot.HeadOID {
		return nil, http.StatusConflict
	}
	repo, err := repo_model.GetRepositoryByID(ctx, mustParsePositiveID(repository))
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	native, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	defer native.Close()
	a := q.Anchor
	proof, err := nativeoperation_service.ProveHumanInlineAnchor(ctx, native, issues_model.CodeCommentAnchor{Base: a.Base, Head: a.Head, Path: a.Path, Side: a.Side, Scope: a.Scope, StartLine: a.StartLine, EndLine: a.EndLine})
	if err != nil {
		var refused issues_model.ErrExactReviewRefused
		if errors.As(err, &refused) {
			return nil, http.StatusUnprocessableEntity
		}
		return nil, http.StatusServiceUnavailable
	}
	return &inlineSnapshotFacts{Review: *review, Proof: proof}, http.StatusOK
}

func resolveInlineSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryInlineSnapshot, int) {
	first, revision, status := observeCallbackSnapshot(ctx, func(ctx context.Context) (*inlineSnapshotFacts, int) {
		return readInlineSnapshot(ctx, admission, request.RepositoryID, *request.InlineSnapshot)
	}, equalSnapshot[inlineSnapshotFacts], nil, []int{http.StatusNotFound, http.StatusUnprocessableEntity})
	if status != http.StatusOK {
		return nil, status
	}
	first.Review.Snapshot.NativeRevision = revision
	return &extension.RepositoryInlineSnapshot{Review: first.Review.Snapshot, Anchor: request.InlineSnapshot.Anchor, EntryOID: first.Proof.EntryOID, EntryKind: first.Proof.EntryKind, DiffDigest: first.Proof.DiffDigest}, http.StatusOK
}

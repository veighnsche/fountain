// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

const OperationRepositoryInlineSnapshot = "repository.inline.snapshot"

var (
	ErrInlineSnapshotQuery       = errors.New("invalid inline preparation query")
	ErrInlineSnapshotChanged     = errors.New("inline preparation changed")
	ErrInlineSnapshotAnchor      = errors.New("inline location is not in the selected diff")
	ErrInlineSnapshotAdmission   = errors.New("current inline preparation admission required")
	ErrInlineSnapshotUnavailable = errors.New("inline preparation unavailable")
)

// RepositoryInlineAnchor is the exact original selection, never a mutable blame
// location. Base is the merge-base tree; Head is the PR head. Path belongs to Side.
type RepositoryInlineAnchor struct {
	Base      string `json:"base"`
	Head      string `json:"head"`
	Path      string `json:"path"`
	Side      string `json:"side"`
	Scope     string `json:"scope"`
	StartLine int64  `json:"start_line"`
	EndLine   int64  `json:"end_line"`
}

func ValidRepositoryInlineAnchor(a RepositoryInlineAnchor) bool {
	if !ValidPullDiffCommit(a.Base) || !ValidPullDiffCommit(a.Head) || a.Path == "" || len(a.Path) > 4096 || !utf8.ValidString(a.Path) || strings.ContainsRune(a.Path, 0) || strings.HasPrefix(a.Path, "/") || path.Clean(a.Path) != a.Path || a.Path == "." || a.Path == ".." || strings.HasPrefix(a.Path, "../") || a.Side != "old" && a.Side != "new" {
		return false
	}
	return a.Scope == "file" && a.StartLine == 0 && a.EndLine == 0 || a.Scope == "lines" && a.StartLine > 0 && a.EndLine >= a.StartLine && a.EndLine <= 9007199254740991
}

type RepositoryInlineSnapshotRequest struct {
	Index  string                 `json:"index"`
	Anchor RepositoryInlineAnchor `json:"anchor"`
}

func ValidRepositoryInlineSnapshotRequest(q RepositoryInlineSnapshotRequest) bool {
	return positivePullID(q.Index) && ValidRepositoryInlineAnchor(q.Anchor)
}

// Preparation proves immutable tree/diff membership and captures current human
// policy/draft facts. It creates no comment/review and grants no write authority.
// A writer must hold native ownership and repeat these proofs before primary SQL.
type RepositoryInlineSnapshot struct {
	Review     RepositoryReviewSnapshot `json:"review"`
	Anchor     RepositoryInlineAnchor   `json:"anchor"`
	EntryOID   string                   `json:"entry_oid"`
	EntryKind  string                   `json:"entry_kind"`
	DiffDigest string                   `json:"diff_digest"`
}

func validRepositoryInlineSnapshot(p *RepositoryInlineSnapshot, repository, actor string, q RepositoryInlineSnapshotRequest) bool {
	return p != nil && ValidRepositoryInlineSnapshotRequest(q) && validRepositoryReviewSnapshot(&p.Review, repository, actor, RepositoryReviewSnapshotRequest{Index: q.Index}) && p.Anchor == q.Anchor && p.Anchor.Base == p.Review.MergeBaseOID && p.Anchor.Head == p.Review.HeadOID && ValidPullDiffCommit(p.EntryOID) && validHumanReviewDigest(p.DiffDigest) && (p.EntryKind == "blob" || p.EntryKind == "commit" && p.Anchor.Scope == "file")
}
func (a Authority) RepositoryInlineSnapshot(ctx context.Context, repository string, q RepositoryInlineSnapshotRequest) (RepositoryInlineSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryInlineSnapshotRequest(q) {
		return RepositoryInlineSnapshot{}, ErrInlineSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryInlineSnapshot, RepositoryID: repository, InlineSnapshot: &q})
	if err != nil {
		return RepositoryInlineSnapshot{}, err
	}
	return *r.InlineSnapshot, nil
}

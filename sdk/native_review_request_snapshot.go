// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"strconv"
)

const OperationRepositoryReviewRequestSnapshot = "repository.review.request.snapshot"
const MaxReviewRequestSnapshotRecords = 1000
const MaxReviewRequestSnapshotBodyBytes = 2 << 20

var (
	ErrReviewRequestSnapshotQuery       = errors.New("invalid reviewer selection query")
	ErrReviewRequestSnapshotAdmission   = errors.New("current reviewer selection admission required")
	ErrReviewRequestSnapshotChanged     = errors.New("reviewer selection changed during read")
	ErrReviewRequestSnapshotTooLarge    = errors.New("complete reviewer request set exceeds preparation bounds")
	ErrReviewRequestSnapshotUnavailable = errors.New("reviewer selection unavailable")
)

// User and team IDs occupy separate native namespaces; never encode a team as a
// negative user. This selects one exact subject, not an unbounded directory.
type RepositoryReviewRequestSnapshotRequest struct {
	Index        string `json:"index"`
	ReviewerKind string `json:"reviewer_kind"`
	ReviewerID   string `json:"reviewer_id"`
}

func ValidRepositoryReviewRequestSnapshotRequest(q RepositoryReviewRequestSnapshotRequest) bool {
	return positivePullID(q.Index) && positivePullID(q.ReviewerID) && (q.ReviewerKind == "user" || q.ReviewerKind == "team")
}

// Evidence binds complete persisted request rows for the selected subject only.
// The digest exposes neither review prose nor another human's pending drafts.
type HumanReviewRequestEvidence struct {
	RequestIDs []string `json:"request_ids"`
	Digest     string   `json:"digest"`
}

func ValidHumanReviewRequestEvidence(e HumanReviewRequestEvidence) bool {
	if e.RequestIDs == nil || len(e.RequestIDs) > MaxReviewRequestSnapshotRecords || !validHumanReviewDigest(e.Digest) {
		return false
	}
	var previous int64
	for _, id := range e.RequestIDs {
		if !positivePullID(id) {
			return false
		}
		n, _ := strconv.ParseInt(id, 10, 64)
		if n <= previous {
			return false
		}
		previous = n
	}
	return true
}

// Preparation is a read, not a mutation grant. A future writer must claim the
// native revision and recheck live human, Git, selected records and native policy.
type RepositoryReviewRequestSnapshot struct {
	Review       RepositoryReviewSnapshot   `json:"review"`
	ReviewerKind string                     `json:"reviewer_kind"`
	ReviewerID   string                     `json:"reviewer_id"`
	Requests     HumanReviewRequestEvidence `json:"requests"`
	AddAction    RepositoryActionCapability `json:"add_action"`
	RemoveAction RepositoryActionCapability `json:"remove_action"`
}

func ValidRepositoryReviewRequestSnapshot(p *RepositoryReviewRequestSnapshot, repository, actor string, q RepositoryReviewRequestSnapshotRequest) bool {
	if p == nil || !ValidRepositoryReviewRequestSnapshotRequest(q) || !validRepositoryReviewSnapshot(&p.Review, repository, actor, RepositoryReviewSnapshotRequest{Index: q.Index}) || p.ReviewerKind != q.ReviewerKind || p.ReviewerID != q.ReviewerID || !ValidHumanReviewRequestEvidence(p.Requests) {
		return false
	}
	for _, action := range []RepositoryActionCapability{p.AddAction, p.RemoveAction} {
		if action.State != "allowed" && action.State != "denied" || len(action.Reason) > 256 {
			return false
		}
	}
	return true
}

func (a Authority) RepositoryReviewRequestSnapshot(ctx context.Context, repository string, q RepositoryReviewRequestSnapshotRequest) (RepositoryReviewRequestSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryReviewRequestSnapshotRequest(q) {
		return RepositoryReviewRequestSnapshot{}, ErrReviewRequestSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryReviewRequestSnapshot, RepositoryID: repository, ReviewRequestSnapshot: &q})
	if err != nil {
		return RepositoryReviewRequestSnapshot{}, err
	}
	return *r.ReviewRequestSnapshot, nil
}

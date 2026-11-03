// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
)

const OperationRepositoryManualMergeSnapshot = "repository.manual_merge.snapshot"

var (
	ErrManualMergeSnapshotQuery       = errors.New("invalid manual merge preparation query")
	ErrManualMergeSnapshotAdmission   = errors.New("current human manual merge preparation admission required")
	ErrManualMergeSnapshotChanged     = errors.New("manual merge preparation changed during read")
	ErrManualMergeSnapshotCommit      = errors.New("manual merge commit is not a full reachable target commit")
	ErrManualMergeSnapshotUnavailable = errors.New("manual merge preparation unavailable")
)

type RepositoryManualMergeSnapshotRequest struct {
	Index     string `json:"index"`
	CommitOID string `json:"commit_oid"`
}

func ValidRepositoryManualMergeSnapshotRequest(q RepositoryManualMergeSnapshotRequest) bool {
	return positivePullID(q.Index) && ValidPullDiffCommit(q.CommitOID)
}

// Manual preparation selects an existing commit on the current target branch.
// It is separate from computing/pushing a merge and conveys no write authority.
// Source-fork identity, head availability and review prose are not prerequisites
// or exposed fields. NativeRevision binds this read to the current idle domain.
type RepositoryManualMergeSnapshot struct {
	RepositoryID   string                     `json:"repository_id"`
	ActorID        string                     `json:"actor_id"`
	PullID         string                     `json:"pull_id"`
	IssueID        string                     `json:"issue_id"`
	Index          string                     `json:"index"`
	BaseBranch     string                     `json:"base_branch"`
	BaseOID        string                     `json:"base_oid"`
	CommitOID      string                     `json:"commit_oid"`
	IsClosed       bool                       `json:"is_closed"`
	HasMerged      bool                       `json:"has_merged"`
	NativeRevision int64                      `json:"native_revision"`
	Action         RepositoryActionCapability `json:"action"`
}

func ValidRepositoryManualMergeSnapshot(p *RepositoryManualMergeSnapshot, repository, actor string, q RepositoryManualMergeSnapshotRequest) bool {
	if p == nil || !positivePullID(repository) || !positivePullID(actor) || !ValidRepositoryManualMergeSnapshotRequest(q) || p.RepositoryID != repository || p.ActorID != actor || !positivePullID(p.PullID) || !positivePullID(p.IssueID) || p.Index != q.Index || p.CommitOID != q.CommitOID || !validReviewSubmitPayloadRef("refs/heads/"+p.BaseBranch) || !ValidPullDiffCommit(p.BaseOID) || len(p.BaseOID) != len(p.CommitOID) || p.NativeRevision < 0 || p.HasMerged && !p.IsClosed || len(p.Action.Reason) > 256 {
		return false
	}
	switch p.Action.State {
	case "allowed", "denied", "unsupported", "unavailable", "unknown":
		return true
	default:
		return false
	}
}

func (a Authority) RepositoryManualMergeSnapshot(ctx context.Context, repository string, q RepositoryManualMergeSnapshotRequest) (RepositoryManualMergeSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryManualMergeSnapshotRequest(q) {
		return RepositoryManualMergeSnapshot{}, ErrManualMergeSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryManualMergeSnapshot, RepositoryID: repository, ManualMergeSnapshot: &q})
	if err != nil {
		return RepositoryManualMergeSnapshot{}, err
	}
	return *r.ManualMergeSnapshot, nil
}

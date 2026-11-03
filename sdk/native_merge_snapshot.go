// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
)

const OperationRepositoryMergeSnapshot = "repository.merge.snapshot"

var (
	ErrMergeSnapshotQuery       = errors.New("invalid human merge preparation query")
	ErrMergeSnapshotAdmission   = errors.New("current human merge preparation admission required")
	ErrMergeSnapshotChanged     = errors.New("human merge preparation changed during read")
	ErrMergeSnapshotUnavailable = errors.New("human merge preparation unavailable")
)

type RepositoryMergeSnapshotRequest struct {
	Index  string `json:"index"`
	Method string `json:"method"`
}

// This selects an ordinary immediate method, never forced, scheduled, manual
// marking or branch update. Native method availability is separate from whether
// an admitted journaled writer for that method has been implemented/qualified.
func ValidRepositoryMergeSnapshotRequest(q RepositoryMergeSnapshotRequest) bool {
	if !positivePullID(q.Index) {
		return false
	}
	switch q.Method {
	case "merge", "rebase", "rebase-merge", "squash", "fast-forward-only":
		return true
	default:
		return false
	}
}

// Preparation reads exact current target tips, not an execution grant. Source
// fork identities/branches and review prose stay private. BaseOID is the target
// branch tip; MergeBaseOID is explicitly the derived common ancestor.
type RepositoryMergeSnapshot struct {
	RepositoryID   string                     `json:"repository_id"`
	ActorID        string                     `json:"actor_id"`
	PullID         string                     `json:"pull_id"`
	IssueID        string                     `json:"issue_id"`
	Index          string                     `json:"index"`
	Method         string                     `json:"method"`
	BaseBranch     string                     `json:"base_branch"`
	HeadOID        string                     `json:"head_oid"`
	BaseOID        string                     `json:"base_oid"`
	MergeBaseOID   string                     `json:"merge_base_oid"`
	IsClosed       bool                       `json:"is_closed"`
	HasMerged      bool                       `json:"has_merged"`
	NativeRevision int64                      `json:"native_revision"`
	Action         RepositoryActionCapability `json:"action"`
}

func ValidRepositoryMergeSnapshot(p *RepositoryMergeSnapshot, repository, actor string, q RepositoryMergeSnapshotRequest) bool {
	if p == nil || !positivePullID(repository) || !positivePullID(actor) || !ValidRepositoryMergeSnapshotRequest(q) || p.RepositoryID != repository || p.ActorID != actor || !positivePullID(p.PullID) || !positivePullID(p.IssueID) || p.Index != q.Index || p.Method != q.Method || !validReviewSubmitPayloadRef("refs/heads/"+p.BaseBranch) || !ValidPullDiffCommit(p.HeadOID) || !ValidPullDiffCommit(p.BaseOID) || !ValidPullDiffCommit(p.MergeBaseOID) || p.NativeRevision < 0 || p.HasMerged && !p.IsClosed || len(p.Action.Reason) > 256 {
		return false
	}
	switch p.Action.State {
	case "allowed", "denied", "unsupported", "unavailable", "unknown":
		return true
	default:
		return false
	}
}

func (a Authority) RepositoryMergeSnapshot(ctx context.Context, repository string, q RepositoryMergeSnapshotRequest) (RepositoryMergeSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryMergeSnapshotRequest(q) {
		return RepositoryMergeSnapshot{}, ErrMergeSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryMergeSnapshot, RepositoryID: repository, MergeSnapshot: &q})
	if err != nil {
		return RepositoryMergeSnapshot{}, err
	}
	return *r.MergeSnapshot, nil
}

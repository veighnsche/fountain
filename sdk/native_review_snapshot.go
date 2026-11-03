// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
)

const OperationRepositoryReviewSnapshot = "repository.review.snapshot"

var (
	ErrReviewSnapshotQuery   = errors.New("invalid review preparation query")
	ErrReviewSnapshotChanged = errors.New("review preparation changed during read")
)

type RepositoryReviewSnapshotRequest struct {
	Index string `json:"index"`
}

func ValidRepositoryReviewSnapshotRequest(q RepositoryReviewSnapshotRequest) bool {
	return positivePullID(q.Index)
}

// RepositoryReviewSnapshot prepares human review work, including an existing
// own draft. It allocates nothing and grants no authority. The conditional writer
// must claim NativeRevision and recheck live admission, native identities, Git
// refs, draft and selected event before committing any review or inline comments.
// Commits come from the base repository's PR ref; private fork identities and
// branch names are not exposed merely because its diff is reviewable.
type RepositoryReviewSnapshot struct {
	RepositoryID         string                     `json:"repository_id"`
	ActorID              string                     `json:"actor_id"`
	PullID               string                     `json:"pull_id"`
	IssueID              string                     `json:"issue_id"`
	Index                string                     `json:"index"`
	BaseBranch           string                     `json:"base_branch"`
	HeadOID              string                     `json:"head_oid"`
	BaseOID              string                     `json:"base_oid"`
	MergeBaseOID         string                     `json:"merge_base_oid"`
	IsClosed             bool                       `json:"is_closed"`
	HasMerged            bool                       `json:"has_merged"`
	IsAuthor             bool                       `json:"is_author"`
	PendingReviewID      string                     `json:"pending_review_id,omitempty"`
	PendingCommitID      string                     `json:"pending_commit_id,omitempty"`
	NativeRevision       int64                      `json:"native_revision"`
	CommentAction        RepositoryActionCapability `json:"comment_action"`
	DraftAction          RepositoryActionCapability `json:"draft_action"`
	ApproveAction        RepositoryActionCapability `json:"approve_action"`
	RequestChangesAction RepositoryActionCapability `json:"request_changes_action"`
}

func validRepositoryReviewSnapshot(p *RepositoryReviewSnapshot, repository, actor string, q RepositoryReviewSnapshotRequest) bool {
	if p == nil || !positivePullID(repository) || !positivePullID(actor) || p.RepositoryID != repository || p.ActorID != actor || !ValidRepositoryReviewSnapshotRequest(q) || p.Index != q.Index || !positivePullID(p.PullID) || !positivePullID(p.IssueID) ||
		!validReviewSubmitPayloadRef("refs/heads/"+p.BaseBranch) || !ValidPullDiffCommit(p.HeadOID) || !ValidPullDiffCommit(p.BaseOID) || !ValidPullDiffCommit(p.MergeBaseOID) || p.NativeRevision < 0 || p.HasMerged && !p.IsClosed {
		return false
	}
	if p.PendingReviewID == "" && p.PendingCommitID != "" || p.PendingReviewID != "" && !positivePullID(p.PendingReviewID) || p.PendingCommitID != "" && !ValidPullDiffCommit(p.PendingCommitID) {
		return false
	}
	for _, action := range []RepositoryActionCapability{p.CommentAction, p.DraftAction, p.ApproveAction, p.RequestChangesAction} {
		if action.State != "allowed" && action.State != "denied" || len(action.Reason) > 256 {
			return false
		}
	}
	return !p.IsAuthor || p.ApproveAction.State != "allowed" && p.RequestChangesAction.State != "allowed"
}

func (a Authority) RepositoryReviewSnapshot(ctx context.Context, repository string, q RepositoryReviewSnapshotRequest) (RepositoryReviewSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryReviewSnapshotRequest(q) {
		return RepositoryReviewSnapshot{}, ErrReviewSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryReviewSnapshot, RepositoryID: repository, ReviewSnapshot: &q})
	if err != nil {
		return RepositoryReviewSnapshot{}, err
	}
	return *r.ReviewSnapshot, nil
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

const OperationRepositoryPullCreateSnapshot = "repository.pull.create.snapshot"

var (
	ErrPullCreateSnapshotQuery       = errors.New("invalid pull creation preparation query")
	ErrPullCreateSnapshotAdmission   = errors.New("current human pull creation preparation admission required")
	ErrPullCreateSnapshotChanged     = errors.New("pull creation preparation changed during read")
	ErrPullCreateSnapshotUnavailable = errors.New("pull creation preparation unavailable")
)

// Explicit repositories and branch names select an ordinary PR, never a tag,
// revision expression, new branch, computed commit or credentialed agent source.
type RepositoryPullCreateSnapshotRequest struct {
	HeadRepositoryID string `json:"head_repository_id"`
	BaseBranch       string `json:"base_branch"`
	HeadBranch       string `json:"head_branch"`
}

func ValidRepositoryPullCreateSnapshotRequest(q RepositoryPullCreateSnapshotRequest) bool {
	for _, branch := range []string{q.BaseBranch, q.HeadBranch} {
		if strings.ContainsAny(branch, "[") || strings.Contains(branch, "@{") || strings.HasSuffix(branch, ".") {
			return false
		}
		for _, part := range strings.Split(branch, "/") {
			if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
				return false
			}
		}
		for _, c := range branch {
			if c < ' ' || c == 0x7f {
				return false
			}
		}
	}
	return positivePullID(q.HeadRepositoryID) && utf8.ValidString(q.BaseBranch) && utf8.ValidString(q.HeadBranch) &&
		validPRCreatePayloadRef("refs/heads/"+q.BaseBranch) && validPRCreatePayloadRef("refs/heads/"+q.HeadBranch)
}

// This is current read evidence, not creation authority or a mergeability
// promise. The later writer must claim the revision, reread both tips/policy and
// run ordinary native PR comparison/creation under that owner before effects.
// No issue/index, ref, operation, notification or Git remote is allocated here.
type RepositoryPullCreateSnapshot struct {
	RepositoryID     string                     `json:"repository_id"`
	ActorID          string                     `json:"actor_id"`
	HeadRepositoryID string                     `json:"head_repository_id"`
	BaseBranch       string                     `json:"base_branch"`
	HeadBranch       string                     `json:"head_branch"`
	BaseOID          string                     `json:"base_oid"`
	HeadOID          string                     `json:"head_oid"`
	NativeRevision   int64                      `json:"native_revision"`
	Action           RepositoryActionCapability `json:"action"`
}

func ValidRepositoryPullCreateSnapshot(p *RepositoryPullCreateSnapshot, repository, actor string, q RepositoryPullCreateSnapshotRequest) bool {
	if p == nil || !positivePullID(repository) || !positivePullID(actor) || !ValidRepositoryPullCreateSnapshotRequest(q) || p.RepositoryID != repository || p.ActorID != actor ||
		p.HeadRepositoryID != q.HeadRepositoryID || p.BaseBranch != q.BaseBranch || p.HeadBranch != q.HeadBranch || !ValidPullDiffCommit(p.BaseOID) || !ValidPullDiffCommit(p.HeadOID) || len(p.BaseOID) != len(p.HeadOID) || p.NativeRevision < 0 || !utf8.ValidString(p.Action.Reason) || len(p.Action.Reason) > 256 {
		return false
	}
	return p.Action.State == "allowed" || p.Action.State == "denied"
}

func (a Authority) RepositoryPullCreateSnapshot(ctx context.Context, repository string, q RepositoryPullCreateSnapshotRequest) (RepositoryPullCreateSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryPullCreateSnapshotRequest(q) {
		return RepositoryPullCreateSnapshot{}, ErrPullCreateSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryPullCreateSnapshot, RepositoryID: repository, PullCreateSnapshot: &q})
	if err != nil {
		return RepositoryPullCreateSnapshot{}, err
	}
	return *r.PullCreateSnapshot, nil
}

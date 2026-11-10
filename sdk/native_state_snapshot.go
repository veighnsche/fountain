// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
)

const OperationRepositoryStateSnapshot = "repository.state.snapshot"

var (
	ErrStateSnapshotQuery   = errors.New("invalid state preparation query")
	ErrStateSnapshotChanged = errors.New("state preparation changed during read")
)

type RepositoryStateSnapshotRequest struct {
	Subject string `json:"subject"`
	Index   string `json:"index"`
}

func ValidRepositoryStateSnapshotRequest(q RepositoryStateSnapshotRequest) bool {
	return (q.Subject == "issue" || q.Subject == "pull") && positivePullID(q.Index)
}

// RepositoryStateSnapshot prepares direct issue/PR close/reopen, not combined
// comment posting or PR ref synchronization. It allocates nothing and grants
// no authority. Dispatch must claim NativeRevision and recheck current human,
// exact parent/state, merged status, unit/permission, blocks, dependencies and
// archive rules before native status/history changes.
type RepositoryStateSnapshot struct {
	RepositoryID   string                     `json:"repository_id"`
	ActorID        string                     `json:"actor_id"`
	Subject        string                     `json:"subject"`
	SubjectID      string                     `json:"subject_id"`
	IssueID        string                     `json:"issue_id"`
	Index          string                     `json:"index"`
	IsClosed       bool                       `json:"is_closed"`
	HasMerged      bool                       `json:"has_merged"`
	NativeRevision int64                      `json:"native_revision"`
	CloseAction    RepositoryActionCapability `json:"close_action"`
	ReopenAction   RepositoryActionCapability `json:"reopen_action"`
}

func validRepositoryStateSnapshot(p *RepositoryStateSnapshot, repository, actor string, q RepositoryStateSnapshotRequest) bool {
	if p == nil || !positivePullID(repository) || !positivePullID(actor) || p.RepositoryID != repository || p.ActorID != actor || p.Subject != q.Subject || p.Index != q.Index ||
		!ValidRepositoryStateSnapshotRequest(q) || !positivePullID(p.SubjectID) || !positivePullID(p.IssueID) || p.Subject == "issue" && p.SubjectID != p.IssueID || p.NativeRevision < 0 || p.Subject == "issue" && p.HasMerged {
		return false
	}
	for _, action := range []RepositoryActionCapability{p.CloseAction, p.ReopenAction} {
		if action.State != "allowed" && action.State != "denied" || len(action.Reason) > 256 {
			return false
		}
	}
	return (!p.IsClosed || p.CloseAction.State != "allowed") && (p.IsClosed || p.ReopenAction.State != "allowed") && (!p.HasMerged || p.CloseAction.State != "allowed" && p.ReopenAction.State != "allowed")
}

func (a Authority) RepositoryStateSnapshot(ctx context.Context, repository string, q RepositoryStateSnapshotRequest) (RepositoryStateSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryStateSnapshotRequest(q) {
		return RepositoryStateSnapshot{}, ErrStateSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryStateSnapshot, RepositoryID: repository, StateSnapshot: &q})
	if err != nil {
		return RepositoryStateSnapshot{}, err
	}
	return *r.StateSnapshot, nil
}

func stateSnapshotCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		conflict: ErrStateSnapshotChanged,
		notFound: ErrRepositoryNotVisible,
	}).forStatus(status)
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
)

const OperationRepositoryLockSnapshot = "repository.lock.snapshot"

var (
	ErrLockSnapshotQuery   = errors.New("invalid lock preparation query")
	ErrLockSnapshotChanged = errors.New("lock preparation changed during read")
)

type RepositoryLockSnapshotRequest struct {
	Subject string `json:"subject"`
	Index   string `json:"index"`
}

func ValidRepositoryLockSnapshotRequest(q RepositoryLockSnapshotRequest) bool {
	return (q.Subject == "issue" || q.Subject == "pull") && positivePullID(q.Index)
}

// RepositoryLockSnapshot prepares only comment lock/unlock. It observes no
// mutation authority, allocates nothing and grants no execution permission.
// Dispatch must claim NativeRevision and recheck live human, exact parent,
// current lock state, enabled subject unit, permission and archive conditions.
type RepositoryLockSnapshot struct {
	RepositoryID   string                     `json:"repository_id"`
	ActorID        string                     `json:"actor_id"`
	Subject        string                     `json:"subject"`
	SubjectID      string                     `json:"subject_id"`
	IssueID        string                     `json:"issue_id"`
	Index          string                     `json:"index"`
	IsLocked       bool                       `json:"is_locked"`
	NativeRevision int64                      `json:"native_revision"`
	LockAction     RepositoryActionCapability `json:"lock_action"`
	UnlockAction   RepositoryActionCapability `json:"unlock_action"`
}

func validRepositoryLockSnapshot(p *RepositoryLockSnapshot, repository, actor string, q RepositoryLockSnapshotRequest) bool {
	if p == nil || !positivePullID(repository) || !positivePullID(actor) || p.RepositoryID != repository || p.ActorID != actor || p.Subject != q.Subject || p.Index != q.Index ||
		!ValidRepositoryLockSnapshotRequest(q) || !positivePullID(p.SubjectID) || !positivePullID(p.IssueID) || p.Subject == "issue" && p.SubjectID != p.IssueID || p.NativeRevision < 0 {
		return false
	}
	for _, action := range []RepositoryActionCapability{p.LockAction, p.UnlockAction} {
		if action.State != "allowed" && action.State != "denied" || len(action.Reason) > 256 {
			return false
		}
	}
	return (!p.IsLocked || p.LockAction.State != "allowed") && (p.IsLocked || p.UnlockAction.State != "allowed")
}

func (a Authority) RepositoryLockSnapshot(ctx context.Context, repository string, q RepositoryLockSnapshotRequest) (RepositoryLockSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryLockSnapshotRequest(q) {
		return RepositoryLockSnapshot{}, ErrLockSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryLockSnapshot, RepositoryID: repository, LockSnapshot: &q})
	if err != nil {
		return RepositoryLockSnapshot{}, err
	}
	return *r.LockSnapshot, nil
}

func lockSnapshotCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		conflict: ErrLockSnapshotChanged,
		notFound: ErrRepositoryNotVisible,
	}).forStatus(status)
}

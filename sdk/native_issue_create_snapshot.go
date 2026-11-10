// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
)

const OperationRepositoryIssueCreateSnapshot = "repository.issue.create.snapshot"

var (
	ErrIssueCreateSnapshotQuery   = errors.New("invalid issue creation preparation query")
	ErrIssueCreateSnapshotChanged = errors.New("issue creation preparation changed during read")
)

// RepositoryIssueCreateSnapshot prepares an ordinary human issue creation.
// It allocates no issue/index, claims no operation and grants no write or
// automation authority. Creation must claim the observed revision and recheck
// the current human, internal issue unit, permission, archive and block rules.
type RepositoryIssueCreateSnapshot struct {
	RepositoryID   string                     `json:"repository_id"`
	ActorID        string                     `json:"actor_id"`
	NativeRevision int64                      `json:"native_revision"`
	Action         RepositoryActionCapability `json:"action"`
}

func validRepositoryIssueCreateSnapshot(p *RepositoryIssueCreateSnapshot, repository, actor string) bool {
	return p != nil && p.RepositoryID == repository && p.ActorID == actor &&
		positivePullID(repository) && positivePullID(actor) && p.NativeRevision >= 0 &&
		(p.Action.State == "allowed" || p.Action.State == "denied") && len(p.Action.Reason) <= 256
}

func (a Authority) RepositoryIssueCreateSnapshot(ctx context.Context, repository string) (RepositoryIssueCreateSnapshot, error) {
	if !positivePullID(repository) {
		return RepositoryIssueCreateSnapshot{}, ErrIssueCreateSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryIssueCreateSnapshot, RepositoryID: repository})
	if err != nil {
		return RepositoryIssueCreateSnapshot{}, err
	}
	return *r.IssueCreateSnapshot, nil
}

func issueCreateSnapshotCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		conflict: ErrIssueCreateSnapshotChanged,
		notFound: ErrRepositoryNotVisible,
	}).forStatus(status)
}

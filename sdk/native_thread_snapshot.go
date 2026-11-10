// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
)

const OperationRepositoryThreadSnapshot = "repository.thread.snapshot"
const MaxThreadSnapshotMembers = 1000

// MaxThreadSnapshotBodyBytes bounds selected comment prose and retained patches.
const MaxThreadSnapshotBodyBytes = 2 << 20

var (
	ErrThreadSnapshotQuery       = errors.New("invalid thread preparation query")
	ErrThreadSnapshotChanged     = errors.New("thread preparation changed")
	ErrThreadSnapshotTarget      = errors.New("thread preparation requires an exact native root")
	ErrThreadSnapshotAdmission   = errors.New("current thread preparation admission required")
	ErrThreadSnapshotUnavailable = errors.New("thread preparation unavailable")
	ErrThreadSnapshotTooLarge    = errors.New("complete thread exceeds preparation bounds")
)

type RepositoryThreadSnapshotRequest struct {
	Index    string `json:"index"`
	ThreadID string `json:"thread_id"`
}

func ValidRepositoryThreadSnapshotRequest(q RepositoryThreadSnapshotRequest) bool {
	return positivePullID(q.Index) && positivePullID(q.ThreadID)
}

// HumanThreadEvidence binds the entire native conversation in grouping order.
// OriginalAnchor is nil for retained unknown context; it is never reconstructed
// from current blame. Digest shares the conditional-resolution codec; the extra
// evidence digest also pins exact resolver IDs, review/author/time/commit facts.
// No prose, other humans' draft data or private source identity is disclosed.
type HumanThreadEvidence struct {
	RootCommentID      string                  `json:"root_comment_id"`
	ReviewID           string                  `json:"review_id"`
	OriginalAnchor     *RepositoryInlineAnchor `json:"original_anchor"`
	ConversationDigest string                  `json:"conversation_digest"`
	EvidenceDigest     string                  `json:"evidence_digest"`
	MemberCount        int                     `json:"member_count"`
	ResolverID         string                  `json:"resolver_id,omitempty"`
	Resolved           bool                    `json:"resolved"`
}

// Preparation grants no mutation authority. A writer must hold native ownership
// and repeat current admission, policy, Git and full conversation proofs before
// committing either resolve or unresolve against this exact root.
type RepositoryThreadSnapshot struct {
	Review          RepositoryReviewSnapshot   `json:"review"`
	Thread          HumanThreadEvidence        `json:"thread"`
	ResolveAction   RepositoryActionCapability `json:"resolve_action"`
	UnresolveAction RepositoryActionCapability `json:"unresolve_action"`
}

// ValidRepositoryThreadSnapshot checks structural scope and evidence, never authority.
func ValidRepositoryThreadSnapshot(p *RepositoryThreadSnapshot, repository, actor string, q RepositoryThreadSnapshotRequest) bool {
	if p == nil || !ValidRepositoryThreadSnapshotRequest(q) || !validRepositoryReviewSnapshot(&p.Review, repository, actor, RepositoryReviewSnapshotRequest{Index: q.Index}) {
		return false
	}
	t := p.Thread
	if t.RootCommentID != q.ThreadID || !validHumanThreadEvidence(t) {
		return false
	}
	for _, action := range []RepositoryActionCapability{p.ResolveAction, p.UnresolveAction} {
		if action.State != "allowed" && action.State != "denied" || len(action.Reason) > 256 {
			return false
		}
	}
	return true
}

func (a Authority) RepositoryThreadSnapshot(ctx context.Context, repository string, q RepositoryThreadSnapshotRequest) (RepositoryThreadSnapshot, error) {
	if !positivePullID(repository) || !ValidRepositoryThreadSnapshotRequest(q) {
		return RepositoryThreadSnapshot{}, ErrThreadSnapshotQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryThreadSnapshot, RepositoryID: repository, ThreadSnapshot: &q})
	if err != nil {
		return RepositoryThreadSnapshot{}, err
	}
	return *r.ThreadSnapshot, nil
}

func validHumanThreadEvidence(t HumanThreadEvidence) bool {
	return !(!positivePullID(t.RootCommentID) || !positivePullID(t.ReviewID) || t.MemberCount < 1 || t.MemberCount > MaxThreadSnapshotMembers || !validHumanReviewDigest(t.ConversationDigest) || !validHumanReviewDigest(t.EvidenceDigest) || t.OriginalAnchor != nil && !ValidRepositoryInlineAnchor(*t.OriginalAnchor) || t.Resolved != (t.ResolverID != "") || t.ResolverID != "" && !positivePullID(t.ResolverID))
}

func threadSnapshotCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:    ErrThreadSnapshotQuery,
		admission:     ErrThreadSnapshotAdmission,
		notFound:      ErrRepositoryNotVisible,
		conflict:      ErrThreadSnapshotChanged,
		unprocessable: ErrThreadSnapshotTarget,
		tooLarge:      ErrThreadSnapshotTooLarge,
		unavailable:   ErrThreadSnapshotUnavailable,
	}).forStatus(status)
}

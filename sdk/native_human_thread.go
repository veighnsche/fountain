// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"

	"forgejo.org/extension-sdk/operationcontract"
)

const (
	OperationKindHumanThread         = "pull_request.human_thread.apply"
	HumanThreadAuthorizationRevision = "native-human-request"
)

const (
	OperationHumanThreadSubmit = "repository.human_thread.submit"
	OperationHumanThreadGet    = "repository.human_thread.get"
	OperationHumanThreadCancel = "repository.human_thread.cancel"
)

var (
	ErrHumanThreadQuery       = errors.New("invalid human thread request")
	ErrHumanThreadAdmission   = errors.New("current human thread admission is required")
	ErrHumanThreadConflict    = errors.New("human thread operation conflicts")
	ErrHumanThreadUnavailable = errors.New("native human thread service is unavailable")
)

// HumanThreadRequest retains one exact native thread preparation and desired
// resolve/unresolve state. It is not authority; persist it before dispatch.
type HumanThreadRequest struct {
	OperationID string                   `json:"operation_id"`
	Expected    RepositoryThreadSnapshot `json:"expected"`
	NotAfter    int64                    `json:"not_after"`
	Resolved    bool                     `json:"resolved"`
}

type HumanThreadReceipt struct {
	RepositoryID int64               `json:"repository_id"`
	ActorID      int64               `json:"actor_id"`
	PullID       int64               `json:"pull_id"`
	IssueID      int64               `json:"issue_id"`
	Index        int64               `json:"index"`
	Before       HumanThreadEvidence `json:"before"`
	After        HumanThreadEvidence `json:"after"`
	Changed      bool                `json:"changed"`
}

func ValidHumanThreadRequest(q HumanThreadRequest) bool {
	p := q.Expected
	if !validOperationID(q.OperationID) || q.NotAfter <= 0 || !ValidRepositoryThreadSnapshot(&p, p.Review.RepositoryID, p.Review.ActorID, RepositoryThreadSnapshotRequest{Index: p.Review.Index, ThreadID: p.Thread.RootCommentID}) {
		return false
	}
	if q.Resolved {
		return p.ResolveAction.State == "allowed"
	}
	return p.UnresolveAction.State == "allowed"
}

// CanonicalHumanThreadIntent is the exact shared typed codec for dispatch,
// receipt consumers and offline recovery, retaining integer identities/revision.
func CanonicalHumanThreadIntent(q HumanThreadRequest) (string, string, error) {
	if !ValidHumanThreadRequest(q) {
		return "", "", ErrHumanThreadQuery
	}
	p := q.Expected.Review
	actor, _ := strconv.ParseInt(p.ActorID, 10, 64)
	repo, _ := strconv.ParseInt(p.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string             `json:"operation_id"`
		ActorID                int64              `json:"actor_id"`
		RepositoryID           int64              `json:"repository_id"`
		Kind                   string             `json:"kind"`
		AuthorizationRevision  string             `json:"authorization_revision"`
		ExpectedNativeRevision int64              `json:"expected_native_revision"`
		NotAfter               int64              `json:"not_after"`
		Payload                HumanThreadRequest `json:"payload"`
	}{q.OperationID, actor, repo, OperationKindHumanThread, HumanThreadAuthorizationRevision, p.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	return string(raw), HumanReviewBodyDigest(string(raw)), nil
}

// ValidHumanThreadReceipt binds original full/unknown context and exact before
// proof. The native primary authenticates post-state digests in its atomic SQL;
// shape validation does not substitute for native source trust or admission.
func ValidHumanThreadReceipt(r HumanThreadReceipt, q HumanThreadRequest) bool {
	p := q.Expected.Review
	if !ValidHumanThreadRequest(q) || strconv.FormatInt(r.RepositoryID, 10) != p.RepositoryID || strconv.FormatInt(r.ActorID, 10) != p.ActorID || strconv.FormatInt(r.PullID, 10) != p.PullID || strconv.FormatInt(r.IssueID, 10) != p.IssueID || strconv.FormatInt(r.Index, 10) != p.Index || !reflect.DeepEqual(r.Before, q.Expected.Thread) {
		return false
	}
	return r.After.Resolved == q.Resolved && validHumanThreadTransition(r.Before, r.After, r.Changed, p.ActorID)
}

func validHumanThreadTransition(a, b HumanThreadEvidence, changed bool, actor string) bool {
	if !validHumanThreadEvidence(a) || !validHumanThreadEvidence(b) || b.RootCommentID != a.RootCommentID || b.ReviewID != a.ReviewID || b.MemberCount != a.MemberCount || !reflect.DeepEqual(b.OriginalAnchor, a.OriginalAnchor) {
		return false
	}
	resolver := ""
	if b.Resolved {
		resolver = a.ResolverID
		if !a.Resolved {
			resolver = actor
		}
	}
	if b.ResolverID != resolver || changed != (a.ResolverID != resolver) {
		return false
	}
	if !changed {
		return reflect.DeepEqual(a, b)
	}
	return a.ConversationDigest != b.ConversationDigest && a.EvidenceDigest != b.EvidenceDigest
}

// HumanThreadOperationID binds the owning application's durable command to
// its actor/repository. Persist it before dispatch; reuse it for lookup/cancel.
func HumanThreadOperationID(actor, repository, command string) (string, error) {
	return humanOperationID(operationcontract.HumanThreadPrefix, actor, repository, command, ErrHumanThreadQuery)
}

func ValidHumanThreadOperationID(id, actor, repository string) bool {
	return validHumanOperationID(operationcontract.HumanThreadPrefix, id, actor, repository)
}

// ValidHumanThreadInstallationID validates a captured equality condition;
// the host derives its actual installation from current admission.
func ValidHumanThreadInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanThread(ctx context.Context, installation string, q HumanThreadRequest) (OperationRecord, error) {
	if !ValidHumanThreadInstallationID(installation) || !ValidHumanThreadRequest(q) || q.Expected.Review.ActorID != a.Actor.ID || !ValidHumanThreadOperationID(q.OperationID, a.Actor.ID, q.Expected.Review.RepositoryID) {
		return OperationRecord{}, ErrHumanThreadQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanThreadSubmit, RepositoryID: q.Expected.Review.RepositoryID, HumanThread: &q, ThreadInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanThread.Record, nil
}

// GetHumanThread returns the live installation even for absence. Consumers
// compare it with the saved namespace before interpreting retained outcomes.
func (a Authority) GetHumanThread(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanThreadOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanThreadQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanThreadGet, RepositoryID: repository, ThreadOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanThread, nil
}

// CancelHumanThread asserts the same saved installation as submit, including
// when the cancellation creates a tombstone before any submission.
func (a Authority) CancelHumanThread(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanThreadInstallationID(installation) || !ValidHumanThreadOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanThreadQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanThreadCancel, RepositoryID: repository, ThreadOperationID: operation, ThreadInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanThread.Record, nil
}

func validHumanThreadResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.ThreadOperationID
	if request.HumanThread != nil {
		id = request.HumanThread.OperationID
	}
	r, valid := validHumanOperationResult(result, request, humanOperationResult{
		operationID: id, installationID: request.ThreadInstallationID,
		kind: OperationKindHumanThread, getOperation: OperationHumanThreadGet,
		strictDigest: true, allowIntervention: false,
	}, request.HumanThread, CanonicalHumanThreadIntent)
	if !valid || r == nil {
		return valid
	}

	var receipt HumanThreadReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.PullID < 1 || receipt.IssueID < 1 || receipt.Index < 1 || !validHumanThreadTransition(receipt.Before, receipt.After, receipt.Changed, request.Authority.Actor.ID) {
		return false
	}
	if q := request.HumanThread; q != nil {
		return ValidHumanThreadReceipt(receipt, *q)
	}
	return true
}

func humanThreadCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:  ErrHumanThreadQuery,
		admission:   ErrHumanThreadAdmission,
		notFound:    ErrRepositoryNotVisible,
		conflict:    ErrHumanThreadConflict,
		unavailable: ErrHumanThreadUnavailable,
	}).forStatus(status)
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"forgejo.org/extension-sdk/operationcontract"
)

const OperationKindHumanLock = "discussion.lock.apply"
const HumanLockAuthorizationRevision = "native-human-request"
const MaxHumanLockReasonBytes = 64 << 10

const MaxHumanLockCallbackBytes = 512 << 10
const OperationHumanLockSubmit = "repository.lock.submit"
const OperationHumanLockGet = "repository.lock.get"
const OperationHumanLockCancel = "repository.lock.cancel"

var (
	ErrHumanLockQuery       = errors.New("invalid human lock request")
	ErrHumanLockAdmission   = errors.New("current human lock admission is required")
	ErrHumanLockConflict    = errors.New("human lock operation conflicts")
	ErrHumanLockUnavailable = errors.New("native human lock service is unavailable")
)

// HumanLockRequest changes a comment lock, never issue open/closed state.
// Expected is preparation, not authority. Native dispatch rechecks the current
// configured reason list. Unlock uses no reason; lock preserves the exact text.
type HumanLockRequest struct {
	OperationID string                 `json:"operation_id"`
	Expected    RepositoryLockSnapshot `json:"expected"`
	NotAfter    int64                  `json:"not_after"`
	Lock        bool                   `json:"lock"`
	Reason      string                 `json:"reason"`
}

type HumanLockReceipt struct {
	RepositoryID int64  `json:"repository_id"`
	ActorID      int64  `json:"actor_id"`
	Subject      string `json:"subject"`
	SubjectID    int64  `json:"subject_id"`
	IssueID      int64  `json:"issue_id"`
	Index        int64  `json:"index"`
	IsLocked     bool   `json:"is_locked"`
	ReasonDigest string `json:"reason_digest"`
	CommentID    int64  `json:"comment_id"`
}

func ValidHumanLockRequest(q HumanLockRequest) bool {
	p := q.Expected
	if !validOperationID(q.OperationID) || !validRepositoryLockSnapshot(&p, p.RepositoryID, p.ActorID, RepositoryLockSnapshotRequest{Subject: p.Subject, Index: p.Index}) ||
		q.NotAfter <= 0 || q.Lock == p.IsLocked || len(q.Reason) > MaxHumanLockReasonBytes || !utf8.ValidString(q.Reason) || strings.ContainsRune(q.Reason, 0) || !q.Lock && q.Reason != "" {
		return false
	}
	if q.Lock {
		return p.LockAction.State == "allowed"
	}
	return p.UnlockAction.State == "allowed"
}

// CanonicalHumanLockIntent is the shared exact typed codec for native dispatch,
// receipt consumers and offline recovery. It preserves reason bytes and integer
// identity/revision without floating-point normalization.
func CanonicalHumanLockIntent(q HumanLockRequest) (string, string, error) {
	if !ValidHumanLockRequest(q) {
		return "", "", ErrHumanLockQuery
	}
	actor, _ := strconv.ParseInt(q.Expected.ActorID, 10, 64)
	repository, _ := strconv.ParseInt(q.Expected.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string           `json:"operation_id"`
		ActorID                int64            `json:"actor_id"`
		RepositoryID           int64            `json:"repository_id"`
		Kind                   string           `json:"kind"`
		AuthorizationRevision  string           `json:"authorization_revision"`
		ExpectedNativeRevision int64            `json:"expected_native_revision"`
		NotAfter               int64            `json:"not_after"`
		Payload                HumanLockRequest `json:"payload"`
	}{q.OperationID, actor, repository, OperationKindHumanLock, HumanLockAuthorizationRevision, q.Expected.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

// ValidHumanLockReceipt checks saved intent against the allocated native result;
// source authentication and live human admission remain separate requirements.
func ValidHumanLockReceipt(r HumanLockReceipt, q HumanLockRequest) bool {
	sum := sha256.Sum256([]byte(q.Reason))
	return ValidHumanLockRequest(q) && strconv.FormatInt(r.RepositoryID, 10) == q.Expected.RepositoryID && strconv.FormatInt(r.ActorID, 10) == q.Expected.ActorID &&
		r.Subject == q.Expected.Subject && strconv.FormatInt(r.SubjectID, 10) == q.Expected.SubjectID && strconv.FormatInt(r.IssueID, 10) == q.Expected.IssueID &&
		strconv.FormatInt(r.Index, 10) == q.Expected.Index && r.IsLocked == q.Lock && r.ReasonDigest == hex.EncodeToString(sum[:]) && r.CommentID > 0
}

// HumanLockOperationID binds the owning application's durable command to
// its actor/repository. Persist it before dispatch; reuse it for lookup/cancel.
func HumanLockOperationID(actor, repository, command string) (string, error) {
	return humanOperationID(operationcontract.HumanLockPrefix, actor, repository, command, ErrHumanLockQuery)
}

func ValidHumanLockOperationID(id, actor, repository string) bool {
	return validHumanOperationID(operationcontract.HumanLockPrefix, id, actor, repository)
}

// ValidHumanLockInstallationID validates a captured equality condition;
// the host derives its actual installation from current admission.
func ValidHumanLockInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanLock(ctx context.Context, installation string, q HumanLockRequest) (OperationRecord, error) {
	if !ValidHumanLockInstallationID(installation) || !ValidHumanLockRequest(q) || q.Expected.ActorID != a.Actor.ID || !ValidHumanLockOperationID(q.OperationID, a.Actor.ID, q.Expected.RepositoryID) {
		return OperationRecord{}, ErrHumanLockQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanLockSubmit, RepositoryID: q.Expected.RepositoryID, HumanLock: &q, LockInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanLock.Record, nil
}

// GetHumanLock returns the live installation even for absence. Consumers
// compare it with the saved namespace before interpreting retained outcomes.
func (a Authority) GetHumanLock(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanLockOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanLockQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanLockGet, RepositoryID: repository, LockOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanLock, nil
}

// CancelHumanLock asserts the same saved installation as submit, including
// when the cancellation creates a tombstone before any submission.
func (a Authority) CancelHumanLock(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanLockInstallationID(installation) || !ValidHumanLockOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanLockQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanLockCancel, RepositoryID: repository, LockOperationID: operation, LockInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanLock.Record, nil
}

func validHumanLockResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.LockOperationID
	if request.HumanLock != nil {
		id = request.HumanLock.OperationID
	}
	r, valid := validHumanOperationResult(result, request, humanOperationResult{
		operationID: id, installationID: request.LockInstallationID,
		kind: OperationKindHumanLock, getOperation: OperationHumanLockGet,
		strictDigest: false, allowIntervention: true,
	}, request.HumanLock, CanonicalHumanLockIntent)
	if !valid || r == nil {
		return valid
	}

	var receipt HumanLockReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.Subject != "issue" && receipt.Subject != "pull" || receipt.SubjectID < 1 || receipt.IssueID < 1 || receipt.Index < 1 || receipt.CommentID < 1 || receipt.Subject == "issue" && receipt.SubjectID != receipt.IssueID || len(receipt.ReasonDigest) != 64 {
		return false
	}
	for _, c := range receipt.ReasonDigest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	if q := request.HumanLock; q != nil {
		return ValidHumanLockReceipt(receipt, *q)
	}
	return true
}

func humanLockCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:  ErrHumanLockQuery,
		admission:   ErrHumanLockAdmission,
		notFound:    ErrRepositoryNotVisible,
		conflict:    ErrHumanLockConflict,
		unavailable: ErrHumanLockUnavailable,
	}).forStatus(status)
}

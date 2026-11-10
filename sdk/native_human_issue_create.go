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

const (
	OperationKindHumanIssueCreate         = "issue.create"
	HumanIssueCreateAuthorizationRevision = "native-human-request"
	MaxHumanIssueCreateTitleBytes         = 255
	MaxHumanIssueCreateBodyBytes          = 64 << 10
	MaxHumanIssueCreateCallbackBytes      = 512 << 10
	OperationHumanIssueCreateSubmit       = "repository.issue.create.submit"
	OperationHumanIssueCreateGet          = "repository.issue.create.get"
	OperationHumanIssueCreateCancel       = "repository.issue.create.cancel"
)

var (
	ErrHumanIssueCreateQuery       = errors.New("invalid human issue creation request")
	ErrHumanIssueCreateAdmission   = errors.New("current human issue creation admission is required")
	ErrHumanIssueCreateConflict    = errors.New("human issue creation operation conflicts")
	ErrHumanIssueCreateUnavailable = errors.New("native human issue creation service is unavailable")
)

// HumanIssueCreateRequest is immutable preparation, not authority. The native
// transaction allocates the issue and index after checking the current human.
type HumanIssueCreateRequest struct {
	OperationID string                        `json:"operation_id"`
	Expected    RepositoryIssueCreateSnapshot `json:"expected"`
	NotAfter    int64                         `json:"not_after"`
	Title       string                        `json:"title"`
	Body        string                        `json:"body"`
}

type HumanIssueCreateReceipt struct {
	RepositoryID   int64  `json:"repository_id"`
	ActorID        int64  `json:"actor_id"`
	IssueID        int64  `json:"issue_id"`
	Index          int64  `json:"index"`
	Title          string `json:"title"`
	BodyDigest     string `json:"body_digest"`
	ContentVersion int64  `json:"content_version"`
}

func ValidHumanIssueCreateRequest(q HumanIssueCreateRequest) bool {
	p := q.Expected
	return validOperationID(q.OperationID) && validRepositoryIssueCreateSnapshot(&p, p.RepositoryID, p.ActorID) &&
		p.Action.State == "allowed" && q.NotAfter > 0 &&
		q.Title != "" && strings.TrimSpace(q.Title) == q.Title && len(q.Title) <= MaxHumanIssueCreateTitleBytes &&
		utf8.ValidString(q.Title) && !strings.ContainsRune(q.Title, 0) &&
		len(q.Body) <= MaxHumanIssueCreateBodyBytes && utf8.ValidString(q.Body) && !strings.ContainsRune(q.Body, 0)
}

// CanonicalHumanIssueCreateIntent is shared by dispatch and recovery. Typed
// integer encoding preserves exact native IDs and revision without float casts.
func CanonicalHumanIssueCreateIntent(q HumanIssueCreateRequest) (string, string, error) {
	if !ValidHumanIssueCreateRequest(q) {
		return "", "", ErrHumanIssueCreateQuery
	}
	actor, _ := strconv.ParseInt(q.Expected.ActorID, 10, 64)
	repository, _ := strconv.ParseInt(q.Expected.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string                  `json:"operation_id"`
		ActorID                int64                   `json:"actor_id"`
		RepositoryID           int64                   `json:"repository_id"`
		Kind                   string                  `json:"kind"`
		AuthorizationRevision  string                  `json:"authorization_revision"`
		ExpectedNativeRevision int64                   `json:"expected_native_revision"`
		NotAfter               int64                   `json:"not_after"`
		Payload                HumanIssueCreateRequest `json:"payload"`
	}{q.OperationID, actor, repository, OperationKindHumanIssueCreate, HumanIssueCreateAuthorizationRevision, q.Expected.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

// ValidHumanIssueCreateReceipt binds the allocated result to its saved request.
// The caller must separately authenticate its source and immutable intent.
func ValidHumanIssueCreateReceipt(receipt HumanIssueCreateReceipt, q HumanIssueCreateRequest) bool {
	sum := sha256.Sum256([]byte(q.Body))
	return ValidHumanIssueCreateRequest(q) && strconv.FormatInt(receipt.RepositoryID, 10) == q.Expected.RepositoryID &&
		strconv.FormatInt(receipt.ActorID, 10) == q.Expected.ActorID && receipt.IssueID > 0 && receipt.Index > 0 &&
		receipt.Title == q.Title && receipt.BodyDigest == hex.EncodeToString(sum[:]) && receipt.ContentVersion == 0
}

// HumanIssueCreateOperationID binds the owning application's durable command to
// its actor/repository. Persist it before dispatch; reuse it for lookup/cancel.
func HumanIssueCreateOperationID(actor, repository, command string) (string, error) {
	return humanOperationID(operationcontract.HumanIssueCreatePrefix, actor, repository, command, ErrHumanIssueCreateQuery)
}

func ValidHumanIssueCreateOperationID(id, actor, repository string) bool {
	return validHumanOperationID(operationcontract.HumanIssueCreatePrefix, id, actor, repository)
}

// ValidHumanIssueCreateInstallationID validates a captured equality condition;
// the host derives its actual installation from current admission.
func ValidHumanIssueCreateInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanIssueCreate(ctx context.Context, installation string, q HumanIssueCreateRequest) (OperationRecord, error) {
	if !ValidHumanIssueCreateInstallationID(installation) || !ValidHumanIssueCreateRequest(q) || q.Expected.ActorID != a.Actor.ID || !ValidHumanIssueCreateOperationID(q.OperationID, a.Actor.ID, q.Expected.RepositoryID) {
		return OperationRecord{}, ErrHumanIssueCreateQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanIssueCreateSubmit, RepositoryID: q.Expected.RepositoryID, HumanIssueCreate: &q, IssueCreateInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanIssueCreate.Record, nil
}

// GetHumanIssueCreate returns the live installation even for absence. Consumers
// compare it with the saved namespace before interpreting retained outcomes.
func (a Authority) GetHumanIssueCreate(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanIssueCreateOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanIssueCreateQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanIssueCreateGet, RepositoryID: repository, IssueCreateOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanIssueCreate, nil
}

// CancelHumanIssueCreate asserts the same saved installation as submit, including
// when the cancellation creates a tombstone before any submission.
func (a Authority) CancelHumanIssueCreate(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanIssueCreateInstallationID(installation) || !ValidHumanIssueCreateOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanIssueCreateQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanIssueCreateCancel, RepositoryID: repository, IssueCreateOperationID: operation, IssueCreateInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanIssueCreate.Record, nil
}

func validHumanIssueCreateResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.IssueCreateOperationID
	if request.HumanIssueCreate != nil {
		id = request.HumanIssueCreate.OperationID
	}
	r, valid := validHumanOperationResult(result, request, humanOperationResult{
		operationID: id, installationID: request.IssueCreateInstallationID,
		kind: OperationKindHumanIssueCreate, getOperation: OperationHumanIssueCreateGet,
		strictDigest: false, allowIntervention: true,
	}, request.HumanIssueCreate, CanonicalHumanIssueCreateIntent)
	if !valid || r == nil {
		return valid
	}

	var receipt HumanIssueCreateReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.IssueID < 1 || receipt.Index < 1 || receipt.ContentVersion != 0 ||
		receipt.Title == "" || strings.TrimSpace(receipt.Title) != receipt.Title || len(receipt.Title) > MaxHumanIssueCreateTitleBytes || !utf8.ValidString(receipt.Title) || strings.ContainsRune(receipt.Title, 0) || len(receipt.BodyDigest) != 64 {
		return false
	}
	for _, c := range receipt.BodyDigest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	if q := request.HumanIssueCreate; q != nil {
		return ValidHumanIssueCreateReceipt(receipt, *q)
	}
	return true
}

func humanIssueCreateCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:  ErrHumanIssueCreateQuery,
		admission:   ErrHumanIssueCreateAdmission,
		notFound:    ErrRepositoryNotVisible,
		conflict:    ErrHumanIssueCreateConflict,
		unavailable: ErrHumanIssueCreateUnavailable,
	}).forStatus(status)
}

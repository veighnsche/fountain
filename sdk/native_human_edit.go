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
	OperationKindHumanEdit         = "discussion.edit"
	HumanEditAuthorizationRevision = "native-human-request"
	MaxHumanEditBodyBytes          = 64 << 10
	MaxHumanEditTitleBytes         = 255 // Native storage truncates titles at this byte boundary.
)

const (
	MaxHumanEditCallbackBytes = 512 << 10
	OperationHumanEditSubmit  = "repository.edit.submit"
	OperationHumanEditGet     = "repository.edit.get"
	OperationHumanEditCancel  = "repository.edit.cancel"
)

var (
	ErrHumanEditQuery       = errors.New("invalid human edit request")
	ErrHumanEditAdmission   = errors.New("current human edit admission is required")
	ErrHumanEditConflict    = errors.New("human edit operation conflicts")
	ErrHumanEditUnavailable = errors.New("native human edit service is unavailable")
)

// HumanEditRequest edits only the supplied fields. A nil field is unchanged;
// an explicitly empty body clears the description. The owning application must
// capture the immutable request before dispatch. Expected is not authority.
type HumanEditRequest struct {
	OperationID string                 `json:"operation_id"`
	Expected    RepositoryEditSnapshot `json:"expected"`
	NotAfter    int64                  `json:"not_after"`
	Title       *string                `json:"title,omitempty"`
	Body        *string                `json:"body,omitempty"`
}

type HumanEditReceipt struct {
	RepositoryID   int64  `json:"repository_id"`
	ActorID        int64  `json:"actor_id"`
	Subject        string `json:"subject"`
	SubjectID      int64  `json:"subject_id"`
	IssueID        int64  `json:"issue_id"`
	Index          int64  `json:"index"`
	Title          string `json:"title"`
	BodyDigest     string `json:"body_digest"`
	ContentVersion int64  `json:"content_version"`
	TitleCommentID int64  `json:"title_comment_id"`
}

func ValidHumanEditRequest(q HumanEditRequest) bool {
	p := q.Expected
	if !validOperationID(q.OperationID) || !positivePullID(p.RepositoryID) || !positivePullID(p.ActorID) ||
		!ValidRepositoryEditSnapshotRequest(RepositoryEditSnapshotRequest{Subject: p.Subject, Index: p.Index}) ||
		!validRepositoryEditSnapshot(&p, p.RepositoryID, p.ActorID, RepositoryEditSnapshotRequest{Subject: p.Subject, Index: p.Index}) ||
		p.NativeRevision <= 0 || q.NotAfter <= 0 || q.Title == nil && q.Body == nil || !utf8.ValidString(p.Title) || strings.ContainsRune(p.Title, 0) {
		return false
	}
	if q.Title != nil && (p.TitleAction.State != "allowed" || len(*q.Title) > MaxHumanEditTitleBytes || strings.TrimSpace(*q.Title) == "" || !utf8.ValidString(*q.Title) || strings.ContainsRune(*q.Title, 0)) {
		return false
	}
	return q.Body == nil || p.BodyAction.State == "allowed" && len(*q.Body) <= MaxHumanEditBodyBytes && utf8.ValidString(*q.Body) && !strings.ContainsRune(*q.Body, 0)
}

// CanonicalHumanEditIntent is the sole immutable codec used by native dispatch,
// receipt consumers and offline recovery. It preserves exact strings and integer
// versions; its digest identifies intent and grants no permission.
func CanonicalHumanEditIntent(q HumanEditRequest) (string, string, error) {
	if !ValidHumanEditRequest(q) {
		return "", "", ErrHumanEditQuery
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
		Payload                HumanEditRequest `json:"payload"`
	}{q.OperationID, actor, repository, OperationKindHumanEdit, HumanEditAuthorizationRevision, q.Expected.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

// HumanEditOperationID binds a durable application command to its human and
// repository. Persist the returned ID before dispatch; reuse it for get/cancel.
// Installation is supplied by the live host, never selected by this helper.
func HumanEditOperationID(actor, repository, command string) (string, error) {
	return humanOperationID(operationcontract.HumanEditPrefix, actor, repository, command, ErrHumanEditQuery)
}

func ValidHumanEditOperationID(id, actor, repository string) bool {
	return validHumanOperationID(operationcontract.HumanEditPrefix, id, actor, repository)
}

// ValidHumanEditInstallationID validates the captured namespace condition.
// The host derives its own installation and compares it; this grants no authority.
func ValidHumanEditInstallationID(installation string) bool {
	return validInstallationID(installation)
}

// SubmitHumanEdit asserts the installation captured before dispatch. A lost
// reply does not prove absence: read the saved operation and check its namespace,
// never choose another installation or ID to retry it.
func (a Authority) SubmitHumanEdit(ctx context.Context, installation string, q HumanEditRequest) (OperationRecord, error) {
	if !ValidHumanEditInstallationID(installation) || !ValidHumanEditRequest(q) || q.Expected.ActorID != a.Actor.ID || !ValidHumanEditOperationID(q.OperationID, a.Actor.ID, q.Expected.RepositoryID) {
		return OperationRecord{}, ErrHumanEditQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanEditSubmit, RepositoryID: q.Expected.RepositoryID, HumanEdit: &q, EditInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanEdit.Record, nil
}

// GetHumanEdit returns the live host's installation, including for absence.
// Compare it with the saved namespace before interpreting an old operation's
// outcome: a replacement installation's absence says nothing about the old one.
func (a Authority) GetHumanEdit(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanEditOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanEditQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanEditGet, RepositoryID: repository, EditOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanEdit, nil
}

// CancelHumanEdit uses the same captured installation condition as submit,
// including when cancellation would create a before-submit tombstone.
func (a Authority) CancelHumanEdit(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanEditInstallationID(installation) || !ValidHumanEditOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanEditQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanEditCancel, RepositoryID: repository, EditOperationID: operation, EditInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanEdit.Record, nil
}

func validHumanEditResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.EditOperationID
	if request.HumanEdit != nil {
		id = request.HumanEdit.OperationID
	}
	r, valid := validHumanOperationResult(result, request, humanOperationResult{
		operationID: id, installationID: request.EditInstallationID,
		kind: OperationKindHumanEdit, getOperation: OperationHumanEditGet,
		strictDigest: false, allowIntervention: true,
	}, request.HumanEdit, CanonicalHumanEditIntent)
	if !valid || r == nil {
		return valid
	}

	var receipt HumanEditReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.IssueID < 1 || receipt.SubjectID < 1 || receipt.Index < 1 || receipt.Subject != "issue" && receipt.Subject != "pull" || receipt.Subject == "issue" && receipt.SubjectID != receipt.IssueID || receipt.ContentVersion < 0 || receipt.TitleCommentID < 0 || len(receipt.BodyDigest) != 64 || !utf8.ValidString(receipt.Title) || strings.ContainsRune(receipt.Title, 0) {
		return false
	}
	for _, c := range receipt.BodyDigest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	if q := request.HumanEdit; q != nil {
		return ValidHumanEditReceipt(receipt, *q)
	}
	return true
}

// ValidHumanEditReceipt checks the exact native result against its saved request.
// It authenticates neither the receipt source nor the current human; consumers
// must also check live admission and the operation's immutable intent digest.
func ValidHumanEditReceipt(receipt HumanEditReceipt, q HumanEditRequest) bool {
	if !ValidHumanEditRequest(q) || receipt.ContentVersion < 0 {
		return false
	}
	title, digest, version := q.Expected.Title, q.Expected.BodyDigest, q.Expected.ContentVersion
	titleChanged := q.Title != nil && *q.Title != title
	if q.Title != nil {
		title = *q.Title
	}
	if q.Body != nil {
		sum := sha256.Sum256([]byte(*q.Body))
		next := hex.EncodeToString(sum[:])
		if next != digest {
			version++
		}
		digest = next
	}
	return strconv.FormatInt(receipt.RepositoryID, 10) == q.Expected.RepositoryID && strconv.FormatInt(receipt.ActorID, 10) == q.Expected.ActorID && receipt.Subject == q.Expected.Subject && strconv.FormatInt(receipt.SubjectID, 10) == q.Expected.SubjectID && strconv.FormatInt(receipt.IssueID, 10) == q.Expected.IssueID && strconv.FormatInt(receipt.Index, 10) == q.Expected.Index && receipt.Title == title && receipt.BodyDigest == digest && receipt.ContentVersion == version && (titleChanged && receipt.TitleCommentID > 0 || !titleChanged && receipt.TitleCommentID == 0)
}

func humanEditCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:  ErrHumanEditQuery,
		admission:   ErrHumanEditAdmission,
		notFound:    ErrRepositoryNotVisible,
		conflict:    ErrHumanEditConflict,
		unavailable: ErrHumanEditUnavailable,
	}).forStatus(status)
}

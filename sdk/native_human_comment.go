// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
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

const HumanCommentAuthorizationRevision = "native-human-request"

// CanonicalHumanCommentIntent is the single codec for this native operation's
// immutable intent. Consumers compare its digest before binding an observation
// to their own journal; the digest is NOT a permission or the application's
// captured-command digest. Native dispatch and offline recovery use this codec.
func CanonicalHumanCommentIntent(q HumanCommentRequest) (string, string, error) {
	if !ValidHumanCommentRequest(q) {
		return "", "", ErrHumanCommentQuery
	}
	actor, _ := strconv.ParseInt(q.Expected.ActorID, 10, 64)
	repository, _ := strconv.ParseInt(q.Expected.RepositoryID, 10, 64)
	payload, err := json.Marshal(q)
	if err != nil {
		return "", "", err
	}
	// Preserve the existing native conditional-envelope normalization exactly.
	var normalized any
	if err := json.NewDecoder(bytes.NewReader(payload)).Decode(&normalized); err != nil {
		return "", "", err
	}
	raw, err := json.Marshal(struct {
		OperationID            string `json:"operation_id"`
		ActorID                int64  `json:"actor_id"`
		RepositoryID           int64  `json:"repository_id"`
		Kind                   string `json:"kind"`
		AuthorizationRevision  string `json:"authorization_revision"`
		ExpectedNativeRevision int64  `json:"expected_native_revision"`
		NotAfter               int64  `json:"not_after"`
		Payload                any    `json:"payload"`
	}{q.OperationID, actor, repository, OperationKindHumanComment, HumanCommentAuthorizationRevision, q.Expected.NativeRevision, q.NotAfter, normalized})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

const MaxHumanCommentBytes = 64 << 10
const MaxHumanCommentCallbackBytes = 512 << 10 // Includes JSON escaping and the native envelope.
const OperationKindHumanComment = "discussion.comment.create"
const OperationHumanCommentSubmit = "repository.comment.submit"
const OperationHumanCommentGet = "repository.comment.get"
const OperationHumanCommentCancel = "repository.comment.cancel"

var (
	ErrHumanCommentQuery       = errors.New("invalid human comment request")
	ErrHumanCommentAdmission   = errors.New("current human comment admission is required")
	ErrHumanCommentConflict    = errors.New("human comment operation conflicts")
	ErrHumanCommentUnavailable = errors.New("native human comment service is unavailable")
)

// HumanCommentRequest is immutable after the owning application journals it.
// Expected is a condition, never admission. No attachment/edit/delete is implied.
type HumanCommentRequest struct {
	OperationID string                    `json:"operation_id"`
	Expected    RepositoryCommentSnapshot `json:"expected"`
	NotAfter    int64                     `json:"not_after"`
	Body        string                    `json:"body"`
}

type HumanCommentReceipt struct {
	RepositoryID int64  `json:"repository_id"`
	ActorID      int64  `json:"actor_id"`
	Subject      string `json:"subject"`
	SubjectID    int64  `json:"subject_id"`
	IssueID      int64  `json:"issue_id"`
	Index        int64  `json:"index"`
	CommentID    int64  `json:"comment_id"`
}

func ValidHumanCommentRequest(q HumanCommentRequest) bool {
	p := q.Expected
	return validOperationID(q.OperationID) && positivePullID(p.RepositoryID) && positivePullID(p.ActorID) &&
		validRepositoryCommentSnapshot(&p, p.RepositoryID, p.ActorID, RepositoryCommentSnapshotRequest{Subject: p.Subject, Index: p.Index}) &&
		ValidRepositoryCommentSnapshotRequest(RepositoryCommentSnapshotRequest{Subject: p.Subject, Index: p.Index}) &&
		p.NativeRevision > 0 && p.Action.State == "allowed" && q.NotAfter > 0 && len(q.Body) <= MaxHumanCommentBytes &&
		utf8.ValidString(q.Body) && !strings.ContainsRune(q.Body, 0) && strings.TrimSpace(q.Body) != ""
}

// HumanCommentOperationID binds a durable application command to its human and
// repository. Persist the returned ID before dispatch; reuse it for get/cancel.
// Installation is supplied by the live host, never selected by this helper.
func HumanCommentOperationID(actor, repository, command string) (string, error) {
	return humanOperationID(operationcontract.HumanCommentPrefix, actor, repository, command, ErrHumanCommentQuery)
}

func ValidHumanCommentOperationID(id, actor, repository string) bool {
	return validHumanOperationID(operationcontract.HumanCommentPrefix, id, actor, repository)
}

// ValidHumanCommentInstallationID validates the captured namespace condition.
// The host derives its own installation and compares it; this grants no authority.
func ValidHumanCommentInstallationID(installation string) bool {
	return validInstallationID(installation)
}

// SubmitHumanComment asserts the installation captured before dispatch. A lost
// reply does not prove absence: read the saved operation and check its namespace,
// never choose another installation or ID to retry it.
func (a Authority) SubmitHumanComment(ctx context.Context, installation string, q HumanCommentRequest) (OperationRecord, error) {
	if !ValidHumanCommentInstallationID(installation) || !ValidHumanCommentRequest(q) || q.Expected.ActorID != a.Actor.ID || !ValidHumanCommentOperationID(q.OperationID, a.Actor.ID, q.Expected.RepositoryID) {
		return OperationRecord{}, ErrHumanCommentQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanCommentSubmit, RepositoryID: q.Expected.RepositoryID, HumanComment: &q, CommentInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanComment.Record, nil
}

// GetHumanComment returns the live host's installation, including for absence.
// Compare it with the saved namespace before interpreting an old operation's
// outcome: a replacement installation's absence says nothing about the old one.
func (a Authority) GetHumanComment(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanCommentOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanCommentQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanCommentGet, RepositoryID: repository, CommentOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanComment, nil
}

// CancelHumanComment uses the same captured installation condition as submit,
// including when cancellation would create a before-submit tombstone.
func (a Authority) CancelHumanComment(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanCommentInstallationID(installation) || !ValidHumanCommentOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanCommentQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanCommentCancel, RepositoryID: repository, CommentOperationID: operation, CommentInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanComment.Record, nil
}

func validHumanCommentResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.CommentOperationID
	if request.HumanComment != nil {
		id = request.HumanComment.OperationID
	}
	r, valid := validHumanOperationResult(result, request, humanOperationResult{
		operationID: id, installationID: request.CommentInstallationID,
		kind: OperationKindHumanComment, getOperation: OperationHumanCommentGet,
		strictDigest: false, allowIntervention: true,
	}, request.HumanComment, CanonicalHumanCommentIntent)
	if !valid || r == nil {
		return valid
	}

	var receipt HumanCommentReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.IssueID < 1 || receipt.SubjectID < 1 || receipt.Index < 1 || receipt.CommentID < 1 || receipt.Subject != "issue" && receipt.Subject != "pull" || receipt.Subject == "issue" && receipt.SubjectID != receipt.IssueID {
		return false
	}
	if q := request.HumanComment; q != nil {
		return receipt.Subject == q.Expected.Subject && strconv.FormatInt(receipt.SubjectID, 10) == q.Expected.SubjectID && strconv.FormatInt(receipt.IssueID, 10) == q.Expected.IssueID && strconv.FormatInt(receipt.Index, 10) == q.Expected.Index
	}
	return true
}

func humanCommentCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:  ErrHumanCommentQuery,
		admission:   ErrHumanCommentAdmission,
		notFound:    ErrRepositoryNotVisible,
		conflict:    ErrHumanCommentConflict,
		unavailable: ErrHumanCommentUnavailable,
	}).forStatus(status)
}

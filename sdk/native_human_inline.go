// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"forgejo.org/extension-sdk/operationcontract"
)

const OperationKindHumanInline = "pull_request.human_inline.create"
const HumanInlineAuthorizationRevision = "native-human-request"
const MaxHumanInlineBodyBytes = 64 << 10
const MaxHumanInlineCallbackBytes = 512 << 10
const OperationHumanInlineSubmit = "repository.human_inline.submit"
const OperationHumanInlineGet = "repository.human_inline.get"
const OperationHumanInlineCancel = "repository.human_inline.cancel"

var ErrHumanInlineQuery = errors.New("invalid human inline request")
var ErrHumanInlineAdmission = errors.New("current human inline admission is required")
var ErrHumanInlineConflict = errors.New("human inline operation conflicts")
var ErrHumanInlineUnavailable = errors.New("native human inline service is unavailable")

// HumanInlineRequest records a fresh inline selection and its current human
// preparation. Expected is evidence, never authority. Persist this command and
// the admitted installation before submitting; reuse them for uncertain replies.
type HumanInlineRequest struct {
	OperationID string                   `json:"operation_id"`
	Expected    RepositoryInlineSnapshot `json:"expected"`
	NotAfter    int64                    `json:"not_after"`
	Draft       bool                     `json:"draft"`
	Body        string                   `json:"body"`
}

type HumanInlineReceipt struct {
	RepositoryID   int64                  `json:"repository_id"`
	ActorID        int64                  `json:"actor_id"`
	PullID         int64                  `json:"pull_id"`
	IssueID        int64                  `json:"issue_id"`
	Index          int64                  `json:"index"`
	ReviewID       int64                  `json:"review_id"`
	CommentID      int64                  `json:"comment_id"`
	HistoryID      int64                  `json:"history_id"`
	Draft          bool                   `json:"draft"`
	CommitID       string                 `json:"commit_id"`
	Official       bool                   `json:"official"`
	Stale          bool                   `json:"stale"`
	BodyDigest     string                 `json:"body_digest"`
	ReviewDigest   string                 `json:"review_digest"`
	Anchor         RepositoryInlineAnchor `json:"anchor"`
	EntryOID       string                 `json:"entry_oid"`
	EntryKind      string                 `json:"entry_kind"`
	DiffDigest     string                 `json:"diff_digest"`
	CommentsCount  int64                  `json:"comments_count"`
	CommentsDigest string                 `json:"comments_digest"`
}

func ValidHumanInlineRequest(q HumanInlineRequest) bool {
	p := q.Expected
	r := p.Review
	if !validOperationID(q.OperationID) || !validRepositoryInlineSnapshot(&p, r.RepositoryID, r.ActorID, RepositoryInlineSnapshotRequest{Index: r.Index, Anchor: p.Anchor}) || q.NotAfter <= 0 || len(q.Body) > MaxHumanInlineBodyBytes || !utf8.ValidString(q.Body) || strings.ContainsRune(q.Body, 0) || strings.TrimSpace(q.Body) == "" {
		return false
	}
	if q.Draft {
		return r.DraftAction.State == "allowed"
	}
	return r.CommentAction.State == "allowed"
}

func CanonicalHumanInlineIntent(q HumanInlineRequest) (string, string, error) {
	if !ValidHumanInlineRequest(q) {
		return "", "", ErrHumanInlineQuery
	}
	p := q.Expected.Review
	actor, _ := strconv.ParseInt(p.ActorID, 10, 64)
	repository, _ := strconv.ParseInt(p.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string             `json:"operation_id"`
		ActorID                int64              `json:"actor_id"`
		RepositoryID           int64              `json:"repository_id"`
		Kind                   string             `json:"kind"`
		AuthorizationRevision  string             `json:"authorization_revision"`
		ExpectedNativeRevision int64              `json:"expected_native_revision"`
		NotAfter               int64              `json:"not_after"`
		Payload                HumanInlineRequest `json:"payload"`
	}{q.OperationID, actor, repository, OperationKindHumanInline, HumanInlineAuthorizationRevision, p.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	return string(raw), HumanReviewBodyDigest(string(raw)), nil
}

func ValidHumanInlineReceipt(r HumanInlineReceipt, q HumanInlineRequest) bool {
	p := q.Expected.Review
	if !ValidHumanInlineRequest(q) || strconv.FormatInt(r.RepositoryID, 10) != p.RepositoryID || strconv.FormatInt(r.ActorID, 10) != p.ActorID || strconv.FormatInt(r.PullID, 10) != p.PullID || strconv.FormatInt(r.IssueID, 10) != p.IssueID || strconv.FormatInt(r.Index, 10) != p.Index || r.ReviewID <= 0 || r.CommentID <= 0 || r.Draft != q.Draft || r.Official || r.BodyDigest != HumanReviewBodyDigest(q.Body) || !validHumanReviewDigest(r.ReviewDigest) || r.Anchor != q.Expected.Anchor || r.EntryOID != q.Expected.EntryOID || r.EntryKind != q.Expected.EntryKind || r.DiffDigest != q.Expected.DiffDigest || r.CommentsCount < 1 || !validHumanReviewDigest(r.CommentsDigest) {
		return false
	}
	if p.PendingReviewID != "" && strconv.FormatInt(r.ReviewID, 10) != p.PendingReviewID {
		return false
	}
	if p.PendingReviewID == "" && (r.CommentsCount != 1 || r.ReviewDigest != HumanReviewBodyDigest("")) {
		return false
	}
	commit := p.HeadOID
	if q.Draft {
		if r.HistoryID != 0 {
			return false
		}
		if p.PendingReviewID != "" {
			commit = p.PendingCommitID
		}
	} else if r.HistoryID <= 0 || r.HistoryID == r.CommentID || r.Stale {
		return false
	}
	return r.CommitID == commit
}

// HumanInlineOperationID binds the owning application's durable command to
// its actor/repository. Persist it before dispatch; reuse it for lookup/cancel.
func HumanInlineOperationID(actor, repository, command string) (string, error) {
	return humanOperationID(operationcontract.HumanInlinePrefix, actor, repository, command, ErrHumanInlineQuery)
}

func ValidHumanInlineOperationID(id, actor, repository string) bool {
	return validHumanOperationID(operationcontract.HumanInlinePrefix, id, actor, repository)
}

// ValidHumanInlineInstallationID validates a captured equality condition;
// the host derives its actual installation from current admission.
func ValidHumanInlineInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanInline(ctx context.Context, installation string, q HumanInlineRequest) (OperationRecord, error) {
	if !ValidHumanInlineInstallationID(installation) || !ValidHumanInlineRequest(q) || q.Expected.Review.ActorID != a.Actor.ID || !ValidHumanInlineOperationID(q.OperationID, a.Actor.ID, q.Expected.Review.RepositoryID) {
		return OperationRecord{}, ErrHumanInlineQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanInlineSubmit, RepositoryID: q.Expected.Review.RepositoryID, HumanInline: &q, InlineInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanInline.Record, nil
}

// GetHumanInline returns the live installation even for absence. Consumers
// compare it with the saved namespace before interpreting retained outcomes.
func (a Authority) GetHumanInline(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanInlineOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanInlineQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanInlineGet, RepositoryID: repository, InlineOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanInline, nil
}

// CancelHumanInline asserts the same saved installation as submit, including
// when the cancellation creates a tombstone before any submission.
func (a Authority) CancelHumanInline(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanInlineInstallationID(installation) || !ValidHumanInlineOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanInlineQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanInlineCancel, RepositoryID: repository, InlineOperationID: operation, InlineInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanInline.Record, nil
}

func validHumanInlineResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.InlineOperationID
	if request.HumanInline != nil {
		id = request.HumanInline.OperationID
	}
	r, valid := validHumanOperationResult(result, request, humanOperationResult{
		operationID: id, installationID: request.InlineInstallationID,
		kind: OperationKindHumanInline, getOperation: OperationHumanInlineGet,
		strictDigest: true, allowIntervention: true,
	}, request.HumanInline, CanonicalHumanInlineIntent)
	if !valid || r == nil {
		return valid
	}

	var receipt HumanInlineReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.PullID < 1 || receipt.IssueID < 1 || receipt.Index < 1 || receipt.ReviewID < 1 || receipt.CommentID < 1 || receipt.Official || !validHumanReviewDigest(receipt.BodyDigest) || !validHumanReviewDigest(receipt.ReviewDigest) || !ValidRepositoryInlineAnchor(receipt.Anchor) || !ValidPullDiffCommit(receipt.EntryOID) || receipt.EntryKind != "blob" && (receipt.EntryKind != "commit" || receipt.Anchor.Scope != "file") || !validHumanReviewDigest(receipt.DiffDigest) || receipt.CommentsCount < 1 || !validHumanReviewDigest(receipt.CommentsDigest) {
		return false
	}
	if receipt.Draft {
		if receipt.HistoryID != 0 || receipt.CommitID != "" && !ValidPullDiffCommit(receipt.CommitID) {
			return false
		}
	} else if receipt.HistoryID <= 0 || receipt.HistoryID == receipt.CommentID || receipt.Stale || !ValidPullDiffCommit(receipt.CommitID) {
		return false
	}
	if q := request.HumanInline; q != nil {
		return ValidHumanInlineReceipt(receipt, *q)
	}
	return true
}

func humanInlineCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:  ErrHumanInlineQuery,
		admission:   ErrHumanInlineAdmission,
		notFound:    ErrRepositoryNotVisible,
		conflict:    ErrHumanInlineConflict,
		unavailable: ErrHumanInlineUnavailable,
	}).forStatus(status)
}

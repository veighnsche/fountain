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

const OperationKindHumanReview = "pull_request.human_review.apply"
const HumanReviewAuthorizationRevision = "native-human-request"
const HumanReviewEventDraft = "PENDING"
const HumanReviewEventComment = "COMMENT"
const MaxHumanReviewBodyBytes = 64 << 10

const OperationHumanReviewSubmit = "repository.human_review.submit"
const OperationHumanReviewGet = "repository.human_review.get"
const OperationHumanReviewCancel = "repository.human_review.cancel"
const MaxHumanReviewCallbackBytes = 512 << 10

var (
	ErrHumanReviewQuery       = errors.New("invalid human review request")
	ErrHumanReviewAdmission   = errors.New("current human review admission is required")
	ErrHumanReviewConflict    = errors.New("human review operation conflicts")
	ErrHumanReviewUnavailable = errors.New("native human review service is unavailable")
)

// HumanReviewRequest captures one draft-body or final-review action. Existing
// own draft comments are consumed with their original context. New inline
// creation is a separate operation. Expected is preparation, never authority.
type HumanReviewRequest struct {
	OperationID string                   `json:"operation_id"`
	Expected    RepositoryReviewSnapshot `json:"expected"`
	NotAfter    int64                    `json:"not_after"`
	Event       string                   `json:"event"`
	Body        string                   `json:"body"`
}

type HumanReviewReceipt struct {
	RepositoryID        int64  `json:"repository_id"`
	ActorID             int64  `json:"actor_id"`
	PullID              int64  `json:"pull_id"`
	IssueID             int64  `json:"issue_id"`
	Index               int64  `json:"index"`
	ReviewID            int64  `json:"review_id"`
	CommentID           int64  `json:"comment_id"`
	Event               string `json:"event"`
	CommitID            string `json:"commit_id"`
	Official            bool   `json:"official"`
	Stale               bool   `json:"stale"`
	BodyDigest          string `json:"body_digest"`
	DraftCommentsCount  int64  `json:"draft_comments_count"`
	DraftCommentsDigest string `json:"draft_comments_digest"`
}

func HumanReviewBodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func ValidHumanReviewRequest(q HumanReviewRequest) bool {
	p := q.Expected
	if !validOperationID(q.OperationID) || !validRepositoryReviewSnapshot(&p, p.RepositoryID, p.ActorID, RepositoryReviewSnapshotRequest{Index: p.Index}) || q.NotAfter <= 0 || len(q.Body) > MaxHumanReviewBodyBytes || !utf8.ValidString(q.Body) || strings.ContainsRune(q.Body, 0) {
		return false
	}
	action := p.CommentAction
	switch q.Event {
	case HumanReviewEventDraft:
		action = p.DraftAction
		if p.PendingReviewID == "" && strings.TrimSpace(q.Body) == "" {
			return false
		}
	case HumanReviewEventComment:
		if p.PendingReviewID == "" && strings.TrimSpace(q.Body) == "" {
			return false
		}
	case ReviewSubmitEventApproved:
		action = p.ApproveAction
	case ReviewSubmitEventRequestChanges:
		action = p.RequestChangesAction
		if strings.TrimSpace(q.Body) == "" {
			return false
		}
	default:
		return false
	}
	return action.State == "allowed"
}

// CanonicalHumanReviewIntent is shared by dispatch, receipt consumers and
// offline recovery; exact prose and integer identity/revision survive encoding.
func CanonicalHumanReviewIntent(q HumanReviewRequest) (string, string, error) {
	if !ValidHumanReviewRequest(q) {
		return "", "", ErrHumanReviewQuery
	}
	actor, _ := strconv.ParseInt(q.Expected.ActorID, 10, 64)
	repository, _ := strconv.ParseInt(q.Expected.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string             `json:"operation_id"`
		ActorID                int64              `json:"actor_id"`
		RepositoryID           int64              `json:"repository_id"`
		Kind                   string             `json:"kind"`
		AuthorizationRevision  string             `json:"authorization_revision"`
		ExpectedNativeRevision int64              `json:"expected_native_revision"`
		NotAfter               int64              `json:"not_after"`
		Payload                HumanReviewRequest `json:"payload"`
	}{q.OperationID, actor, repository, OperationKindHumanReview, HumanReviewAuthorizationRevision, q.Expected.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	return string(raw), HumanReviewBodyDigest(string(raw)), nil
}

func validHumanReviewDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, b := range s {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

func ValidHumanReviewReceipt(r HumanReviewReceipt, q HumanReviewRequest) bool {
	p := q.Expected
	if !ValidHumanReviewRequest(q) || strconv.FormatInt(r.RepositoryID, 10) != p.RepositoryID || strconv.FormatInt(r.ActorID, 10) != p.ActorID || strconv.FormatInt(r.PullID, 10) != p.PullID || strconv.FormatInt(r.IssueID, 10) != p.IssueID || strconv.FormatInt(r.Index, 10) != p.Index || r.ReviewID <= 0 || r.Event != q.Event || r.BodyDigest != HumanReviewBodyDigest(q.Body) || r.DraftCommentsCount < 0 || !validHumanReviewDigest(r.DraftCommentsDigest) {
		return false
	}
	if p.PendingReviewID != "" && strconv.FormatInt(r.ReviewID, 10) != p.PendingReviewID {
		return false
	}
	if p.PendingReviewID == "" && r.DraftCommentsCount != 0 || r.DraftCommentsCount == 0 && r.DraftCommentsDigest != HumanReviewBodyDigest("[]") {
		return false
	}
	commit := p.HeadOID
	if q.Event == HumanReviewEventDraft {
		if r.CommentID != 0 || r.Official {
			return false
		}
		if p.PendingReviewID != "" {
			commit = p.PendingCommitID
		}
	} else if r.CommentID <= 0 || r.Stale {
		return false
	}
	return r.CommitID == commit
}

// HumanReviewOperationID binds the owning application's durable command to
// its actor/repository. Persist it before dispatch; reuse it for lookup/cancel.
func HumanReviewOperationID(actor, repository, command string) (string, error) {
	return humanOperationID(operationcontract.HumanReviewPrefix, actor, repository, command, ErrHumanReviewQuery)
}

func ValidHumanReviewOperationID(id, actor, repository string) bool {
	return validHumanOperationID(operationcontract.HumanReviewPrefix, id, actor, repository)
}

// ValidHumanReviewInstallationID validates a captured equality condition;
// the host derives its actual installation from current admission.
func ValidHumanReviewInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanReview(ctx context.Context, installation string, q HumanReviewRequest) (OperationRecord, error) {
	if !ValidHumanReviewInstallationID(installation) || !ValidHumanReviewRequest(q) || q.Expected.ActorID != a.Actor.ID || !ValidHumanReviewOperationID(q.OperationID, a.Actor.ID, q.Expected.RepositoryID) {
		return OperationRecord{}, ErrHumanReviewQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanReviewSubmit, RepositoryID: q.Expected.RepositoryID, HumanReview: &q, ReviewInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanReview.Record, nil
}

// GetHumanReview returns the live installation even for absence. Consumers
// compare it with the saved namespace before interpreting retained outcomes.
func (a Authority) GetHumanReview(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanReviewOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanReviewQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanReviewGet, RepositoryID: repository, ReviewOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanReview, nil
}

// CancelHumanReview asserts the same saved installation as submit, including
// when the cancellation creates a tombstone before any submission.
func (a Authority) CancelHumanReview(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanReviewInstallationID(installation) || !ValidHumanReviewOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanReviewQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanReviewCancel, RepositoryID: repository, ReviewOperationID: operation, ReviewInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanReview.Record, nil
}

func validHumanReviewResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.ReviewOperationID
	if request.HumanReview != nil {
		id = request.HumanReview.OperationID
	}
	r, valid := validHumanOperationResult(result, request, humanOperationResult{
		operationID: id, installationID: request.ReviewInstallationID,
		kind: OperationKindHumanReview, getOperation: OperationHumanReviewGet,
		strictDigest: false, allowIntervention: true,
	}, request.HumanReview, CanonicalHumanReviewIntent)
	if !valid || r == nil {
		return valid
	}

	var receipt HumanReviewReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.PullID < 1 || receipt.IssueID < 1 || receipt.Index < 1 || receipt.ReviewID < 1 || !ValidPullDiffCommit(receipt.CommitID) || !validHumanReviewDigest(receipt.BodyDigest) || receipt.DraftCommentsCount < 0 || !validHumanReviewDigest(receipt.DraftCommentsDigest) || receipt.DraftCommentsCount == 0 && receipt.DraftCommentsDigest != HumanReviewBodyDigest("[]") {
		return false
	}
	switch receipt.Event {
	case HumanReviewEventDraft:
		if receipt.CommentID != 0 || receipt.Official {
			return false
		}
	case HumanReviewEventComment, ReviewSubmitEventApproved, ReviewSubmitEventRequestChanges:
		if receipt.CommentID <= 0 || receipt.Stale {
			return false
		}
	default:
		return false
	}
	if q := request.HumanReview; q != nil {
		return ValidHumanReviewReceipt(receipt, *q)
	}
	return true
}

func humanReviewCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:  ErrHumanReviewQuery,
		admission:   ErrHumanReviewAdmission,
		notFound:    ErrRepositoryNotVisible,
		conflict:    ErrHumanReviewConflict,
		unavailable: ErrHumanReviewUnavailable,
	}).forStatus(status)
}

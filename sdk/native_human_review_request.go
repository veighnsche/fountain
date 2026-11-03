// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
)

const OperationKindHumanReviewRequest = "pull_request.human_review_request.apply"
const HumanReviewRequestAuthorizationRevision = "native-human-request"

const OperationHumanReviewRequestSubmit = "repository.human_review_request.submit"
const OperationHumanReviewRequestGet = "repository.human_review_request.get"
const OperationHumanReviewRequestCancel = "repository.human_review_request.cancel"

var ErrHumanReviewRequestQuery = errors.New("invalid human reviewer request")
var ErrHumanReviewRequestAdmission = errors.New("current human reviewer admission is required")
var ErrHumanReviewRequestConflict = errors.New("human reviewer operation conflicts")
var ErrHumanReviewRequestUnavailable = errors.New("native human reviewer service is unavailable")

// HumanReviewRequestChange is an exact captured selection and desired state,
// never authority. Persist it before dispatch and reuse it unchanged on replay.
type HumanReviewRequestChange struct {
	OperationID string                          `json:"operation_id"`
	Expected    RepositoryReviewRequestSnapshot `json:"expected"`
	NotAfter    int64                           `json:"not_after"`
	Requested   bool                            `json:"requested"`
}

type HumanReviewRequestReceipt struct {
	RepositoryID        int64                      `json:"repository_id"`
	ActorID             int64                      `json:"actor_id"`
	PullID              int64                      `json:"pull_id"`
	IssueID             int64                      `json:"issue_id"`
	Index               int64                      `json:"index"`
	ReviewerKind        string                     `json:"reviewer_kind"`
	ReviewerID          int64                      `json:"reviewer_id"`
	Before              HumanReviewRequestEvidence `json:"before"`
	After               HumanReviewRequestEvidence `json:"after"`
	Changed             bool                       `json:"changed"`
	HistoryIDs          []string                   `json:"history_ids"`
	HistoryDigest       string                     `json:"history_digest"`
	ReviewsBeforeDigest string                     `json:"reviews_before_digest"`
	ReviewsAfterDigest  string                     `json:"reviews_after_digest"`
}

func ValidHumanReviewRequestChange(q HumanReviewRequestChange) bool {
	p := q.Expected
	if !validOperationID(q.OperationID) || q.NotAfter <= 0 || !ValidRepositoryReviewRequestSnapshot(&p, p.Review.RepositoryID, p.Review.ActorID, RepositoryReviewRequestSnapshotRequest{Index: p.Review.Index, ReviewerKind: p.ReviewerKind, ReviewerID: p.ReviewerID}) {
		return false
	}
	if q.Requested {
		return p.AddAction.State == "allowed"
	}
	return p.RemoveAction.State == "allowed"
}

// The native writer, receipt consumers and offline recovery share this exact
// typed envelope. Native numeric IDs/revision never pass through float values.
func CanonicalHumanReviewRequestIntent(q HumanReviewRequestChange) (string, string, error) {
	if !ValidHumanReviewRequestChange(q) {
		return "", "", ErrHumanReviewRequestQuery
	}
	p := q.Expected.Review
	actor, _ := strconv.ParseInt(p.ActorID, 10, 64)
	repo, _ := strconv.ParseInt(p.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string                   `json:"operation_id"`
		ActorID                int64                    `json:"actor_id"`
		RepositoryID           int64                    `json:"repository_id"`
		Kind                   string                   `json:"kind"`
		AuthorizationRevision  string                   `json:"authorization_revision"`
		ExpectedNativeRevision int64                    `json:"expected_native_revision"`
		NotAfter               int64                    `json:"not_after"`
		Payload                HumanReviewRequestChange `json:"payload"`
	}{q.OperationID, actor, repo, OperationKindHumanReviewRequest, HumanReviewRequestAuthorizationRevision, p.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	return string(raw), HumanReviewBodyDigest(string(raw)), nil
}

// Structural binding does not replace native source trust. The atomic primary
// proves exact persisted request/history rows and historical official-flag effects.
func ValidHumanReviewRequestReceipt(r HumanReviewRequestReceipt, q HumanReviewRequestChange) bool {
	p := q.Expected
	if !ValidHumanReviewRequestChange(q) || strconv.FormatInt(r.RepositoryID, 10) != p.Review.RepositoryID || strconv.FormatInt(r.ActorID, 10) != p.Review.ActorID || strconv.FormatInt(r.PullID, 10) != p.Review.PullID || strconv.FormatInt(r.IssueID, 10) != p.Review.IssueID || strconv.FormatInt(r.Index, 10) != p.Review.Index || r.ReviewerKind != p.ReviewerKind || strconv.FormatInt(r.ReviewerID, 10) != p.ReviewerID || !reflect.DeepEqual(r.Before, p.Requests) || !ValidHumanReviewRequestEvidence(r.After) {
		return false
	}
	return validHumanReviewRequestTransition(r, q.Requested)
}

func validHumanReviewRequestTransition(r HumanReviewRequestReceipt, requested bool) bool {
	if r.RepositoryID < 1 || r.ActorID < 1 || r.PullID < 1 || r.IssueID < 1 || r.Index < 1 || r.ReviewerID < 1 || r.ReviewerKind != "user" && r.ReviewerKind != "team" || !ValidHumanReviewRequestEvidence(r.Before) || !ValidHumanReviewRequestEvidence(r.After) {
		return false
	}
	if requested != (len(r.After.RequestIDs) > 0) || r.Changed != (requested != (len(r.Before.RequestIDs) > 0)) || !validHumanReviewDigest(r.ReviewsBeforeDigest) || !validHumanReviewDigest(r.ReviewsAfterDigest) || !ValidHumanReviewRequestEvidence(HumanReviewRequestEvidence{RequestIDs: r.HistoryIDs, Digest: r.HistoryDigest}) {
		return false
	}
	empty := HumanReviewBodyDigest("[]")
	if len(r.After.RequestIDs) == 0 && r.After.Digest != empty {
		return false
	}
	if !r.Changed {
		return reflect.DeepEqual(r.Before, r.After) && r.ReviewsBeforeDigest == r.ReviewsAfterDigest && len(r.HistoryIDs) == 0 && r.HistoryDigest == empty
	}
	if r.ReviewsBeforeDigest == r.ReviewsAfterDigest || r.Before.Digest == r.After.Digest {
		return false
	}
	if requested {
		return len(r.After.RequestIDs) == 1 && len(r.HistoryIDs) == 1
	}
	return len(r.HistoryIDs) == len(r.Before.RequestIDs)
}

// HumanReviewRequestOperationID binds the owning application's durable command to
// its actor/repository. Persist it before dispatch; reuse it for lookup/cancel.
func HumanReviewRequestOperationID(actor, repository, command string) (string, error) {
	if !positivePullID(actor) || !positivePullID(repository) || !validOperationID(command) {
		return "", ErrHumanReviewRequestQuery
	}
	sum := sha256.Sum256([]byte(command))
	return "human-review-request:" + actor + ":" + repository + ":" + hex.EncodeToString(sum[:]), nil
}

func ValidHumanReviewRequestOperationID(id, actor, repository string) bool {
	if !positivePullID(actor) || !positivePullID(repository) {
		return false
	}
	prefix := "human-review-request:" + actor + ":" + repository + ":"
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+64 {
		return false
	}
	for _, b := range []byte(strings.TrimPrefix(id, prefix)) {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return validOperationID(id)
}

// ValidHumanReviewRequestInstallationID validates a captured equality condition;
// the host derives its actual installation from current admission.
func ValidHumanReviewRequestInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanReviewRequest(ctx context.Context, installation string, q HumanReviewRequestChange) (OperationRecord, error) {
	if !ValidHumanReviewRequestInstallationID(installation) || !ValidHumanReviewRequestChange(q) || q.Expected.Review.ActorID != a.Actor.ID || !ValidHumanReviewRequestOperationID(q.OperationID, a.Actor.ID, q.Expected.Review.RepositoryID) {
		return OperationRecord{}, ErrHumanReviewRequestQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanReviewRequestSubmit, RepositoryID: q.Expected.Review.RepositoryID, HumanReviewRequest: &q, ReviewRequestInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanReviewRequest.Record, nil
}

// GetHumanReviewRequest returns the live installation even for absence. Consumers
// compare it with the saved namespace before interpreting retained outcomes.
func (a Authority) GetHumanReviewRequest(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanReviewRequestOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanReviewRequestQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanReviewRequestGet, RepositoryID: repository, ReviewRequestOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanReviewRequest, nil
}

// CancelHumanReviewRequest asserts the same saved installation as submit, including
// when the cancellation creates a tombstone before any submission.
func (a Authority) CancelHumanReviewRequest(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanReviewRequestInstallationID(installation) || !ValidHumanReviewRequestOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanReviewRequestQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanReviewRequestCancel, RepositoryID: repository, ReviewRequestOperationID: operation, ReviewRequestInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanReviewRequest.Record, nil
}

func validHumanReviewRequestResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.ReviewRequestOperationID
	if request.HumanReviewRequest != nil {
		id = request.HumanReviewRequest.OperationID
	}
	if result == nil || !validInstallationID(result.InstallationID) || result.OperationID != id || request.Operation != OperationHumanReviewRequestGet && result.InstallationID != request.ReviewRequestInstallationID {
		return false
	}
	if result.Status == BackgroundOutcomeNotObserved {
		return request.Operation == OperationHumanReviewRequestGet && result.Record == nil
	}
	r := result.Record
	if r == nil || r.InstallationID != result.InstallationID || r.OperationID != id || r.Outcome != r.EffectState || r.EffectState != result.Status {
		return false
	}
	switch r.EffectState {
	case EffectPending, EffectNotCommitted, EffectCommitted, EffectIndeterminate:
	default:
		return false
	}
	switch r.CancellationStatus {
	case CancellationNone, CancellationPending, CancellationCancelled, CancellationTooLate, CancellationIndeterminate:
	default:
		return false
	}
	if r.Kind == "" {
		return r.ActorID == "" && r.RepositoryID == "" && r.IntentDigest == "" && len(r.Receipt) == 0 && r.CompletionState == "" && r.EffectState == EffectNotCommitted && r.CancellationStatus == CancellationCancelled
	}
	if r.Kind != OperationKindHumanReviewRequest || r.ActorID != request.Authority.Actor.ID || r.RepositoryID != request.RepositoryID || !validHumanReviewDigest(r.IntentDigest) {
		return false
	}
	if q := request.HumanReviewRequest; q != nil {
		_, digest, err := CanonicalHumanReviewRequestIntent(*q)
		if err != nil || digest != r.IntentDigest {
			return false
		}
	}
	if r.EffectState != EffectCommitted {
		return len(r.Receipt) == 0 && r.CompletionState == ""
	}
	if r.CompletionState != CompletionPending && r.CompletionState != CompletionComplete && r.CompletionState != CompletionNeedsIntervention {
		return false
	}
	var receipt HumanReviewRequestReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.PullID < 1 || receipt.IssueID < 1 || receipt.Index < 1 || !validHumanReviewRequestTransition(receipt, len(receipt.After.RequestIDs) > 0) {
		return false
	}
	if q := request.HumanReviewRequest; q != nil {
		return ValidHumanReviewRequestReceipt(receipt, *q)
	}
	return true
}

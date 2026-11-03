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
)

const OperationKindHumanState = "discussion.state.apply"
const HumanStateAuthorizationRevision = "native-human-request"

const OperationHumanStateSubmit = "repository.state.submit"
const OperationHumanStateGet = "repository.state.get"
const OperationHumanStateCancel = "repository.state.cancel"

var (
	ErrHumanStateQuery       = errors.New("invalid human state request")
	ErrHumanStateAdmission   = errors.New("current human state admission is required")
	ErrHumanStateConflict    = errors.New("human state operation conflicts")
	ErrHumanStateUnavailable = errors.New("native human state service is unavailable")
)

// HumanStateRequest prepares a direct issue/PR close/reopen transition. It is
// not authority and does not combine comment posting or PR ref synchronization.
type HumanStateRequest struct {
	OperationID string                  `json:"operation_id"`
	Expected    RepositoryStateSnapshot `json:"expected"`
	NotAfter    int64                   `json:"not_after"`
	Close       bool                    `json:"close"`
}

type HumanStateReceipt struct {
	RepositoryID int64  `json:"repository_id"`
	ActorID      int64  `json:"actor_id"`
	Subject      string `json:"subject"`
	SubjectID    int64  `json:"subject_id"`
	IssueID      int64  `json:"issue_id"`
	Index        int64  `json:"index"`
	IsClosed     bool   `json:"is_closed"`
	ClosedUnix   int64  `json:"closed_unix"`
	CommentID    int64  `json:"comment_id"`
}

func ValidHumanStateRequest(q HumanStateRequest) bool {
	p := q.Expected
	if !validOperationID(q.OperationID) || !validRepositoryStateSnapshot(&p, p.RepositoryID, p.ActorID, RepositoryStateSnapshotRequest{Subject: p.Subject, Index: p.Index}) ||
		q.NotAfter <= 0 || q.Close == p.IsClosed || p.HasMerged {
		return false
	}
	if q.Close {
		return p.CloseAction.State == "allowed"
	}
	return p.ReopenAction.State == "allowed"
}

// CanonicalHumanStateIntent is the shared exact typed codec for native dispatch,
// receipt consumers and offline recovery. It preserves integer identity/revision
// and exact typed preparation without floating-point normalization.
func CanonicalHumanStateIntent(q HumanStateRequest) (string, string, error) {
	if !ValidHumanStateRequest(q) {
		return "", "", ErrHumanStateQuery
	}
	actor, _ := strconv.ParseInt(q.Expected.ActorID, 10, 64)
	repository, _ := strconv.ParseInt(q.Expected.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string            `json:"operation_id"`
		ActorID                int64             `json:"actor_id"`
		RepositoryID           int64             `json:"repository_id"`
		Kind                   string            `json:"kind"`
		AuthorizationRevision  string            `json:"authorization_revision"`
		ExpectedNativeRevision int64             `json:"expected_native_revision"`
		NotAfter               int64             `json:"not_after"`
		Payload                HumanStateRequest `json:"payload"`
	}{q.OperationID, actor, repository, OperationKindHumanState, HumanStateAuthorizationRevision, q.Expected.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

// ValidHumanStateReceipt checks saved intent against the allocated native result;
// source authentication and live human admission remain separate requirements.
func ValidHumanStateReceipt(r HumanStateReceipt, q HumanStateRequest) bool {
	return ValidHumanStateRequest(q) && strconv.FormatInt(r.RepositoryID, 10) == q.Expected.RepositoryID && strconv.FormatInt(r.ActorID, 10) == q.Expected.ActorID &&
		r.Subject == q.Expected.Subject && strconv.FormatInt(r.SubjectID, 10) == q.Expected.SubjectID && strconv.FormatInt(r.IssueID, 10) == q.Expected.IssueID &&
		strconv.FormatInt(r.Index, 10) == q.Expected.Index && r.IsClosed == q.Close && r.CommentID > 0 && (q.Close && r.ClosedUnix > 0 || !q.Close && r.ClosedUnix == 0)
}

// HumanStateOperationID binds the owning application's durable command to
// its actor/repository. Persist it before dispatch; reuse it for lookup/cancel.
func HumanStateOperationID(actor, repository, command string) (string, error) {
	if !positivePullID(actor) || !positivePullID(repository) || !validOperationID(command) {
		return "", ErrHumanStateQuery
	}
	sum := sha256.Sum256([]byte(command))
	return "human-state:" + actor + ":" + repository + ":" + hex.EncodeToString(sum[:]), nil
}

func ValidHumanStateOperationID(id, actor, repository string) bool {
	if !positivePullID(actor) || !positivePullID(repository) {
		return false
	}
	prefix := "human-state:" + actor + ":" + repository + ":"
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

// ValidHumanStateInstallationID validates a captured equality condition;
// the host derives its actual installation from current admission.
func ValidHumanStateInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanState(ctx context.Context, installation string, q HumanStateRequest) (OperationRecord, error) {
	if !ValidHumanStateInstallationID(installation) || !ValidHumanStateRequest(q) || q.Expected.ActorID != a.Actor.ID || !ValidHumanStateOperationID(q.OperationID, a.Actor.ID, q.Expected.RepositoryID) {
		return OperationRecord{}, ErrHumanStateQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanStateSubmit, RepositoryID: q.Expected.RepositoryID, HumanState: &q, StateInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanState.Record, nil
}

// GetHumanState returns the live installation even for absence. Consumers
// compare it with the saved namespace before interpreting retained outcomes.
func (a Authority) GetHumanState(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanStateOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanStateQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanStateGet, RepositoryID: repository, StateOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanState, nil
}

// CancelHumanState asserts the same saved installation as submit, including
// when the cancellation creates a tombstone before any submission.
func (a Authority) CancelHumanState(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanStateInstallationID(installation) || !ValidHumanStateOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanStateQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanStateCancel, RepositoryID: repository, StateOperationID: operation, StateInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanState.Record, nil
}

func validHumanStateResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.StateOperationID
	if request.HumanState != nil {
		id = request.HumanState.OperationID
	}
	if result == nil || !validInstallationID(result.InstallationID) || result.OperationID != id ||
		request.Operation != OperationHumanStateGet && result.InstallationID != request.StateInstallationID {
		return false
	}
	if result.Status == BackgroundOutcomeNotObserved {
		return request.Operation == OperationHumanStateGet && result.Record == nil
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
	if r.Kind != OperationKindHumanState || r.ActorID != request.Authority.Actor.ID || r.RepositoryID != request.RepositoryID || len(r.IntentDigest) != 64 {
		return false
	}
	if q := request.HumanState; q != nil {
		_, digest, err := CanonicalHumanStateIntent(*q)
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
	var receipt HumanStateReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.Subject != "issue" && receipt.Subject != "pull" || receipt.SubjectID < 1 || receipt.IssueID < 1 || receipt.Index < 1 || receipt.CommentID < 1 || receipt.Subject == "issue" && receipt.SubjectID != receipt.IssueID || receipt.IsClosed && receipt.ClosedUnix <= 0 || !receipt.IsClosed && receipt.ClosedUnix != 0 {
		return false
	}
	if q := request.HumanState; q != nil {
		return ValidHumanStateReceipt(receipt, *q)
	}
	return true
}

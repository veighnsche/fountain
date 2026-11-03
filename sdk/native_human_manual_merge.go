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
)

const OperationKindHumanManualMerge = "pull_request.human_manual_merge.apply"
const HumanManualMergeAuthorizationRevision = "native-human-request"

const OperationHumanManualMergeSubmit = "repository.human_manual_merge.submit"
const OperationHumanManualMergeGet = "repository.human_manual_merge.get"
const OperationHumanManualMergeCancel = "repository.human_manual_merge.cancel"

var ErrHumanManualMergeQuery = errors.New("invalid human manual merge command")
var ErrHumanManualMergeAdmission = errors.New("current human manual merge admission is required")
var ErrHumanManualMergeConflict = errors.New("human manual merge operation conflicts")
var ErrHumanManualMergeUnavailable = errors.New("native human manual merge service is unavailable")

// HumanManualMergeRequest marks an existing reachable commit in SQL. It cannot
// select a ref-push method, create a commit or convey authority to change Git.
type HumanManualMergeRequest struct {
	OperationID string                        `json:"operation_id"`
	Expected    RepositoryManualMergeSnapshot `json:"expected"`
	NotAfter    int64                         `json:"not_after"`
}

func ValidHumanManualMergeRequest(q HumanManualMergeRequest) bool {
	p := q.Expected
	return ValidHumanManualMergeOperationID(q.OperationID, p.ActorID, p.RepositoryID) && q.NotAfter > 0 && p.NativeRevision >= 1 && !p.IsClosed && !p.HasMerged && p.Action.State == "allowed" && utf8.ValidString(p.BaseBranch) && utf8.ValidString(p.Action.Reason) &&
		ValidRepositoryManualMergeSnapshot(&p, p.RepositoryID, p.ActorID, RepositoryManualMergeSnapshotRequest{Index: p.Index, CommitOID: p.CommitOID})
}

func CanonicalHumanManualMergeIntent(q HumanManualMergeRequest) (string, string, error) {
	if !ValidHumanManualMergeRequest(q) {
		return "", "", ErrHumanManualMergeQuery
	}
	p := q.Expected
	actor, _ := strconv.ParseInt(p.ActorID, 10, 64)
	repository, _ := strconv.ParseInt(p.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string                  `json:"operation_id"`
		ActorID                int64                   `json:"actor_id"`
		RepositoryID           int64                   `json:"repository_id"`
		Kind                   string                  `json:"kind"`
		AuthorizationRevision  string                  `json:"authorization_revision"`
		ExpectedNativeRevision int64                   `json:"expected_native_revision"`
		NotAfter               int64                   `json:"not_after"`
		Payload                HumanManualMergeRequest `json:"payload"`
	}{q.OperationID, actor, repository, OperationKindHumanManualMerge, HumanManualMergeAuthorizationRevision, p.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

func HumanManualMergeOperationID(actor, repository, command string) (string, error) {
	if !positivePullID(actor) || !positivePullID(repository) || !validOperationID(command) {
		return "", ErrHumanManualMergeQuery
	}
	sum := sha256.Sum256([]byte(command))
	return "human-manual-merge:" + actor + ":" + repository + ":" + hex.EncodeToString(sum[:]), nil
}

func ValidHumanManualMergeOperationID(id, actor, repository string) bool {
	if !positivePullID(actor) || !positivePullID(repository) {
		return false
	}
	prefix := "human-manual-merge:" + actor + ":" + repository + ":"
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+64 || !validOperationID(id) {
		return false
	}
	for _, c := range []byte(strings.TrimPrefix(id, prefix)) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// This proves the atomic SQL mark/history, not a Git ref write or completed
// notification/cross-reference dispatch. Completion remains separately pending.
type HumanManualMergeReceipt struct {
	RepositoryID int64  `json:"repository_id"`
	ActorID      int64  `json:"actor_id"`
	PullID       int64  `json:"pull_id"`
	IssueID      int64  `json:"issue_id"`
	Index        int64  `json:"index"`
	BaseBranch   string `json:"base_branch"`
	BaseOID      string `json:"base_oid"`
	CommitOID    string `json:"commit_oid"`
	MergedUnix   int64  `json:"merged_unix"`
	ClosedUnix   int64  `json:"closed_unix"`
	HistoryID    int64  `json:"history_id"`
	IntentDigest string `json:"intent_digest"`
}

func ValidHumanManualMergeReceipt(r HumanManualMergeReceipt, q HumanManualMergeRequest) bool {
	p := q.Expected
	_, digest, err := CanonicalHumanManualMergeIntent(q)
	return err == nil && r.IntentDigest == digest && strconv.FormatInt(r.RepositoryID, 10) == p.RepositoryID && strconv.FormatInt(r.ActorID, 10) == p.ActorID && strconv.FormatInt(r.PullID, 10) == p.PullID && strconv.FormatInt(r.IssueID, 10) == p.IssueID && strconv.FormatInt(r.Index, 10) == p.Index && r.BaseBranch == p.BaseBranch && r.BaseOID == p.BaseOID && r.CommitOID == p.CommitOID && r.MergedUnix != 0 && r.ClosedUnix > 0 && r.HistoryID > 0
}

func ValidHumanManualMergeInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanManualMerge(ctx context.Context, installation string, q HumanManualMergeRequest) (OperationRecord, error) {
	if !ValidHumanManualMergeInstallationID(installation) || !ValidHumanManualMergeRequest(q) || q.Expected.ActorID != a.Actor.ID {
		return OperationRecord{}, ErrHumanManualMergeQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanManualMergeSubmit, RepositoryID: q.Expected.RepositoryID, HumanManualMerge: &q, ManualMergeInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanManualMerge.Record, nil
}

// Lookup reports the live installation even for absence. Compare it with the
// saved namespace before interpreting an old command's retained outcome.
func (a Authority) GetHumanManualMerge(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanManualMergeOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanManualMergeQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanManualMergeGet, RepositoryID: repository, ManualMergeOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanManualMerge, nil
}

func (a Authority) CancelHumanManualMerge(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanManualMergeInstallationID(installation) || !ValidHumanManualMergeOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanManualMergeQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanManualMergeCancel, RepositoryID: repository, ManualMergeOperationID: operation, ManualMergeInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanManualMerge.Record, nil
}

func validHumanManualMergeResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.ManualMergeOperationID
	if request.HumanManualMerge != nil {
		id = request.HumanManualMerge.OperationID
	}
	if result == nil || !validInstallationID(result.InstallationID) || result.OperationID != id || request.Operation != OperationHumanManualMergeGet && result.InstallationID != request.ManualMergeInstallationID {
		return false
	}
	if result.Status == BackgroundOutcomeNotObserved {
		return request.Operation == OperationHumanManualMergeGet && result.Record == nil
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
	if r.Kind != OperationKindHumanManualMerge || r.ActorID != request.Authority.Actor.ID || r.RepositoryID != request.RepositoryID || !validHumanReviewDigest(r.IntentDigest) {
		return false
	}
	if q := request.HumanManualMerge; q != nil {
		_, digest, err := CanonicalHumanManualMergeIntent(*q)
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
	var receipt HumanManualMergeReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.PullID < 1 || receipt.IssueID < 1 || receipt.Index < 1 || !validReviewSubmitPayloadRef("refs/heads/"+receipt.BaseBranch) || !ValidPullDiffCommit(receipt.BaseOID) || !ValidPullDiffCommit(receipt.CommitOID) || len(receipt.BaseOID) != len(receipt.CommitOID) || receipt.MergedUnix == 0 || receipt.ClosedUnix < 1 || receipt.HistoryID < 1 || receipt.IntentDigest != r.IntentDigest {
		return false
	}
	raw, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(raw, r.Receipt) {
		return false
	}
	if q := request.HumanManualMerge; q != nil {
		return ValidHumanManualMergeReceipt(receipt, *q)
	}
	return true
}

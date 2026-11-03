// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

const OperationHumanMergeSubmit = "repository.human_merge.submit"
const OperationHumanMergeGet = "repository.human_merge.get"
const OperationHumanMergeCancel = "repository.human_merge.cancel"

var ErrHumanMergeAdmission = errors.New("current human merge admission is required")
var ErrHumanMergeConflict = errors.New("human merge operation conflicts")
var ErrHumanMergeUnavailable = errors.New("native human merge service is unavailable")

func ValidHumanMergeInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanMerge(ctx context.Context, installation string, q HumanMergeRequest) (OperationRecord, error) {
	if !ValidHumanMergeInstallationID(installation) || !ValidHumanMergeRequest(q) || q.Expected.ActorID != a.Actor.ID {
		return OperationRecord{}, ErrHumanMergeQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanMergeSubmit, RepositoryID: q.Expected.RepositoryID, HumanMerge: &q, MergeInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanMerge.Record, nil
}

// Lookup reports the live installation even for absence. Compare it with the
// saved namespace before interpreting an old command's retained outcome.
func (a Authority) GetHumanMerge(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanMergeOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanMergeQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanMergeGet, RepositoryID: repository, MergeOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanMerge, nil
}

func (a Authority) CancelHumanMerge(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanMergeInstallationID(installation) || !ValidHumanMergeOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanMergeQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanMergeCancel, RepositoryID: repository, MergeOperationID: operation, MergeInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanMerge.Record, nil
}

func validHumanMergeResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.MergeOperationID
	if request.HumanMerge != nil {
		id = request.HumanMerge.OperationID
	}
	if result == nil || !validInstallationID(result.InstallationID) || result.OperationID != id || request.Operation != OperationHumanMergeGet && result.InstallationID != request.MergeInstallationID {
		return false
	}
	if result.Status == BackgroundOutcomeNotObserved {
		return request.Operation == OperationHumanMergeGet && result.Record == nil
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
	if r.Kind != OperationKindHumanMerge || r.ActorID != request.Authority.Actor.ID || r.RepositoryID != request.RepositoryID || !validHumanReviewDigest(r.IntentDigest) {
		return false
	}
	if q := request.HumanMerge; q != nil {
		_, digest, err := CanonicalHumanMergeIntent(*q)
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
	var receipt HumanMergeReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.IntentDigest != r.IntentDigest || !validHumanMergeReceiptShape(receipt) {
		return false
	}
	raw, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(raw, r.Receipt) {
		return false
	}
	if q := request.HumanMerge; q != nil {
		return ValidHumanMergeReceipt(receipt, *q)
	}
	return true
}

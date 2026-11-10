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

const (
	OperationHumanMergeSubmit = "repository.human_merge.submit"
	OperationHumanMergeGet    = "repository.human_merge.get"
	OperationHumanMergeCancel = "repository.human_merge.cancel"
)

var (
	ErrHumanMergeAdmission   = errors.New("current human merge admission is required")
	ErrHumanMergeConflict    = errors.New("human merge operation conflicts")
	ErrHumanMergeUnavailable = errors.New("native human merge service is unavailable")
)

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
	r, valid := validHumanOperationResult(result, request, humanOperationResult{
		operationID: id, installationID: request.MergeInstallationID,
		kind: OperationKindHumanMerge, getOperation: OperationHumanMergeGet,
		strictDigest: true, allowIntervention: true,
	}, request.HumanMerge, CanonicalHumanMergeIntent)
	if !valid || r == nil {
		return valid
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

func humanMergeCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:  ErrHumanMergeQuery,
		admission:   ErrHumanMergeAdmission,
		notFound:    ErrRepositoryNotVisible,
		conflict:    ErrHumanMergeConflict,
		unavailable: ErrHumanMergeUnavailable,
	}).forStatus(status)
}

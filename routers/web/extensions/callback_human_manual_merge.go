// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

func resolveHumanManualMerge(ctx context.Context, entry *callbackAdmission, request extension.CallbackRequest) (*extension.OperationLookup, int) {
	installation, err := humanOperationAdmission(ctx, entry, request.RepositoryID, "")
	if err != nil {
		return nil, http.StatusForbidden
	}
	// This is an equality condition against live admission, never a namespace
	// selected by client JSON. Check before lookup/claim/cancel so an operation
	// captured under an old installation cannot affect a replacement's journal.
	if request.Operation != extension.OperationHumanManualMergeGet && request.ManualMergeInstallationID != installation {
		return nil, http.StatusForbidden
	}
	admit := func(ctx context.Context) error {
		_, err := humanOperationAdmission(ctx, entry, request.RepositoryID, installation)
		return err
	}
	service := nativeoperation_service.Default()
	operation := request.ManualMergeOperationID
	if request.HumanManualMerge != nil {
		operation = request.HumanManualMerge.OperationID
	}
	// A scoped ID also owns cancellation tombstones before submit. Existing
	// metadata must match the current principal; never project a foreign row.
	lookup, err := service.Get(ctx, installation, operation)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if record := lookup.Record; record != nil && record.Kind != "" && (record.Kind != extension.OperationKindHumanManualMerge || record.ActorID != strconv.FormatInt(entry.actorID, 10) || record.RepositoryID != request.RepositoryID) {
		return nil, http.StatusForbidden
	}
	var record extension.OperationRecord
	switch request.Operation {
	case extension.OperationHumanManualMergeSubmit:
		record, err = service.SubmitHumanManualMerge(ctx, installation, *request.HumanManualMerge, admit)
	case extension.OperationHumanManualMergeCancel:
		if err = admit(ctx); err == nil {
			record, err = service.Cancel(ctx, installation, operation)
		}
	case extension.OperationHumanManualMergeGet:
		if err = admit(ctx); err != nil {
			return nil, http.StatusForbidden
		}
		return &lookup, http.StatusOK
	default:
		return nil, http.StatusBadRequest
	}
	if err != nil && !errors.Is(err, nativeoperation_service.ErrCancelledBeforeSubmit) {
		switch {
		case errors.Is(err, nativeoperation_service.ErrAuthorityLost):
			return nil, http.StatusForbidden
		case errors.Is(err, nativeoperation_service.ErrIntentConflict):
			return nil, http.StatusConflict
		case errors.Is(err, nativeoperation_service.ErrInvalidIntent):
			return nil, http.StatusBadRequest
		default:
			return nil, http.StatusServiceUnavailable
		}
	}
	if err = admit(ctx); err != nil {
		return nil, http.StatusForbidden
	}
	return &extension.OperationLookup{InstallationID: installation, OperationID: operation, Status: record.EffectState, Record: &record}, http.StatusOK
}

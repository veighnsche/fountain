// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"forgejo.org/extension-sdk/operationcontract"
)

func humanOperationID(prefix, actor, repository, command string, queryErr error) (string, error) {
	if !positivePullID(actor) || !positivePullID(repository) || !operationcontract.ValidID(command) {
		return "", queryErr
	}
	sum := sha256.Sum256([]byte(command))
	return prefix + actor + ":" + repository + ":" + hex.EncodeToString(sum[:]), nil
}

func validHumanOperationID(prefix, id, actor, repository string) bool {
	if !positivePullID(actor) || !positivePullID(repository) {
		return false
	}
	prefix += actor + ":" + repository + ":"
	if !strings.HasPrefix(id, prefix) || len(id) != len(prefix)+64 || !operationcontract.ValidID(id) {
		return false
	}
	for _, b := range []byte(strings.TrimPrefix(id, prefix)) {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

type humanOperationResult struct {
	operationID       string
	installationID    string
	kind              string
	getOperation      string
	strictDigest      bool
	allowIntervention bool
}

// A nil record with valid=true is a complete absence, cancellation tombstone,
// or uncommitted result. A committed record still needs its family's receipt
// validation. The codec belongs to that family, including historical codecs.
func validHumanOperationResult[T any](result *OperationLookup, request CallbackRequest, expected humanOperationResult, submitted *T, canonical func(T) (string, string, error)) (*OperationRecord, bool) {
	if result == nil || !validInstallationID(result.InstallationID) || result.OperationID != expected.operationID ||
		request.Operation != expected.getOperation && result.InstallationID != expected.installationID {
		return nil, false
	}
	if result.Status == BackgroundOutcomeNotObserved {
		return nil, request.Operation == expected.getOperation && result.Record == nil
	}
	r := result.Record
	if r == nil || r.InstallationID != result.InstallationID || r.OperationID != expected.operationID || r.Outcome != r.EffectState || r.EffectState != result.Status {
		return nil, false
	}
	switch r.EffectState {
	case EffectPending, EffectNotCommitted, EffectCommitted, EffectIndeterminate:
	default:
		return nil, false
	}
	switch r.CancellationStatus {
	case CancellationNone, CancellationPending, CancellationCancelled, CancellationTooLate, CancellationIndeterminate:
	default:
		return nil, false
	}
	if r.Kind == "" {
		return nil, r.ActorID == "" && r.RepositoryID == "" && r.IntentDigest == "" && len(r.Receipt) == 0 && r.CompletionState == "" && r.EffectState == EffectNotCommitted && r.CancellationStatus == CancellationCancelled
	}
	if r.Kind != expected.kind || r.ActorID != request.Authority.Actor.ID || r.RepositoryID != request.RepositoryID || len(r.IntentDigest) != 64 ||
		expected.strictDigest && !validHumanReviewDigest(r.IntentDigest) {
		return nil, false
	}
	if submitted != nil {
		_, digest, err := canonical(*submitted)
		if err != nil || digest != r.IntentDigest {
			return nil, false
		}
	}
	if r.EffectState != EffectCommitted {
		return nil, len(r.Receipt) == 0 && r.CompletionState == ""
	}
	if r.CompletionState != CompletionPending && r.CompletionState != CompletionComplete &&
		(!expected.allowIntervention || r.CompletionState != CompletionNeedsIntervention) {
		return nil, false
	}
	return r, true
}

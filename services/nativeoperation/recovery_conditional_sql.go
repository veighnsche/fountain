// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Historical canonical journal bytes are evidence.
	"errors"
	"reflect"

	"forgejo.org/extension-sdk/operationcontract"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"

	"github.com/google/uuid"
)

var errConditionalRecoveryEvidence = errors.New("conditional primary evidence differs")

// conditionalRecoveryPayload validates historical capture without applying a
// live submission deadline. Recanonicalization uses the original journal codec,
// including its payload number normalization; no receipt or hash is rewritten.
func conditionalRecoveryPayload(op *nativeoperation_model.Operation, kind string) (json.RawMessage, error) {
	if op == nil || !op.Submitted || op.Kind != kind || !validOperationID(op.OperationID) || operationcontract.IsHumanID(op.OperationID) ||
		op.ActorID <= 0 || op.RepositoryID <= 0 || op.ExpectedNativeRevision < 1 || op.NotAfter <= 0 || op.AuthRevision == "" || len(op.AuthRevision) > 512 {
		return nil, errConditionalRecoveryEvidence
	}
	id, err := uuid.Parse(op.InstallationID)
	if err != nil || id.String() != op.InstallationID {
		return nil, errConditionalRecoveryEvidence
	}
	var saved struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil || len(saved.Payload) == 0 {
		return nil, errConditionalRecoveryEvidence
	}
	intent := &ValidIntent{
		OperationID: op.OperationID, ActorID: op.ActorID, RepositoryID: op.RepositoryID, Kind: op.Kind,
		AuthRevision: op.AuthRevision, ExpectedNativeRevision: op.ExpectedNativeRevision, NotAfter: op.NotAfter,
	}
	canonical, digest, err := canonicalDigest(intent, saved.Payload)
	if err != nil || canonical != op.Intent || digest != op.IntentDigest {
		return nil, errConditionalRecoveryEvidence
	}
	return saved.Payload, nil
}

func exactConditionalReceipt(raw string, receipt any) error {
	if raw == "" || json.Unmarshal([]byte(raw), receipt) != nil {
		return errConditionalRecoveryEvidence
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || string(canonical) != raw {
		return errConditionalRecoveryEvidence
	}
	return nil
}

func (s *Service) recoverConditionalSQL(ctx context.Context, a *RecoveryAssessment, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation, expected Scope, inspect func(context.Context, *nativeoperation_model.Operation) (string, error)) (RecoveryAssessment, error) {
	if !nativeoperation_model.OfflineInhibited() {
		return RecoveryAssessment{}, ErrNotInhibited
	}
	raw, err := encodeScope(expected)
	if err != nil || held == nil || op == nil || held.Owner != conditionalOwner(op.InstallationID, op.OperationID) ||
		held.OwnerKind != nativeoperation_model.OwnerConditional || held.Generation < 1 || held.Revision != op.ExpectedNativeRevision+1 ||
		held.Scope != raw || !reflect.DeepEqual(scope, expected) {
		return fenced(a, ReasonRecoveryUncertainEffect, "conditional SQL scope and immutable capture differ")
	}
	recorded, err := nativeoperation_model.RecoverConditionalSQLPrimary(ctx, op, held.Owner, held.Generation, held.Scope, func(tx context.Context, current *nativeoperation_model.Operation) (string, error) {
		if _, err := repo_model.GetRepositoryByID(tx, current.RepositoryID); err != nil {
			if repo_model.IsErrRepoNotExist(err) {
				return "", errConditionalRecoveryEvidence
			}
			return "", err
		}
		if _, err := user_model.GetUserByID(tx, current.ActorID); err != nil {
			if user_model.IsErrUserNotExist(err) {
				return "", errConditionalRecoveryEvidence
			}
			return "", err
		}
		return inspect(tx, current)
	})
	if err != nil {
		if errors.Is(err, errConditionalRecoveryEvidence) || errors.Is(err, nativeoperation_model.ErrAdmissionLost) || errors.Is(err, nativeoperation_model.ErrWrongOwner) {
			return fenced(a, ReasonRecoveryUncertainEffect, "conditional SQL primary receipt, native evidence or exact owner differs; preserved")
		}
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, recorded.EffectState
	a.Checks = append(a.Checks, "exact conditional capture and atomic SQL outcome reconciled; no primary or notification replayed")
	s.retireStaleCapabilities()
	return *a, nil
}

func recoveredLifecycleCompletion(op *nativeoperation_model.Operation) (string, error) {
	switch op.Completion {
	case nativeoperation_model.CompletionPending, nativeoperation_model.CompletionNeedsIntervention:
		return nativeoperation_model.CompletionNeedsIntervention, nil
	case nativeoperation_model.CompletionComplete:
		return nativeoperation_model.CompletionComplete, nil
	default:
		return "", errConditionalRecoveryEvidence
	}
}

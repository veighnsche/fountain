// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/modules/setting"
)

// All conditional effect kinds capture the same authenticated provenance.
// Keeping that packet here prevents a newly added credential/grant binding
// from being lost by an individual publisher, reviewer or issue writer.
func operationForSubmission(decision extensionauth_model.SubmissionDecision, installation string, intent *ValidIntent) *nativeoperation_model.Operation {
	return &nativeoperation_model.Operation{InstallationID: installation, OperationID: intent.OperationID, Kind: intent.Kind,
		ActorID: decision.ActorID, RepositoryID: intent.RepositoryID, TokenID: decision.TokenID, CredentialFingerprint: decision.CredentialFingerprint,
		PackageID: decision.PackageID, ControllerGrantID: decision.ControllerGrantID, AuthRevision: intent.AuthRevision,
		ExpectedNativeRevision: intent.ExpectedNativeRevision, NotAfter: intent.NotAfter, IntentDigest: intent.Digest, Intent: intent.Canonical}
}

// Submit authenticates the installation/principal (already verified by the
// dispatcher into decision), validates the complete intent and durably claims
// its operation ID before native execution. The same ID and identical intent
// return or reconcile the existing record, never start a second execution.
// Different content under the same ID is intent_conflict.
func (s *Service) Submit(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent) (sdk.OperationRecord, error) {
	if intent == nil || intent.Kind == "" || decision.ActorID != intent.ActorID {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	if setting.OAuth2.FactoryAppliancePackageID != "" && decision.PackageID == setting.OAuth2.FactoryAppliancePackageID && decision.ControllerGrantID == "" {
		return sdk.OperationRecord{}, ErrAuthorityLost
	}
	if decision.ControllerGrantID != "" {
		if _, err := extensionauth_model.CheckControllerUse(ctx, extensionauth_model.ControllerUse{GrantID: decision.ControllerGrantID, InstallationID: installationID,
			PackageID: decision.PackageID, ActorID: decision.ActorID, RepositoryID: intent.RepositoryID, TokenID: decision.TokenID,
			CredentialFingerprint: decision.CredentialFingerprint, Kind: intent.Kind, NotAfter: intent.NotAfter}); err != nil {
			return sdk.OperationRecord{}, ErrAuthorityLost
		}
	}
	implementation, ok := implementationFor(intent.Kind)
	if !ok || implementation.submit == nil {
		return sdk.OperationRecord{}, ErrKindUnavailable
	}

	existing, err := nativeoperation_model.LookupOperation(ctx, installationID, intent.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if existing != nil {
		return s.reconcileSubmission(existing, decision, intent)
	}
	return implementation.submit(s, ctx, decision, installationID, intent)
}

// Both the initial lookup and a concurrent journal-insert loser must bind the
// same grant provenance. An identical payload cannot adopt another grant's
// receipt, while a pre-submit cancellation retains its original meaning.
func (s *Service) reconcileSubmission(existing *nativeoperation_model.Operation, decision extensionauth_model.SubmissionDecision, intent *ValidIntent) (sdk.OperationRecord, error) {
	if existing.Submitted && (existing.ControllerGrantID != decision.ControllerGrantID || existing.PackageID != decision.PackageID || existing.ActorID != decision.ActorID) {
		return sdk.OperationRecord{}, ErrIntentConflict
	}
	return s.reconcileExisting(existing, intent)
}

func (s *Service) reconcileExisting(existing *nativeoperation_model.Operation, intent *ValidIntent) (sdk.OperationRecord, error) {
	if !existing.Submitted {
		return ToRecord(existing), ErrCancelledBeforeSubmit
	}
	if existing.IntentDigest != intent.Digest {
		return sdk.OperationRecord{}, ErrIntentConflict
	}
	// Identical replay: return the recorded outcome without relaunching.
	return ToRecord(existing), nil
}

// Get returns the recorded intent, effect and native completion evidence for
// the owning installation. Absence is not_observed, not proof that an
// earlier request cannot still arrive or that a write never occurred. Lookup
// performs no new mutation.
func (s *Service) Get(ctx context.Context, installationID, operationID string) (sdk.OperationLookup, error) {
	if !validOperationID(operationID) {
		return sdk.OperationLookup{}, ErrInvalidIntent
	}
	op, err := nativeoperation_model.LookupOperation(ctx, installationID, operationID)
	if err != nil {
		return sdk.OperationLookup{}, err
	}
	return ToLookup(installationID, operationID, op), nil
}

// Cancel invalidates the owning installation's operation even if the native
// actor has since lost permission. It persists cancellation even when
// submission has not arrived; a delayed submission under the same ID cannot
// recreate authority. Cancellation is pending until its ordering against any
// in-flight write is known.
func (s *Service) Cancel(ctx context.Context, installationID, operationID string) (sdk.OperationRecord, error) {
	if !validOperationID(operationID) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	op, err := nativeoperation_model.LookupOperation(ctx, installationID, operationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if op == nil {
		tombstone, err := nativeoperation_model.InsertTombstone(ctx, installationID, operationID)
		if err != nil && !errors.Is(err, nativeoperation_model.ErrDuplicateOperation) {
			return sdk.OperationRecord{}, err
		}
		if errors.Is(err, nativeoperation_model.ErrDuplicateOperation) {
			op = tombstone
		} else {
			return ToRecord(tombstone), nil
		}
	}
	if !op.Submitted {
		return ToRecord(op), nil
	}
	revoked, err := nativeoperation_model.RevokeOperation(ctx, installationID, operationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if revoked == nil {
		return sdk.OperationRecord{}, errors.New("operation vanished during cancellation")
	}
	return ToRecord(revoked), nil
}

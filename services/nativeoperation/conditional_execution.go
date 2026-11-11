// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
	"os"
)

// claimBackgroundSubmit selects only the implementation bound by the closed
// host composition. Kind-specific callers cannot replace the claim protocol.
func (s *Service) claimBackgroundSubmit(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent, scope Scope) (sdk.OperationRecord, error) {
	implementation, ok := implementationFor(intent.Kind)
	if !ok || implementation.mode != executeOnSubmit || implementation.execute == nil {
		return sdk.OperationRecord{}, ErrKindUnavailable
	}
	return s.claimConditionalSubmit(ctx, decision, installationID, intent, scope,
		func(ctx context.Context, op *nativeoperation_model.Operation, intent *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
			owned := execcontext.NewContext(ctx, execution)
			if err := s.revalidateSubmission(owned, op); err != nil {
				if errors.Is(err, ErrAuthorityLost) {
					return conditionalRefusal(ctx, op, execution)(nativeoperation_model.ReasonAuthorityLost)
				}
				return sdk.OperationRecord{}, terminalResult{}, err
			}
			return implementation.execute(s, ctx, op, intent, scope, execution)
		})
}

// claimConditionalSubmit owns capability creation, exact claim/replay handling
// and retirement for the private background and human execution lanes.
func (s *Service) claimConditionalSubmit(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent, scope Scope, execute func(context.Context, *nativeoperation_model.Operation, *ValidIntent, *execcontext.Execution) (sdk.OperationRecord, terminalResult, error)) (sdk.OperationRecord, error) {
	dir, err := s.execDir()
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	capabilityPath, secret, err := execcontext.WriteCapabilityFile(dir)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	retire := func() { _ = os.Remove(capabilityPath) }
	owner := conditionalOwner(installationID, intent.OperationID)
	encoded, err := encodeScope(scope)
	if err != nil {
		retire()
		return sdk.OperationRecord{}, err
	}
	op := operationForSubmission(decision, installationID, intent)
	claimed, err := nativeoperation_model.ClaimConditional(ctx, op, owner, encoded, execcontext.Verifier(secret))
	if err != nil {
		retire()
		if errors.Is(err, nativeoperation_model.ErrBusy) {
			return sdk.OperationRecord{}, ErrBusy
		}
		if errors.Is(err, nativeoperation_model.ErrInhibited) {
			return sdk.OperationRecord{}, err
		}
		if errors.Is(err, nativeoperation_model.ErrStaleRevision) {
			return s.recordStaleRevision(ctx, op)
		}
		// A concurrent identical submit may have won the insert; reread
		// and reconcile rather than launching a second execution.
		existing, lookupErr := nativeoperation_model.LookupOperation(ctx, installationID, intent.OperationID)
		if lookupErr != nil {
			return sdk.OperationRecord{}, lookupErr
		}
		if existing != nil {
			return s.reconcileSubmission(existing, decision, intent)
		}
		return sdk.OperationRecord{}, err
	}
	execution := &execcontext.Execution{Owner: owner, Generation: claimed.Generation, CapabilityPath: capabilityPath}
	record, terminal, err := execute(ctx, op, intent, execution)
	if err != nil {
		// Infrastructure failure before reconciliation: fail closed with
		// the owner held and the capability kept for later recovery.
		return sdk.OperationRecord{}, err
	}
	if !terminal.retain {
		// The terminal record already released the owner; retire the file.
		retire()
	}
	return record, nil
}

func conditionalRefusal(ctx context.Context, op *nativeoperation_model.Operation, execution *execcontext.Execution) func(string) (sdk.OperationRecord, terminalResult, error) {
	release := context.WithoutCancel(ctx)
	return func(reason string) (sdk.OperationRecord, terminalResult, error) {
		recorded, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone,
		}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(recorded), terminalResult{}, nil
	}
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	extensionauth_model "forgejo.org/models/extensionauth"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	execcontext "forgejo.org/modules/nativeoperation"
	repo_module "forgejo.org/modules/repository"

	"github.com/google/uuid"
)

const (
	CrashPointHumanMergeBeforePush    = "human-merge-before-push"
	CrashPointHumanMergeAfterPush     = "human-merge-after-push"
	CrashPointHumanMergeAfterFinish   = "human-merge-after-finish"
	CrashPointHumanMergeBeforeRelease = "human-merge-before-release"
)

// SubmitHumanMerge uses live human admission and the existing conditional
// operation owner, never background enrollment. One captured command stages and
// pushes at most once. Existing records are returned without restarting work.
// dispatch is supplied by the repository service to join its native push engine
// without an import cycle; all event facts still come from the proved receipt.
func (s *Service) SubmitHumanMerge(ctx context.Context, installation string, q sdk.HumanMergeRequest, admit HumanReviewAdmission, dispatch func(context.Context, *repo_model.Repository, []*repo_module.PushUpdateOptions) error) (sdk.OperationRecord, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil || dispatch == nil || db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, err := humanMergeIntent(q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if admit(ctx) != nil {
		return sdk.OperationRecord{}, ErrAuthorityLost
	}
	finish := func(record sdk.OperationRecord, err error) (sdk.OperationRecord, error) {
		if err == nil || errors.Is(err, ErrCancelledBeforeSubmit) {
			if ctx.Err() != nil || admit(ctx) != nil {
				return sdk.OperationRecord{}, ErrAuthorityLost
			}
		}
		return record, err
	}
	existing, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if existing != nil {
		record, err := s.reconcileExisting(existing, intent)
		return finish(record, err)
	}
	if q.NotAfter <= s.clock() || q.NotAfter > s.clock()+int64(MaxOperationLifetime/time.Second) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	record, err := s.claimConditionalSubmit(ctx, extensionauth_model.SubmissionDecision{ActorID: intent.ActorID}, installation, intent, humanMergeCaptureScope(q), func(ctx context.Context, _ *nativeoperation_model.Operation, _ *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
		owned := execcontext.NewContext(ctx, execution)
		settle := context.WithoutCancel(owned)
		live, cancel := context.WithDeadline(owned, time.Unix(q.NotAfter, 0))
		defer cancel()
		stage, err := s.PrepareHumanMergeOperation(live, installation, q, admit)
		if err != nil {
			return s.settleUnpreparedHumanMerge(settle, installation, q)
		}
		defer stage.Close()
		if err := TestCrashBarrier(CrashPointHumanMergeBeforePush); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		// Git/transport errors cannot classify the effect. Push returns only
		// after its synchronous writer/hooks finish; Close retires local state.
		_, _ = s.PushHumanMergeOperation(live, installation, q, stage, admit)
		stage.Close()
		if err := TestCrashBarrier(CrashPointHumanMergeAfterPush); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		record, err := s.RecordHumanMergeEffect(settle, installation, q)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		switch record.EffectState {
		case nativeoperation_model.EffectNotCommitted:
			record, err = s.ReleaseHumanMergeNotCommitted(settle, installation, q, stage)
			return record, terminalResult{}, err
		case nativeoperation_model.EffectIndeterminate:
			return record, terminalResult{retain: true}, nil
		case nativeoperation_model.EffectCommitted:
		default:
			return sdk.OperationRecord{}, terminalResult{}, ErrHumanMergePushEvidence
		}
		if _, err := s.ApplyHumanMergeMetadata(settle, installation, q); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if err := s.SyncHumanMergeBranch(settle, installation, q); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		record, err = s.FinishHumanMerge(settle, installation, q)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if record.CompletionState == nativeoperation_model.CompletionNeedsIntervention {
			return record, terminalResult{retain: true}, nil
		}
		if err := TestCrashBarrier(CrashPointHumanMergeAfterFinish); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		record, err = s.DispatchHumanMergePushEvent(settle, installation, q, dispatch)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if record.CompletionState == nativeoperation_model.CompletionNeedsIntervention {
			return record, terminalResult{retain: true}, nil
		}
		if err := TestCrashBarrier(CrashPointHumanMergeBeforeRelease); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		record, err = s.CompleteHumanMerge(settle, installation, q, stage)
		return record, terminalResult{}, err
	})
	return finish(record, err)
}

// Preparation is local-only. Before any result is bound, this exact scope can
// neither admit nor launch a native push. Scope/operation locks order settlement
// against binding; a bound scope or uncertain current ref keeps the fence.
func (s *Service) settleUnpreparedHumanMerge(ctx context.Context, installation string, q sdk.HumanMergeRequest) (sdk.OperationRecord, terminalResult, error) {
	intent, held, scope, err := readHumanMergeHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if scope.NewOID != "" || scope.CompletionPhase != "" {
		return sdk.OperationRecord{}, terminalResult{}, ErrHumanMergeCompletionNotReady
	}
	record, err := s.RecordHumanMergeEffect(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if record.EffectState != nativeoperation_model.EffectNotCommitted {
		return record, terminalResult{retain: true}, nil
	}
	released, err := nativeoperation_model.ReleaseHumanMergeNotCommitted(ctx, humanMergeExpectedOperation(installation, intent), held.Owner, held.Generation, held.Scope, func(tx context.Context, op *nativeoperation_model.Operation) error {
		current, err := s.observeHumanMergeEffect(tx, op, q, scope)
		if err != nil {
			return err
		}
		if current.EffectState != nativeoperation_model.EffectNotCommitted {
			return ErrHumanMergePushEvidence
		}
		return nil
	})
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	// claimConditionalSubmit retires the private file after this exact release.
	return ToRecord(released), terminalResult{}, nil
}

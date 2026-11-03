// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"

	sdk "forgejo.org/extension-sdk"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/google/uuid"
)

// RecordHumanMergeEffect attributes only the held operation's primary ref
// effect. Raw writer reports and caller-provided commit facts are not evidence.
// It needs the native execution capability, not renewed permission to perform a
// merge: current authority loss must not erase a primary effect already made.
// Every outcome retains the owner/capability for completion or quiescence/recovery.
func (s *Service) RecordHumanMergeEffect(ctx context.Context, installation string, q sdk.HumanMergeRequest) (sdk.OperationRecord, error) {
	intent, held, scope, err := readHumanMergeHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	want := humanMergeExpectedOperation(installation, intent)
	recorded, err := nativeoperation_model.RecordHumanMergeEffect(ctx, want, held.Owner, held.Generation, held.Scope, func(tx context.Context, op *nativeoperation_model.Operation) (nativeoperation_model.TerminalOutcome, error) {
		return s.observeHumanMergeEffect(tx, op, q, scope)
	})
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if recorded.EffectState == nativeoperation_model.EffectCommitted {
		var receipt sdk.HumanMergeReceipt
		if json.Unmarshal([]byte(recorded.Receipt), &receipt) != nil || !sdk.ValidHumanMergeReceipt(receipt, q) || receipt.ResultOID != scope.NewOID {
			return sdk.OperationRecord{}, ErrHumanMergePushEvidence
		}
		raw, err := json.Marshal(receipt)
		if err != nil || string(raw) != recorded.Receipt {
			return sdk.OperationRecord{}, ErrHumanMergePushEvidence
		}
	}
	return ToRecord(recorded), nil
}

func humanMergeEffectOutcome(op *nativeoperation_model.Operation, effect, reason, receipt string) nativeoperation_model.TerminalOutcome {
	o := nativeoperation_model.TerminalOutcome{EffectState: effect, Reason: reason, Receipt: receipt, Cancellation: nativeoperation_model.CancellationNone}
	if effect == nativeoperation_model.EffectCommitted {
		o.Completion = nativeoperation_model.CompletionPending
	}
	if op.Revoked {
		switch effect {
		case nativeoperation_model.EffectCommitted:
			o.Cancellation = nativeoperation_model.CancellationTooLate
		case nativeoperation_model.EffectNotCommitted:
			o.Cancellation = nativeoperation_model.CancellationCancelled
		default:
			o.Cancellation = nativeoperation_model.CancellationIndeterminate
		}
	}
	return o
}

func (s *Service) observeHumanMergeEffect(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanMergeRequest, scope Scope) (nativeoperation_model.TerminalOutcome, error) {
	uncertain := func(reason string) (nativeoperation_model.TerminalOutcome, error) {
		return humanMergeEffectOutcome(op, nativeoperation_model.EffectIndeterminate, reason, ""), nil
	}
	repository, err := repo_model.GetRepositoryByID(ctx, op.RepositoryID)
	if err != nil {
		return uncertain(nativeoperation_model.ReasonNativeRefused)
	}
	native, err := git.OpenRepository(ctx, repository.RepoPath())
	if err != nil {
		return uncertain(nativeoperation_model.ReasonNativeRefused)
	}
	defer native.Close()
	tip, err := native.GetRefCommitID(scope.Ref)
	if err != nil {
		return uncertain(nativeoperation_model.ReasonStaleBaseOrResult)
	}
	if !op.Admitted {
		if tip != scope.OldOID {
			return uncertain(nativeoperation_model.ReasonUnexpectedRefEffects)
		}
		reason := op.Reason
		if op.Revoked {
			reason = nativeoperation_model.ReasonCancelledBeforeAdmission
		} else if reason == "" {
			reason = nativeoperation_model.ReasonNativeRefused
		}
		return humanMergeEffectOutcome(op, nativeoperation_model.EffectNotCommitted, reason, ""), nil
	}
	if scope.NewOID == "" || tip != scope.NewOID || tip == scope.OldOID {
		return uncertain(nativeoperation_model.ReasonStaleBaseOrResult)
	}
	commit, err := native.GetCommit(tip)
	if err != nil {
		return uncertain(nativeoperation_model.ReasonNativeRefused)
	}
	base, err := native.GetMergeBaseSimple(scope.OldOID, tip)
	if err != nil || base != scope.OldOID {
		return uncertain(nativeoperation_model.ReasonStaleBaseOrResult)
	}
	p := q.Expected
	r := sdk.HumanMergeReceipt{RepositoryID: op.RepositoryID, ActorID: op.ActorID, PullID: nativeCommentID(p.PullID), IssueID: nativeCommentID(p.IssueID), Index: nativeCommentID(p.Index), Method: p.Method, BaseBranch: p.BaseBranch, HeadOID: p.HeadOID, BaseOID: p.BaseOID, MergeBaseOID: p.MergeBaseOID, ResultOID: tip, TreeOID: commit.Tree.ID.String(), IntentDigest: op.IntentDigest}
	for _, parent := range commit.Parents {
		r.Parents = append(r.Parents, parent.String())
	}
	if !sdk.ValidHumanMergeReceipt(r, q) {
		return uncertain(nativeoperation_model.ReasonNativeRefused)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nativeoperation_model.TerminalOutcome{}, err
	}
	return humanMergeEffectOutcome(op, nativeoperation_model.EffectCommitted, "", string(raw)), nil
}

func readHumanMergeHeld(ctx context.Context, installation string, q sdk.HumanMergeRequest) (*ValidIntent, *nativeoperation_model.Reservation, Scope, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation {
		return nil, nil, Scope{}, ErrInvalidIntent
	}
	intent, err := humanMergeIntent(q)
	if err != nil {
		return nil, nil, Scope{}, err
	}
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return nil, nil, Scope{}, err
	}
	x := execcontext.FromContext(ctx)
	if x == nil || held.OwnerKind != nativeoperation_model.OwnerConditional || x.Owner != held.Owner || x.Generation != held.Generation || held.Owner != conditionalOwner(installation, q.OperationID) || held.Revision != intent.ExpectedNativeRevision+1 {
		return nil, nil, Scope{}, nativeoperation_model.ErrWrongOwner
	}
	proof, err := execcontext.ReadCapabilityFile(x.CapabilityPath)
	if err != nil || !execcontext.VerifyProof(held.Verifier, proof) {
		return nil, nil, Scope{}, nativeoperation_model.ErrWrongOwner
	}
	scope, err := decodeScope(held.Scope)
	if err != nil {
		return nil, nil, Scope{}, err
	}
	if err := checkHumanMergeScope(q, scope, held.Scope); err != nil {
		return nil, nil, Scope{}, err
	}
	return intent, held, scope, nil
}

// Live attribution and offline recovery share one exact scope definition.
func checkHumanMergeScope(q sdk.HumanMergeRequest, scope Scope, raw string) error {
	exact := humanMergeCaptureScope(q)
	exact.NewOID = scope.NewOID
	// Completion claims are server-derived phases after the ref primary. They
	// never alter its exact tuple or immutable receipt, and admit no new push.
	if scope.CompletionPhase != "" && scope.CompletionPhase != HumanMergeFinishStarted && scope.CompletionPhase != HumanMergeFinishDone && scope.CompletionPhase != HumanMergePushEventStarted && scope.CompletionPhase != HumanMergePushEventDone {
		return ErrInvalidIntent
	}
	exact.CompletionPhase = scope.CompletionPhase
	encoded, err := encodeScope(exact)
	if err != nil || encoded != raw || scope.NewOID != "" && (!sdk.ValidPullDiffCommit(scope.NewOID) || scope.NewOID == scope.OldOID) {
		return ErrInvalidIntent
	}
	return nil
}

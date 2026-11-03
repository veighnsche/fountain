// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Persisted native intent codec.
	"errors"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"

	"github.com/google/uuid"
)

// Called only through Recover's stopped/inhibited-domain contract. The primary
// SQL mark and receipt are atomic: recovery neither reconstructs a receipt from
// a similar PR nor repeats native metadata, notification or reference effects.
func (s *Service) recoverHumanManualMerge(ctx context.Context, a *RecoveryAssessment, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	// Proof retirement must follow our committed release, never an outer
	// transaction that could still roll back after this method returns.
	if db.InTransaction(ctx) {
		return RecoveryAssessment{}, ErrInvalidIntent
	}
	var saved struct {
		Payload sdk.HumanManualMergeRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "manual merge intent is missing")
	}
	q := saved.Payload
	intent, err := humanManualMergeIntent(q)
	installation, parseErr := uuid.Parse(op.InstallationID)
	if err != nil || parseErr != nil || installation.String() != op.InstallationID || !humanMergeCaptureMatches(op, intent) || held.OwnerKind != nativeoperation_model.OwnerConditional || held.Generation < 1 || held.Revision != op.ExpectedNativeRevision+1 || checkHumanManualMergeScope(q, scope, held.Scope) != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "manual merge scope and immutable intent differ")
	}
	recorded, err := nativeoperation_model.RecoverHumanManualMerge(ctx, humanManualMergeOperation(op.InstallationID, intent), held.Owner, held.Generation, held.Scope, func(tx context.Context, current *nativeoperation_model.Operation) (string, error) {
		phase := scope.CompletionPhase
		if !current.Admitted && (current.EffectState == nativeoperation_model.EffectPending || current.EffectState == nativeoperation_model.EffectNotCommitted) && current.Receipt == "" && current.Completion == "" && phase == "" {
			pr, err := readHumanManualMergeParent(tx, q)
			if err != nil {
				return "", err
			}
			if pr.HasMerged || pr.Issue.IsClosed != q.Expected.IsClosed {
				return "", ErrHumanManualMergeEvidence
			}
			return "", nil
		}
		if !current.Admitted || current.EffectState != nativeoperation_model.EffectCommitted {
			return "", ErrHumanManualMergeEvidence
		}
		if _, _, err := readHumanManualMergeReceipt(tx, q, current.Receipt); err != nil {
			return "", err
		}
		switch current.Completion {
		case nativeoperation_model.CompletionPending:
			if phase == HumanManualMergeFinishDone {
				return nativeoperation_model.CompletionComplete, nil
			}
			return nativeoperation_model.CompletionNeedsIntervention, nil
		case nativeoperation_model.CompletionComplete:
			if phase != HumanManualMergeFinishDone {
				return "", ErrHumanManualMergeEvidence
			}
			return current.Completion, nil
		case nativeoperation_model.CompletionNeedsIntervention:
			if phase == HumanManualMergeFinishDone {
				return "", ErrHumanManualMergeEvidence
			}
			return current.Completion, nil
		default:
			return "", ErrHumanManualMergeEvidence
		}
	})
	if err != nil {
		if issues_model.IsErrCommentNotExist(err) || issues_model.IsErrPullRequestNotExist(err) || issues_model.IsErrIssueNotExist(err) || repo_model.IsErrRepoNotExist(err) || user_model.IsErrUserNotExist(err) {
			return fenced(a, ReasonRecoveryMissingEvidence, "manual merge native parent, history or original actor is missing; preserved")
		}
		if errors.Is(err, ErrHumanManualMergeEvidence) || errors.Is(err, nativeoperation_model.ErrAdmissionLost) || errors.Is(err, nativeoperation_model.ErrWrongOwner) {
			return fenced(a, ReasonRecoveryUncertainEffect, "manual merge native evidence or atomic outcome differs; preserved")
		}
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, recorded.EffectState
	a.Checks = append(a.Checks, "exact manual merge capture and native SQL/Git evidence reconciled; no primary or finishing dispatch replayed")
	s.retireStaleCapabilities()
	return *a, nil
}

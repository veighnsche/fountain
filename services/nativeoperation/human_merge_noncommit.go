// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"os"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"
)

// ReleaseHumanMergeNotCommitted retires a proved live refusal after Close has
// stopped its exact stage, including a pre-launch refusal. A saved non-commit
// alone is insufficient: admission must remain false and the current ref must
// still prove no effect. No fresh human permission is needed to preserve already
// recorded refusal truth. Interrupted writers without their stage need recovery.
func (s *Service) ReleaseHumanMergeNotCommitted(ctx context.Context, installation string, q sdk.HumanMergeRequest, stage *pull_service.PreparedExactMerge) (sdk.OperationRecord, error) {
	if stage == nil || db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, held, scope, err := readHumanMergeHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if scope.CompletionPhase != "" {
		return sdk.OperationRecord{}, ErrHumanMergeCompletionNotReady
	}
	recorded, err := nativeoperation_model.ReleaseHumanMergeNotCommitted(ctx, humanMergeExpectedOperation(installation, intent), held.Owner, held.Generation, held.Scope, func(tx context.Context, op *nativeoperation_model.Operation) error {
		observed, err := s.observeHumanMergeEffect(tx, op, q, scope)
		if err != nil {
			return err
		}
		if observed.EffectState != nativeoperation_model.EffectNotCommitted || observed.Receipt != "" || observed.Completion != "" {
			return ErrHumanMergePushEvidence
		}
		pr, err := issues_model.GetPullRequestByID(tx, nativeCommentID(q.Expected.PullID))
		if err != nil {
			return err
		}
		if err := pr.LoadIssue(tx); err != nil {
			return err
		}
		actor, err := user_model.GetUserByID(tx, intent.ActorID)
		if err != nil {
			return err
		}
		return stage.WithClosedExactTarget(tx, pr, actor, repo_model.MergeStyle(q.Expected.Method), q.Expected.HeadOID, q.Expected.BaseOID, q.Message, func(result pull_service.PreparedMergeResult) error {
			if scope.NewOID != "" && scope.NewOID != result.CommitID {
				return ErrHumanMergePushEvidence
			}
			return nil
		})
	})
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	record := ToRecord(recorded)
	// SQL has already revoked this proof, even if private-file cleanup fails.
	if err := os.Remove(execcontext.FromContext(ctx).CapabilityPath); err != nil && !os.IsNotExist(err) {
		return record, err
	}
	return record, nil
}

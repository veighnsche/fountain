// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"
)

var ErrHumanMergeCompletionNotReady = errors.New("native human merge completion is not ready")

// CompleteHumanMerge releases a live writer only after its exact native stage
// is closed, its primary receipt is proved, and all native completion dispatch
// phases succeeded. It cannot release an interrupted writer without that live
// stage; stopped-writer recovery is separate. Retained lookup resolves a lost
// result without replaying a push, notification or completion event.
func (s *Service) CompleteHumanMerge(ctx context.Context, installation string, q sdk.HumanMergeRequest, stage *pull_service.PreparedExactMerge) (sdk.OperationRecord, error) {
	if stage == nil || db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, held, scope, err := readHumanMergeHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if scope.CompletionPhase != HumanMergePushEventDone {
		return sdk.OperationRecord{}, ErrHumanMergeCompletionNotReady
	}
	want := humanMergeExpectedOperation(installation, intent)
	var recorded *nativeoperation_model.Operation
	_, err = s.withHumanMergeReceipt(ctx, installation, q, func(tx context.Context, receipt sdk.HumanMergeReceipt, pr *issues_model.PullRequest, actor *user_model.User) (bool, error) {
		if !pr.HasMerged || !pr.Issue.IsClosed || pr.MergedCommitID != receipt.ResultOID || pr.MergerID != receipt.ActorID || pr.MergeBase != receipt.MergeBaseOID || pr.MergedUnix <= 0 {
			return false, pull_service.ErrMergeMetadata
		}
		branch, err := git_model.GetBranch(tx, receipt.RepositoryID, receipt.BaseBranch)
		if err != nil {
			return false, err
		}
		if branch.IsDeleted || branch.CommitID != receipt.ResultOID {
			return false, ErrHumanMergePushEvidence
		}
		err = stage.WithQuiescentExactTarget(tx, pr, actor, repo_model.MergeStyle(q.Expected.Method), q.Expected.HeadOID, q.Expected.BaseOID, q.Message, func(result pull_service.PreparedMergeResult) error {
			if result.CommitID != receipt.ResultOID || result.TreeID != receipt.TreeOID || !slices.Equal(result.Parents, receipt.Parents) {
				return ErrHumanMergePushEvidence
			}
			raw, err := json.Marshal(receipt)
			if err != nil {
				return err
			}
			recorded, err = nativeoperation_model.CompleteHumanMerge(tx, want, held.Owner, held.Generation, held.Scope, string(raw))
			return err
		})
		return false, err
	})
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	// The SQL release revokes this proof before deleting its private file. If
	// cleanup fails, lookup still reports the committed completion truth.
	record := ToRecord(recorded)
	if err := os.Remove(execcontext.FromContext(ctx).CapabilityPath); err != nil && !os.IsNotExist(err) {
		return record, err
	}
	return record, nil
}

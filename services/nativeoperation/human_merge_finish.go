// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"errors"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	user_model "forgejo.org/models/user"
	pull_service "forgejo.org/services/pull"
)

const HumanMergeFinishStarted = "human-merge-finish-started"
const HumanMergeFinishDone = "human-merge-finish-done"

var ErrHumanMergeFinishClaimed = errors.New("native human merge finishing was already claimed")

// FinishHumanMerge dispatches native merge notification/cross-reference work
// once after exact PR and branch-catalog completion. The durable scope marker
// commits before dispatch; an uncertain/failed attempt is never replayed here.
// Overall completion and ownership remain pending for push-event processing and
// safe writer quiescence/recovery. No new merge authorization is performed.
func (s *Service) FinishHumanMerge(ctx context.Context, installation string, q sdk.HumanMergeRequest) (sdk.OperationRecord, error) {
	// The claim must commit before dispatch, independently of any caller SQL.
	if db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, held, scope, err := readHumanMergeHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if scope.CompletionPhase != "" {
		return sdk.OperationRecord{}, ErrHumanMergeFinishClaimed
	}
	started := scope
	started.CompletionPhase = HumanMergeFinishStarted
	startedJSON, err := encodeScope(started)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	done := scope
	done.CompletionPhase = HumanMergeFinishDone
	doneJSON, err := encodeScope(done)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	want := humanMergeExpectedOperation(installation, intent)
	var pull *issues_model.PullRequest
	var merger *user_model.User
	var receiptJSON string
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
		raw, err := json.Marshal(receipt)
		if err != nil {
			return false, err
		}
		receiptJSON = string(raw)
		if err := nativeoperation_model.ClaimHumanMergeDispatch(tx, want, held.Owner, held.Generation, held.Scope, startedJSON, receiptJSON); err != nil {
			return false, err
		}
		pull, merger = pr, actor
		return false, nil
	})
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	// No SQL transaction spans native notification/issue-status dispatch.
	finishErr := pull_service.FinishMerge(ctx, pull, merger, false)
	recorded, err := nativeoperation_model.RecordHumanMergeDispatch(context.WithoutCancel(ctx), want, held.Owner, held.Generation, startedJSON, doneJSON, receiptJSON, finishErr == nil)
	if err != nil {
		return sdk.OperationRecord{}, errors.Join(err, finishErr)
	}
	return ToRecord(recorded), nil
}

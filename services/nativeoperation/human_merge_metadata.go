// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	user_model "forgejo.org/models/user"
	pull_service "forgejo.org/services/pull"
)

// ApplyHumanMergeMetadata consumes the exact committed ref receipt, never a new
// merge grant. It applies native PR/issue/history/counter/schedule SQL once. The
// primary receipt, completion-pending state, owner and capability stay intact for
// notification/cross-reference completion and interrupted-writer reconciliation.
func (s *Service) ApplyHumanMergeMetadata(ctx context.Context, installation string, q sdk.HumanMergeRequest) (bool, error) {
	return s.withHumanMergeReceipt(ctx, installation, q, func(tx context.Context, receipt sdk.HumanMergeReceipt, pr *issues_model.PullRequest, merger *user_model.User) (bool, error) {
		return pull_service.ApplyMergeMetadata(tx, pr.ID, merger, receipt.ResultOID, receipt.MergeBaseOID)
	})
}

// All receipt-consuming SQL bookkeeping shares the same native execution,
// immutable evidence, scope/operation locks and exact current PR identity.
func (s *Service) withHumanMergeReceipt(ctx context.Context, installation string, q sdk.HumanMergeRequest, apply func(context.Context, sdk.HumanMergeReceipt, *issues_model.PullRequest, *user_model.User) (bool, error)) (bool, error) {
	intent, held, scope, err := readHumanMergeHeld(ctx, installation, q)
	if err != nil {
		return false, err
	}
	op, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return false, err
	}
	if op == nil {
		return false, ErrInvalidIntent
	}
	var receipt sdk.HumanMergeReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || !sdk.ValidHumanMergeReceipt(receipt, q) || receipt.ResultOID != scope.NewOID {
		return false, ErrHumanMergePushEvidence
	}
	raw, err := json.Marshal(receipt)
	if err != nil || string(raw) != op.Receipt {
		return false, ErrHumanMergePushEvidence
	}
	return nativeoperation_model.ApplyHumanMergeMetadata(ctx, humanMergeExpectedOperation(installation, intent), held.Owner, held.Generation, held.Scope, op.Receipt, func(tx context.Context, current *nativeoperation_model.Operation) (bool, error) {
		// The primary receipt is immutable historical truth. Metadata completion
		// additionally needs its exact current target/object evidence; divergence
		// refuses bookkeeping without undoing that primary truth.
		observed, err := s.observeHumanMergeEffect(tx, current, q, scope)
		if err != nil {
			return false, err
		}
		if observed.EffectState != nativeoperation_model.EffectCommitted || observed.Receipt != current.Receipt {
			return false, ErrHumanMergePushEvidence
		}
		pr, err := issues_model.GetPullRequestByID(tx, receipt.PullID)
		if err != nil {
			return false, err
		}
		if err := pr.LoadIssue(tx); err != nil {
			return false, err
		}
		if pr.BaseRepoID != receipt.RepositoryID || pr.IssueID != receipt.IssueID || pr.Index != receipt.Index || pr.BaseBranch != receipt.BaseBranch || !pr.Issue.IsPull || pr.Issue.RepoID != receipt.RepositoryID || pr.Issue.Index != receipt.Index {
			return false, pull_service.ErrMergeMetadata
		}
		merger, err := user_model.GetUserByID(tx, receipt.ActorID)
		if err != nil {
			return false, err
		}
		return apply(tx, receipt, pr, merger)
	})
}

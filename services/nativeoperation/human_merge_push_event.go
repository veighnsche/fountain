// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	repo_module "forgejo.org/modules/repository"
	pull_service "forgejo.org/services/pull"
)

const HumanMergePushEventStarted = "human-merge-push-event-started"
const HumanMergePushEventDone = "human-merge-push-event-done"

var (
	ErrHumanMergePushEventNotReady = errors.New("native human merge finishing is not complete")
	ErrHumanMergePushEventClaimed  = errors.New("native human merge push event was already claimed")
)

// DispatchHumanMergePushEvent binds a single native push event to the exact
// committed receipt after finishing. The server supplies the existing native
// engine as dispatch to avoid a service import cycle. No event fact or execution
// authority comes from that callback. A claim commits before dispatch; uncertain
// results stay claimed without automatic resend. Overall completion/owner release
// and stopped-writer recovery remain separate consumers.
func (s *Service) DispatchHumanMergePushEvent(ctx context.Context, installation string, q sdk.HumanMergeRequest, dispatch func(context.Context, *repo_model.Repository, []*repo_module.PushUpdateOptions) error) (sdk.OperationRecord, error) {
	if dispatch == nil || db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, held, scope, err := readHumanMergeHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if scope.CompletionPhase == HumanMergePushEventStarted || scope.CompletionPhase == HumanMergePushEventDone {
		return sdk.OperationRecord{}, ErrHumanMergePushEventClaimed
	}
	if scope.CompletionPhase != HumanMergeFinishDone {
		return sdk.OperationRecord{}, ErrHumanMergePushEventNotReady
	}
	started := scope
	started.CompletionPhase = HumanMergePushEventStarted
	startedJSON, err := encodeScope(started)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	done := scope
	done.CompletionPhase = HumanMergePushEventDone
	doneJSON, err := encodeScope(done)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	want := humanMergeExpectedOperation(installation, intent)
	var repository *repo_model.Repository
	var update *repo_module.PushUpdateOptions
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
		repository, err = repo_model.GetRepositoryByID(tx, receipt.RepositoryID)
		if err != nil {
			return false, err
		}
		if err := repository.LoadOwner(tx); err != nil {
			return false, err
		}
		update = &repo_module.PushUpdateOptions{
			RefFullName: git.RefNameFromBranch(receipt.BaseBranch), OldCommitID: receipt.BaseOID, NewCommitID: receipt.ResultOID,
			PusherID: receipt.ActorID, PusherName: actor.Name, RepoUserName: repository.Owner.Name, RepoName: repository.Name,
			TimeNano: time.Now().UnixNano(),
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return false, err
		}
		receiptJSON = string(raw)
		return false, nativeoperation_model.ClaimHumanMergeDispatch(tx, want, held.Owner, held.Generation, held.Scope, startedJSON, receiptJSON)
	})
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	// Native event/queue/notifier effects do not share the claim's transaction.
	dispatchErr := dispatch(ctx, repository, []*repo_module.PushUpdateOptions{update})
	recorded, err := nativeoperation_model.RecordHumanMergeDispatch(context.WithoutCancel(ctx), want, held.Owner, held.Generation, startedJSON, doneJSON, receiptJSON, dispatchErr == nil)
	if err != nil {
		return sdk.OperationRecord{}, errors.Join(err, dispatchErr)
	}
	return ToRecord(recorded), nil
}

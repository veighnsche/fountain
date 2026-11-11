// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"

	sdk "forgejo.org/extension-sdk"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	repo_module "forgejo.org/modules/repository"
	pull_service "forgejo.org/services/pull"

	"github.com/google/uuid"
)

// Recovery runs only through Recover's stopped/inhibited-domain contract. It
// never pushes refs, re-admits a human or replays notification/push dispatch.
// Missing/partial dispatch is explicit intervention, not completed delivery.
func (s *Service) recoverHumanMerge(ctx context.Context, a *RecoveryAssessment, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanMergeRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human merge intent is missing")
	}
	q := saved.Payload
	intent, err := humanMergeIntent(q)
	installation, parseErr := uuid.Parse(op.InstallationID)
	if err != nil || parseErr != nil || installation.String() != op.InstallationID || !humanMergeCaptureMatches(op, intent) || held.OwnerKind != nativeoperation_model.OwnerConditional || held.Generation < 1 || held.Revision < 1 || held.Revision-1 != op.ExpectedNativeRevision || checkHumanMergeScope(q, scope, held.Scope) != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "human merge scope and immutable intent differ")
	}
	pr, err := issues_model.GetPullRequestByID(ctx, nativeCommentID(q.Expected.PullID))
	if err != nil || pr.LoadIssue(ctx) != nil || pr.BaseRepoID != op.RepositoryID || pr.IssueID != scope.IssueID || pr.Index != scope.PRNumber || pr.BaseBranch != q.Expected.BaseBranch || !pr.Issue.IsPull || pr.Issue.RepoID != op.RepositoryID || pr.Issue.Index != pr.Index {
		return fenced(a, ReasonRecoveryMissingEvidence, "human merge native parent identity differs")
	}
	repository, err := repo_model.GetRepositoryByID(ctx, op.RepositoryID)
	if err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human merge repository is missing")
	}
	observed, err := s.observeHumanMergeEffect(ctx, op, q, scope)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	if observed.EffectState == nativeoperation_model.EffectIndeterminate || op.IsTerminal() && op.EffectState != observed.EffectState {
		return fenced(a, ReasonRecoveryUncertainEffect, "human merge admission, current ref and saved outcome do not agree")
	}
	if scope.CompletionPhase != "" && (op.EffectState != nativeoperation_model.EffectCommitted || op.Receipt == "") {
		return fenced(a, ReasonRecoveryUncertainEffect, "human merge dispatch phase has no saved committed receipt")
	}
	if !op.IsTerminal() && (op.Receipt != "" || op.Completion != "") || op.Completion == nativeoperation_model.CompletionNeedsIntervention && scope.CompletionPhase == "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human merge bookkeeping has no attributable primary phase")
	}
	if observed.EffectState == nativeoperation_model.EffectNotCommitted {
		if pr.HasMerged || op.Receipt != "" || op.Completion != "" || scope.CompletionPhase != "" {
			return fenced(a, ReasonRecoveryUncertainEffect, "human merge no-effect evidence has conflicting bookkeeping")
		}
		if err := retireHumanMergeRefLock(ctx, repository, scope, op, held); err != nil {
			return fenced(a, ReasonRecoveryMissingEvidence, "stopped human merge target lock differs or could not be retired")
		}
		if _, err := nativeoperation_model.RecordHumanMergeEffect(ctx, humanMergeExpectedOperation(op.InstallationID, intent), held.Owner, held.Generation, held.Scope, func(context.Context, *nativeoperation_model.Operation) (nativeoperation_model.TerminalOutcome, error) {
			if op.Reason == "" && !op.Revoked {
				observed.Reason = nativeoperation_model.ReasonRecoveredNoEffect
			}
			return observed, nil
		}); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, held, nativeoperation_model.EffectNotCommitted)
	}
	if observed.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human merge primary effect is unclassified")
	}
	if op.EffectState == nativeoperation_model.EffectCommitted && (op.Receipt != observed.Receipt || op.Completion != nativeoperation_model.CompletionPending && op.Completion != nativeoperation_model.CompletionComplete && op.Completion != nativeoperation_model.CompletionNeedsIntervention) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human merge exact receipt or completion differs")
	}
	var receipt sdk.HumanMergeReceipt
	if json.Unmarshal([]byte(observed.Receipt), &receipt) != nil || !sdk.ValidHumanMergeReceipt(receipt, q) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human merge committed receipt cannot be reconstructed")
	}
	metadataMatches := pr.HasMerged && pr.Issue.IsClosed && pr.MergedCommitID == receipt.ResultOID && pr.MergerID == receipt.ActorID && pr.MergeBase == receipt.MergeBaseOID && pr.MergedUnix > 0
	if pr.HasMerged && !metadataMatches || !pr.HasMerged && pr.Issue.IsClosed || scope.CompletionPhase != "" && !metadataMatches || op.Completion == nativeoperation_model.CompletionComplete && scope.CompletionPhase != HumanMergePushEventDone {
		return fenced(a, ReasonRecoveryMissingEvidence, "human merge metadata contradicts the recorded completion phase")
	}
	if scope.CompletionPhase != "" {
		branch, err := git_model.GetBranch(ctx, receipt.RepositoryID, receipt.BaseBranch)
		if err != nil || branch.IsDeleted || branch.CommitID != receipt.ResultOID {
			return fenced(a, ReasonRecoveryMissingEvidence, "human merge dispatched branch evidence differs")
		}
	}
	actor, err := user_model.GetUserByID(ctx, receipt.ActorID)
	if err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human merge original actor is missing")
	}
	if err := retireHumanMergeRefLock(ctx, repository, scope, op, held); err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "stopped human merge target lock differs or could not be retired")
	}
	op, err = nativeoperation_model.RecordHumanMergeEffect(ctx, humanMergeExpectedOperation(op.InstallationID, intent), held.Owner, held.Generation, held.Scope, func(context.Context, *nativeoperation_model.Operation) (nativeoperation_model.TerminalOutcome, error) {
		return observed, nil
	})
	if err != nil {
		return RecoveryAssessment{}, err
	}
	owned := execcontext.NewContext(ctx, &execcontext.Execution{Owner: held.Owner, Generation: held.Generation})
	if op.Completion == nativeoperation_model.CompletionPending {
		_, err = nativeoperation_model.ApplyHumanMergeMetadata(owned, humanMergeExpectedOperation(op.InstallationID, intent), held.Owner, held.Generation, held.Scope, op.Receipt, func(tx context.Context, _ *nativeoperation_model.Operation) (bool, error) {
			changed, err := pull_service.ApplyMergeMetadata(tx, pr.ID, actor, receipt.ResultOID, receipt.MergeBaseOID)
			if err != nil {
				return false, err
			}
			native, err := git.OpenRepository(tx, repository.RepoPath())
			if err != nil {
				return false, err
			}
			defer native.Close()
			return changed, repo_module.SyncBranchesToDB(tx, repository.ID, actor.ID, []string{receipt.BaseBranch}, native.GetBranchCommit)
		})
		if err != nil {
			return RecoveryAssessment{}, err
		}
		completion := nativeoperation_model.CompletionNeedsIntervention
		if scope.CompletionPhase == HumanMergePushEventDone {
			completion = nativeoperation_model.CompletionComplete
		}
		if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, held.Owner, held.Generation, completion); err != nil {
			return RecoveryAssessment{}, err
		}
		a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
		a.Checks = append(a.Checks, "exact admitted merge receipt established; native SQL metadata/catalog reconciled; no ref or notification/push dispatch replayed")
		s.retireStaleCapabilities()
		return *a, nil
	}
	return s.releaseRecovered(ctx, a, held, nativeoperation_model.EffectCommitted)
}

// Only offline recovery may retire this exact stopped writer's files-backend
// ref lock. Admission/ref/SQL evidence must already establish a known outcome.
// An admitted old target never reaches here. Unknown locks stay fenced rather
// than blocking a newly admitted writer after ownership has been released.
func retireHumanMergeRefLock(ctx context.Context, repository *repo_model.Repository, scope Scope, op *nativeoperation_model.Operation, held *nativeoperation_model.Reservation) error {
	return git.RetireStoppedRefLock(ctx, repository.RepoPath(), git.RefLockRetirementProof{
		StoppedDomain:  nativeoperation_model.OfflineInhibited(),
		InstallationID: op.InstallationID, OperationID: op.OperationID, Owner: held.Owner, Generation: held.Generation,
		Ref: scope.Ref, NewOID: scope.NewOID, ObjectFormat: repository.ObjectFormatName,
	})
}

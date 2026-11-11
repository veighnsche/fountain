// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"strings"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/gitrepo"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"
)

// submitMerge claims the operation ID and reservation, executes one native
// fast-forward-only merge under the held owner, and reconciles the
// attributable result. There is no unguarded fallback: every refusal path
// records not_committed or retains the fence.
func (s *Service) submitMerge(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent) (sdk.OperationRecord, error) {
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if intent.Merge == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := Scope{
		Kind:         nativeoperation_model.OwnerConditional,
		RepositoryID: intent.RepositoryID,
		Ref:          intent.Merge.BaseRef,
		OldOID:       intent.Merge.ExpectedBaseOID,
		NewOID:       intent.Merge.ExpectedHeadOID,
		HeadRef:      intent.Merge.HeadRef,
		HeadOID:      intent.Merge.ExpectedHeadOID,
		PRNumber:     intent.Merge.PullRequestNumber,
	}
	return s.claimBackgroundSubmit(ctx, decision, installationID, intent, scope)
}

func (s *Service) recordStaleRevision(ctx context.Context, op *nativeoperation_model.Operation) (sdk.OperationRecord, error) {
	op.EffectState = nativeoperation_model.EffectNotCommitted
	op.Reason = nativeoperation_model.ReasonStaleNativeRevision
	op.Cancellation = nativeoperation_model.CancellationNone
	recorded, err := nativeoperation_model.InsertRefusedOperation(ctx, op)
	if err != nil && !errors.Is(err, nativeoperation_model.ErrDuplicateOperation) {
		return sdk.OperationRecord{}, err
	}
	if errors.Is(err, nativeoperation_model.ErrDuplicateOperation) {
		return s.reconcileExisting(recorded, &ValidIntent{Digest: op.IntentDigest})
	}
	return ToRecord(recorded), nil
}

type terminalResult struct {
	retain bool
}

// Retained reports whether the owner stays held for an unresolved effect.
func (t terminalResult) Retained() bool {
	return t.retain
}

// mergeResult carries the exact-merge outcome into reconciliation: the
// realized base SHA on success, the engine-error hint reason when the tip
// evidence shows no effect, and whether a post-write result mismatch made
// the outcome unattributable from the engine's report alone.
type mergeResult struct {
	realized       string
	hint           string
	unattributable bool
}

// executeMerge runs the guarded merge and reconciles its result. It returns
// the SDK record and whether the owner stays held for an unresolved effect.
// All native work runs on the owned context so nested participating writers
// attribute to this execution; terminal records and the owner release run
// detached so a caller disconnect cannot stick the reservation.
func (s *Service) executeMerge(ctx context.Context, op *nativeoperation_model.Operation, intent *ValidIntent, scope Scope, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	merge := intent.Merge
	// Terminal records and the owner release must complete even when the
	// caller disconnects mid-write; native work below keeps the live
	// context so cancellation still stops it.
	releaseCtx := context.WithoutCancel(ctx)
	owned := execcontext.NewContext(ctx, execution)
	refuse := conditionalRefusal(ctx, op, execution)
	repository, err := repo_model.GetRepositoryByID(owned, intent.RepositoryID)
	if err != nil {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	if s.clock() >= op.NotAfter {
		return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
	}
	doer, err := user_model.GetUserByID(owned, op.ActorID)
	if err != nil {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	pr, err := issues_model.GetPullRequestByIndex(owned, intent.RepositoryID, merge.PullRequestNumber)
	if err != nil {
		if issues_model.IsErrPullRequestNotExist(err) {
			return refuse(nativeoperation_model.ReasonPRMismatch)
		}
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	baseGitRepo, err := gitrepo.OpenRepository(owned, repository)
	if err != nil {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	defer baseGitRepo.Close()
	message, _, err := pull_service.GetDefaultMergeMessage(owned, baseGitRepo, pr, repo_model.MergeStyleFastForwardOnly)
	if err != nil {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	// Test-only barrier for the claim/cancel race proof: with the claim
	// held and the native merge not started, the driver revokes the
	// operation here so prepared admission below must observe the
	// cancellation and refuse.
	if err := TestCrashBarrier(CrashPointMergeBeforeNative); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	// The exact entry re-verifies the live candidate under the held owner
	// and runs the native engine: deterministic pre-write refusals map to
	// bounded reasons, while engine outcomes reconcile from admission and
	// the authoritative tip, never from the error alone.
	realized, mergeErr := pull_service.MergeExactFastForward(owned, pr, doer, baseGitRepo,
		merge.HeadRef, merge.BaseRef, merge.ExpectedHeadOID, merge.ExpectedBaseOID, message)
	if mergeErr != nil {
		var refused pull_service.ErrExactMergeRefused
		exact := errors.As(mergeErr, &refused)
		if exact && refused.Reason != pull_service.ExactMergeRefusedResultMismatch {
			// Deterministic pre-write refusal: the engine never
			// ran, no admission was recorded and no ref moved.
			return refuse(mapExactMergeRefusal(mergeErr))
		}
		// A post-write result mismatch is unattributable from the
		// engine's report: only a tip that proves the exact effect
		// may still commit, and any other outcome stays fenced.
		result := mergeResult{hint: mapMergeEngineError(mergeErr), unattributable: exact}
		// Test-only crash barrier for offline-recovery proof: with
		// the native merge done and the owner still held, the
		// driver fails here to simulate a crash before
		// reconciliation.
		if err := TestCrashBarrier(CrashPointMergeAfterNative); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return s.reconcileMerge(releaseCtx, op, scope, repository, execution, result)
	}
	// Test-only crash barrier for offline-recovery proof: with the native
	// merge done and the owner still held, the driver fails here to
	// simulate a crash before reconciliation.
	if err := TestCrashBarrier(CrashPointMergeAfterNative); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return s.reconcileMerge(releaseCtx, op, scope, repository, execution, mergeResult{realized: realized})
}

// MergeReceipt attributes one committed merge to its operation.
type MergeReceipt struct {
	OldOID       string `json:"old_oid"`
	NewOID       string `json:"new_oid"`
	ActorID      int64  `json:"actor_id"`
	RepositoryID int64  `json:"repository_id"`
	PRNumber     int64  `json:"pr_number"`
	PRID         int64  `json:"pr_id"`
	HeadRef      string `json:"head_ref"`
	BaseRef      string `json:"base_ref"`
	Method       string `json:"method"`
}

// reconcileMerge resolves the write after writer quiescence. The merge
// engine's error alone never decides: attribution comes from the recorded
// admission plus the authoritative ref tip, bound to the realized SHA when
// the engine reported one. A known effect releases the owner; an unknown
// effect retains the fence.
func (s *Service) reconcileMerge(ctx context.Context, op *nativeoperation_model.Operation, scope Scope, repository *repo_model.Repository, execution *execcontext.Execution, result mergeResult) (sdk.OperationRecord, terminalResult, error) {
	owner := execution.Owner
	fresh, err := nativeoperation_model.LookupOperation(ctx, op.InstallationID, op.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if fresh == nil {
		return sdk.OperationRecord{}, terminalResult{}, errors.New("operation vanished during execution")
	}
	tip, tipErr := s.readRef(ctx, repository.RepoPath(), scope.Ref)
	if tipErr != nil {
		recorded, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState:  nativeoperation_model.EffectIndeterminate,
			Cancellation: nativeoperation_model.CancellationNone,
		}, "")
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(recorded), terminalResult{retain: true}, nil
	}
	tip = strings.ToLower(tip)
	committed := fresh.Admitted && tip == scope.NewOID && tip != scope.OldOID &&
		(result.realized == "" || strings.EqualFold(tip, result.realized))
	if committed {
		receipt, _ := json.Marshal(MergeReceipt{
			OldOID:       scope.OldOID,
			NewOID:       scope.NewOID,
			ActorID:      op.ActorID,
			RepositoryID: op.RepositoryID,
			PRNumber:     scope.PRNumber,
			PRID:         mergedPRID(ctx, repository.ID, scope.PRNumber),
			HeadRef:      scope.HeadRef,
			BaseRef:      scope.Ref,
			Method:       "fast-forward-only",
		})
		completion := nativeoperation_model.CompletionNeedsIntervention
		if prMergedAt(ctx, repository.ID, scope.PRNumber, tip) {
			completion = nativeoperation_model.CompletionComplete
		}
		recorded, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState:  nativeoperation_model.EffectCommitted,
			Cancellation: nativeoperation_model.CancellationNone,
			Completion:   completion,
			Receipt:      string(receipt),
		}, owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(recorded), terminalResult{}, nil
	}
	if result.unattributable || fresh.Admitted || (tip == scope.NewOID && tip != scope.OldOID) {
		// An unattributable engine report, an admission the tip
		// disagrees with, or a tip that moved without admission: the
		// effect cannot be attributed, so it stays fenced.
		recorded, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState:  nativeoperation_model.EffectIndeterminate,
			Cancellation: nativeoperation_model.CancellationNone,
		}, "")
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(recorded), terminalResult{retain: true}, nil
	}
	reason := fresh.Reason
	if reason == "" {
		reason = result.hint
	}
	if reason == "" {
		reason = nativeoperation_model.ReasonNativeRefused
	}
	recorded, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
		EffectState:  nativeoperation_model.EffectNotCommitted,
		Reason:       reason,
		Cancellation: nativeoperation_model.CancellationNone,
	}, owner)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(recorded), terminalResult{}, nil
}

func mergedPRID(ctx context.Context, repoID, number int64) int64 {
	pr, err := issues_model.GetPullRequestByIndex(ctx, repoID, number)
	if err != nil || pr == nil {
		return 0
	}
	return pr.ID
}

func prMergedAt(ctx context.Context, repoID, number int64, tip string) bool {
	pr, err := issues_model.GetPullRequestByIndex(ctx, repoID, number)
	if err != nil || pr == nil {
		return false
	}
	return pr.HasMerged && strings.EqualFold(pr.MergedCommitID, tip)
}

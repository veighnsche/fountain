// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"errors"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"

	"github.com/google/uuid"
)

// Recover invokes this only for a stopped/inhibited mutation domain. SQL primary
// and receipt are atomic; a similar PR never supplies a missing receipt. Git is
// inspected before the atomic SQL outcome/release, with no transaction across it.
func (s *Service) recoverHumanPullCreate(ctx context.Context, a *RecoveryAssessment, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	if db.InTransaction(ctx) {
		return RecoveryAssessment{}, ErrInvalidIntent
	}
	var saved struct {
		Payload sdk.HumanPullCreateRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human PR creation intent is missing")
	}
	q := saved.Payload
	v, err := humanPullCreateIntent(q)
	installation, parseErr := uuid.Parse(op.InstallationID)
	if err != nil || parseErr != nil || installation.String() != op.InstallationID || !humanMergeCaptureMatches(op, v) || held.OwnerKind != nativeoperation_model.OwnerConditional || held.Generation < 1 || held.Revision != op.ExpectedNativeRevision+1 {
		return fenced(a, ReasonRecoveryUncertainEffect, "human PR creation capture and held generation differ")
	}
	base, err := repo_model.GetRepositoryByID(ctx, v.RepositoryID)
	if err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human PR creation target is missing")
	}
	head, err := repo_model.GetRepositoryByID(ctx, nativeCommentID(q.Expected.HeadRepositoryID))
	if err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human PR creation source is missing")
	}
	if _, err := user_model.GetUserByID(ctx, v.ActorID); err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human PR creation original actor is missing")
	}
	pending := !op.Admitted && (op.EffectState == nativeoperation_model.EffectPending || op.EffectState == nativeoperation_model.EffectNotCommitted) && op.Receipt == "" && op.Completion == "" && scope.CompletionPhase == ""
	var pr *issues_model.PullRequest
	if pending {
		pr = &issues_model.PullRequest{BaseRepo: base, HeadRepo: head}
	} else {
		if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
			return fenced(a, ReasonRecoveryUncertainEffect, "human PR creation has no atomic committed receipt")
		}
		pr, err = readHumanPullCreateReceipt(ctx, q, op.Receipt)
		if err != nil {
			return fenced(a, ReasonRecoveryMissingEvidence, "human PR creation receipt or native primary differs; preserved")
		}
	}
	if checkHumanPullCreateScope(q, scope, held.Scope, pr) != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "human PR creation scope differs from its immutable capture")
	}
	if err := checkHumanPullCreateTips(ctx, q, pr); err != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "human PR creation source or target Git evidence differs")
	}
	// No scope is permitted to invent a derived ref before the primary. A
	// prepared writer may have stopped before or after its one exact CAS.
	if !pending {
		tip, absent, known := s.prCreateRefTip(ctx, base.RepoPath(), pr.GetGitRefName())
		if !known || !absent && tip != q.Expected.HeadOID || scope.CompletionPhase == "" && !absent || scope.CompletionPhase != "" && scope.CompletionPhase != HumanPullCreateRefPrepared && absent {
			return fenced(a, ReasonRecoveryUncertainEffect, "human PR creation derived ref contradicts the recorded phase")
		}
	}
	// Successful native finishing necessarily wrote this exact initial push
	// history (unless the comparison is empty). Read Git now; validate SQL in
	// the release transaction. This neither recomputes nor dispatches reviews.
	var history string
	if scope.CompletionPhase == HumanPullCreateFinishDone {
		history, err = humanPullCreateRecoveryHistory(ctx, pr)
		if err != nil {
			return fenced(a, ReasonRecoveryMissingEvidence, "human PR creation finished comparison cannot be read")
		}
	}
	recorded, err := nativeoperation_model.RecoverHumanPullCreate(ctx, humanPullCreateOperation(op.InstallationID, v), held.Owner, held.Generation, held.Scope, func(tx context.Context, current *nativeoperation_model.Operation) (string, error) {
		if current.Admitted != op.Admitted || current.EffectState != op.EffectState || current.Completion != op.Completion || current.Receipt != op.Receipt {
			return "", ErrHumanPullCreateEvidence
		}
		if pending {
			if err := humanPullCreateDuplicate(tx, q); err != nil {
				return "", ErrHumanPullCreateEvidence
			}
			return "", nil
		}
		if _, err := readHumanPullCreateReceipt(tx, q, current.Receipt); err != nil {
			return "", err
		}
		if scope.CompletionPhase == HumanPullCreateFinishDone {
			var comments []*issues_model.Comment
			if err := db.GetEngine(tx).Where("issue_id=? AND type=?", pr.IssueID, issues_model.CommentTypePullRequestPush).Find(&comments); err != nil {
				return "", err
			}
			if history == "" && len(comments) != 0 || history != "" && (len(comments) != 1 || comments[0].PosterID != v.ActorID || comments[0].Content != history) {
				return "", ErrHumanPullCreateEvidence
			}
		}
		switch current.Completion {
		case nativeoperation_model.CompletionPending:
			if scope.CompletionPhase == HumanPullCreateFinishDone {
				return nativeoperation_model.CompletionComplete, nil
			}
			return nativeoperation_model.CompletionNeedsIntervention, nil
		case nativeoperation_model.CompletionComplete:
			if scope.CompletionPhase != HumanPullCreateFinishDone {
				return "", ErrHumanPullCreateEvidence
			}
			return current.Completion, nil
		case nativeoperation_model.CompletionNeedsIntervention:
			if scope.CompletionPhase == HumanPullCreateFinishDone {
				return "", ErrHumanPullCreateEvidence
			}
			return current.Completion, nil
		default:
			return "", ErrHumanPullCreateEvidence
		}
	})
	if err != nil {
		if errors.Is(err, ErrHumanPullCreateEvidence) || errors.Is(err, nativeoperation_model.ErrAdmissionLost) || errors.Is(err, nativeoperation_model.ErrWrongOwner) || issues_model.IsErrPullRequestNotExist(err) || issues_model.IsErrIssueNotExist(err) {
			return fenced(a, ReasonRecoveryUncertainEffect, "human PR creation native evidence or atomic outcome differs; preserved")
		}
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, recorded.EffectState
	a.Checks = append(a.Checks, "exact human PR capture and native SQL/Git evidence reconciled; no primary, ref or notification replay")
	s.retireStaleCapabilities()
	return *a, nil
}

func humanPullCreateRecoveryHistory(ctx context.Context, pr *issues_model.PullRequest) (string, error) {
	native, err := git.OpenRepository(ctx, pr.BaseRepo.RepoPath())
	if err != nil {
		return "", err
	}
	defer native.Close()
	comparison, err := native.GetCompareInfo(pr.BaseRepo.RepoPath(), git.BranchPrefix+pr.BaseBranch, pr.GetGitRefName(), false, false)
	if err != nil || len(comparison.Commits) == 0 {
		return "", err
	}
	data := issues_model.PushActionContent{IsForcePush: false, CommitIDs: make([]string, 0, len(comparison.Commits))}
	for i := len(comparison.Commits) - 1; i >= 0; i-- {
		data.CommitIDs = append(data.CommitIDs, comparison.Commits[i].ID.String())
	}
	raw, err := json.Marshal(data)
	return string(raw), err
}

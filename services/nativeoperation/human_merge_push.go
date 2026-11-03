// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"
)

var ErrHumanMergePushEvidence = errors.New("human merge push evidence is incomplete or differs")

// HumanMergePushObservation contains native evidence after the synchronous Git
// call for the future journal reconciler. An interrupted writer still requires
// quiescence/recovery qualification. A successful Git exit is not a receipt;
// attribution needs the saved admission and exact tip, plus native completion.
type HumanMergePushObservation struct {
	Result      pull_service.PreparedMergeResult
	Attempted   bool
	Admitted    bool
	ObservedOID string
}

// PushHumanMergeOperation binds and pushes one already journaled, staged human
// command through native hooks. It never releases the owner or capability,
// assigns an effect/completion state, or retries an uncertain writer. The caller
// MUST reconcile the retained operation before releasing it, and Close the stage.
func (s *Service) PushHumanMergeOperation(ctx context.Context, installation string, q sdk.HumanMergeRequest, stage *pull_service.PreparedExactMerge, admit HumanReviewAdmission) (observation HumanMergePushObservation, err error) {
	if stage == nil || admit == nil {
		return observation, ErrInvalidIntent
	}
	intent, err := humanMergeIntent(q)
	if err != nil {
		return observation, err
	}
	observation.Result = stage.Result()
	if observation.Result.CommitID == q.Expected.BaseOID {
		return observation, pull_service.ErrExactMergeRefused{Reason: pull_service.ExactMergeRefusedNoOp}
	}
	execution := execcontext.FromContext(ctx)
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return observation, err
	}
	if execution == nil || execution.Owner != held.Owner || execution.Generation != held.Generation {
		return observation, nativeoperation_model.ErrWrongOwner
	}
	proof, err := execcontext.ReadCapabilityFile(execution.CapabilityPath)
	if err != nil || !execcontext.VerifyProof(held.Verifier, proof) {
		return observation, nativeoperation_model.ErrWrongOwner
	}
	pr, actor, err := checkHumanMergeTarget(ctx, intent.ActorID, q.Expected, admit)
	if err != nil {
		return observation, err
	}
	if err := pull_service.CheckPreparedMergeHooks(ctx, pr.BaseRepo); err != nil {
		return observation, err
	}
	live, cancel := context.WithDeadline(ctx, time.Unix(q.NotAfter, 0))
	defer cancel()
	retire, err := s.BindHumanMergeStage(live, installation, q, stage, admit)
	if err != nil {
		return observation, err
	}
	defer retire() // The synchronous Git child and its hooks quiesce first.
	bound := humanMergeCaptureScope(q)
	bound.NewOID = observation.Result.CommitID
	encoded, err := encodeScope(bound)
	if err != nil {
		return observation, err
	}
	guard := func() error {
		if err := live.Err(); err != nil {
			return err
		}
		// Current session/native policy/source are re-read after LFS too. The
		// prepared hook performs its own final check under locked ref inputs.
		if _, _, err := checkHumanMergeTarget(live, intent.ActorID, q.Expected, admit); err != nil {
			return err
		}
		return s.checkHumanMergeCapture(live, installation, intent, encoded)
	}
	observation.Attempted, err = stage.PushExact(live, pr, actor, repo_model.MergeStyle(q.Expected.Method), q.Expected.HeadOID, q.Expected.BaseOID, q.Message, guard)
	// Preserve pending truth on every error; a failed/lost reply and an old tip
	// never prove non-commit or quiescence of an interrupted process tree.
	readback := context.WithoutCancel(ctx)
	current, readErr := nativeoperation_model.LookupOperation(readback, installation, q.OperationID)
	if readErr == nil && current != nil {
		observation.Admitted = current.Admitted
	} else if readErr == nil {
		readErr = ErrHumanMergePushEvidence
	}
	observation.ObservedOID, readErr = readHumanMergeTip(readback, s, intent.RepositoryID, bound.Ref, readErr)
	if err == nil && readErr == nil && (!observation.Admitted || observation.ObservedOID != observation.Result.CommitID) {
		readErr = ErrHumanMergePushEvidence
	}
	return observation, errors.Join(err, readErr)
}

func readHumanMergeTip(ctx context.Context, s *Service, repositoryID int64, ref string, earlier error) (string, error) {
	if earlier != nil {
		return "", earlier
	}
	repository, err := repo_model.GetRepositoryByID(ctx, repositoryID)
	if err != nil {
		return "", err
	}
	return s.readRef(ctx, repository.RepoPath(), ref)
}

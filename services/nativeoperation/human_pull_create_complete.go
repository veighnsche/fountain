// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"os"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
	issue_service "forgejo.org/services/issue"
	notify_service "forgejo.org/services/notify"
	pull_service "forgejo.org/services/pull"
)

const (
	HumanPullCreateFinishStarted = "human-pull-create-finish-started"
	HumanPullCreateFinishDone    = "human-pull-create-finish-done"
)

var (
	ErrHumanPullCreateFinishClaimed      = errors.New("native human pull creation finishing was already claimed")
	ErrHumanPullCreateCompletionNotReady = errors.New("native human pull creation completion is not ready")
)

func (s *Service) checkHumanPullCreateRefEvidence(ctx context.Context, q sdk.HumanPullCreateRequest, pr *issues_model.PullRequest) error {
	if err := checkHumanPullCreateTips(ctx, q, pr); err != nil {
		return err
	}
	tip, absent, known := s.prCreateRefTip(ctx, pr.BaseRepo.RepoPath(), pr.GetGitRefName())
	if !known || absent || tip != q.Expected.HeadOID {
		return ErrHumanPullCreateEvidence
	}
	return nil
}

// FinishHumanPullCreate durably claims ordinary native creation finishing once.
// It consumes committed SQL/ref truth, not renewed human creation authority.
// Git reads and native notification dispatch run outside the claim transaction;
// an interrupted or failed dispatch remains fenced and is never repeated here.
func (s *Service) FinishHumanPullCreate(ctx context.Context, installation string, q sdk.HumanPullCreateRequest) (sdk.OperationRecord, error) {
	if db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	v, held, scope, pr, op, err := readHumanPullCreateHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if scope.CompletionPhase == HumanPullCreateFinishStarted || scope.CompletionPhase == HumanPullCreateFinishDone {
		return sdk.OperationRecord{}, ErrHumanPullCreateFinishClaimed
	}
	if scope.CompletionPhase != HumanPullCreateRefDone {
		return sdk.OperationRecord{}, ErrHumanPullCreateCompletionNotReady
	}
	if err := s.checkHumanPullCreateRefEvidence(ctx, q, pr); err != nil {
		return sdk.OperationRecord{}, err
	}
	started, done := scope, scope
	started.CompletionPhase, done.CompletionPhase = HumanPullCreateFinishStarted, HumanPullCreateFinishDone
	startedJSON, err := encodeScope(started)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	doneJSON, err := encodeScope(done)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	want := humanPullCreateOperation(installation, v)
	err = nativeoperation_model.WithHumanPullCreateReceipt(ctx, want, held.Owner, held.Generation, held.Scope, op.Receipt, func(tx context.Context, current *nativeoperation_model.Operation) error {
		var err error
		pr, err = readHumanPullCreateReceipt(tx, q, current.Receipt)
		if err != nil {
			return err
		}
		if err := pr.Issue.LoadPoster(tx); err != nil {
			return err
		}
		_, err = nativeoperation_model.ChangeHumanPullCreatePhase(tx, want, held.Owner, held.Generation, held.Scope, startedJSON, current.Receipt)
		return err
	})
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	finishErr := func() error {
		if err := s.checkHumanPullCreateRefEvidence(ctx, q, pr); err != nil {
			return err
		}
		notifiers, err := pull_service.FinishPullRequestCreation(ctx, pr.BaseRepo, pr)
		if err != nil {
			return err
		}
		issue_service.ReviewRequestNotify(ctx, pr.Issue, pr.Issue.Poster, notifiers)
		mentions, err := issues_model.FindAndUpdateIssueMentions(ctx, pr.Issue, pr.Issue.Poster, pr.Issue.Content)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		notify_service.NewPullRequest(ctx, pr, mentions)
		if err := ctx.Err(); err != nil {
			return err
		}
		return s.checkHumanPullCreateRefEvidence(ctx, q, pr)
	}()
	var recorded *nativeoperation_model.Operation
	err = nativeoperation_model.WithHumanPullCreateReceipt(context.WithoutCancel(ctx), want, held.Owner, held.Generation, startedJSON, op.Receipt, func(tx context.Context, current *nativeoperation_model.Operation) error {
		if _, err := readHumanPullCreateReceipt(tx, q, current.Receipt); err != nil {
			return err
		}
		var err error
		recorded, err = nativeoperation_model.RecordHumanPullCreateFinish(tx, want, held.Owner, held.Generation, startedJSON, doneJSON, current.Receipt, finishErr == nil)
		return err
	})
	if err != nil {
		return sdk.OperationRecord{}, errors.Join(err, finishErr)
	}
	return ToRecord(recorded), finishErr
}

// CompleteHumanPullCreate releases only exact successfully finished creation.
// The SQL completion/release revokes proof before its private file is retired.
// Stopped-writer recovery is separate; retained lookup resolves lost replies.
func (s *Service) CompleteHumanPullCreate(ctx context.Context, installation string, q sdk.HumanPullCreateRequest) (sdk.OperationRecord, error) {
	if db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	v, held, scope, pr, op, err := readHumanPullCreateHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if scope.CompletionPhase != HumanPullCreateFinishDone {
		return sdk.OperationRecord{}, ErrHumanPullCreateCompletionNotReady
	}
	if err := s.checkHumanPullCreateRefEvidence(ctx, q, pr); err != nil {
		return sdk.OperationRecord{}, err
	}
	var recorded *nativeoperation_model.Operation
	want := humanPullCreateOperation(installation, v)
	err = nativeoperation_model.WithHumanPullCreateReceipt(ctx, want, held.Owner, held.Generation, held.Scope, op.Receipt, func(tx context.Context, current *nativeoperation_model.Operation) error {
		if _, err := readHumanPullCreateReceipt(tx, q, current.Receipt); err != nil {
			return err
		}
		var err error
		recorded, err = nativeoperation_model.CompleteHumanPullCreate(tx, want, held.Owner, held.Generation, held.Scope, current.Receipt)
		return err
	})
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	record := ToRecord(recorded)
	if err := os.Remove(execcontext.FromContext(ctx).CapabilityPath); err != nil && !os.IsNotExist(err) {
		return record, err
	}
	return record, nil
}

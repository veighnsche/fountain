// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"errors"

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
)

const (
	CrashPointHumanThreadBeforePrimary = "human-thread-before-primary"
	CrashPointHumanThreadAfterPrimary  = "human-thread-after-primary"
)

func humanThreadIntent(q sdk.HumanThreadRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanThreadRequest(q) {
		return nil, ErrInvalidIntent
	}
	p := q.Expected.Review
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(p.ActorID), RepositoryID: nativeCommentID(p.RepositoryID), Kind: nativeoperation_model.KindHumanThread, AuthRevision: sdk.HumanThreadAuthorizationRevision, ExpectedNativeRevision: p.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanThreadIntent(q)
	return intent, err
}

// SubmitHumanThread uses current human request admission, never background
// enrollment. Primary root state and exact before/after receipt commit together.
// Replays and early cancellation use the existing installation-bound journal.
func (s *Service) SubmitHumanThread(ctx context.Context, installation string, q sdk.HumanThreadRequest, admit HumanReviewAdmission) (sdk.OperationRecord, error) {
	submission, record, err := s.beginHumanSubmission(ctx, installation, admit, func() (*ValidIntent, error) { return humanThreadIntent(q) })
	if submission == nil {
		return record, err
	}
	intent := submission.intent
	scope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: intent.RepositoryID, IssueID: nativeCommentID(q.Expected.Review.IssueID)}
	return submission.run(scope, func(ctx context.Context, op *nativeoperation_model.Operation, _ *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
		return s.executeHumanThread(ctx, op, q, admit, execution)
	})
}

func (s *Service) executeHumanThread(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanThreadRequest, admit HumanReviewAdmission, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owned, release := execcontext.NewContext(ctx, execution), context.WithoutCancel(ctx)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		r, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(r), terminalResult{}, nil
	}
	if err := TestCrashBarrier(CrashPointHumanThreadBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	committed, err := nativeoperation_model.CommitHumanThreadPrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		gate := func(ctx context.Context) error {
			if s.clock() >= op.NotAfter {
				return nativeoperation_model.ErrOperationExpired
			}
			return admit(ctx)
		}
		result, err := ApplyPreparedHumanThread(tx, op.ActorID, q.Expected, q.Resolved, gate)
		if err != nil {
			return "", err
		}
		p := q.Expected.Review
		receipt := sdk.HumanThreadReceipt{RepositoryID: op.RepositoryID, ActorID: op.ActorID, PullID: nativeCommentID(p.PullID), IssueID: nativeCommentID(p.IssueID), Index: nativeCommentID(p.Index), Before: result.Before, After: result.After, Changed: result.Changed}
		if !sdk.ValidHumanThreadReceipt(receipt, q) {
			return "", errors.New("native human thread receipt differs")
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return "", err
		}
		if s.clock() >= op.NotAfter {
			return "", nativeoperation_model.ErrOperationExpired
		}
		return string(raw), nil
	})
	if err != nil {
		var deterministic issues_model.ErrExactReviewRefused
		switch {
		case errors.Is(err, ErrAuthorityLost):
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, ErrHumanThreadMissing), errors.Is(err, ErrHumanThreadTarget), errors.Is(err, ErrHumanThreadChanged), errors.Is(err, ErrHumanThreadTooLarge):
			return refuse(nativeoperation_model.ReasonStaleConversation)
		case errors.As(err, &deterministic):
			reason, retain := mapHumanThreadRefusal(deterministic)
			if retain {
				return sdk.OperationRecord{}, terminalResult{}, err
			}
			return refuse(reason)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			current, e := nativeoperation_model.LookupOperation(release, op.InstallationID, op.OperationID)
			if e != nil {
				return sdk.OperationRecord{}, terminalResult{}, e
			}
			if current != nil && current.Revoked {
				return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
			}
		}
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if err := TestCrashBarrier(CrashPointHumanThreadAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	// Native conversation marking has no notification/stopwatch work to replay.
	r, err := nativeoperation_model.SetCompletionAndRelease(release, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, nativeoperation_model.CompletionComplete)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(r), terminalResult{}, nil
}

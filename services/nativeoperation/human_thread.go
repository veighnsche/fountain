// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/google/uuid"
)

const CrashPointHumanThreadBeforePrimary = "human-thread-before-primary"
const CrashPointHumanThreadAfterPrimary = "human-thread-after-primary"

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
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, err := humanThreadIntent(q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if ctx.Err() != nil {
		return sdk.OperationRecord{}, ctx.Err()
	}
	if admit(ctx) != nil {
		return sdk.OperationRecord{}, ErrAuthorityLost
	}
	finish := func(r sdk.OperationRecord, err error) (sdk.OperationRecord, error) {
		if err == nil || errors.Is(err, ErrCancelledBeforeSubmit) {
			if ctx.Err() != nil || admit(ctx) != nil {
				return sdk.OperationRecord{}, ErrAuthorityLost
			}
		}
		return r, err
	}
	existing, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if existing != nil {
		r, err := s.reconcileExisting(existing, intent)
		return finish(r, err)
	}
	if q.NotAfter <= s.clock() || q.NotAfter > s.clock()+int64(MaxOperationLifetime/time.Second) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: intent.RepositoryID, IssueID: nativeCommentID(q.Expected.Review.IssueID)}
	record, err := s.claimConditionalSubmit(ctx, extensionauth_model.SubmissionDecision{ActorID: intent.ActorID}, installation, intent, scope, func(ctx context.Context, op *nativeoperation_model.Operation, _ *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
		return s.executeHumanThread(ctx, op, q, admit, execution)
	})
	return finish(record, err)
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
			reason := nativeoperation_model.ReasonNativeRefused
			switch deterministic.Reason {
			case "head changed":
				reason = nativeoperation_model.ReasonStaleHead
			case "base changed", "merge base changed":
				reason = nativeoperation_model.ReasonStaleBaseOrResult
			case "human thread conversation changed":
				reason = nativeoperation_model.ReasonStaleConversation
			case "thread actor is inactive", "thread workflow unavailable", "thread actor cannot read pull request", "human thread permission lost":
				reason = nativeoperation_model.ReasonAuthorityLost
			case "human thread parent changed", "human thread pull changed":
				reason = nativeoperation_model.ReasonPRMismatch
			case "human thread primary evidence differs":
				return sdk.OperationRecord{}, terminalResult{}, err // Unexpected SQL effects retain the fence after rollback.
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

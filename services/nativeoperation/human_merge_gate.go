// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"sync"
	"time"

	sdk "forgejo.org/extension-sdk"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"

	"github.com/google/uuid"
)

// A live request witness stays only in the service that dispatches native hooks.
// No session, callback or reusable bearer is persisted in the operation/scope.
type humanMergeAdmission struct {
	mu         sync.Mutex
	retired    bool
	ctx        context.Context
	want       *nativeoperation_model.Operation
	generation int64
	scope      string
	query      sdk.HumanMergeRequest
	stage      *pull_service.PreparedExactMerge
	admit      HumanReviewAdmission
}

func humanMergeExpectedOperation(installation string, intent *ValidIntent) *nativeoperation_model.Operation {
	return &nativeoperation_model.Operation{InstallationID: installation, OperationID: intent.OperationID, ActorID: intent.ActorID, RepositoryID: intent.RepositoryID, Kind: intent.Kind, AuthRevision: intent.AuthRevision, ExpectedNativeRevision: intent.ExpectedNativeRevision, NotAfter: intent.NotAfter, Intent: intent.Canonical, IntentDigest: intent.Digest}
}

// BindHumanMergeStage binds the actual open stage's result exactly once and
// retains live human admission for this held generation. The same Service must
// dispatch its prepared hook. The caller MUST defer retirement and stage.Close
// until its push has finished. Retirement never releases or rewrites the durable
// fence; a missing witness after restart cannot authorize a merge.
func (s *Service) BindHumanMergeStage(ctx context.Context, installation string, q sdk.HumanMergeRequest, stage *pull_service.PreparedExactMerge, admit HumanReviewAdmission) (retire func(), err error) {
	id, parseErr := uuid.Parse(installation)
	if parseErr != nil || id.String() != installation || stage == nil || admit == nil {
		return nil, ErrInvalidIntent
	}
	intent, err := humanMergeIntent(q)
	if err != nil {
		return nil, err
	}
	before, err := encodeScope(humanMergeCaptureScope(q))
	if err != nil {
		return nil, err
	}
	if err := s.checkHumanMergeCapture(ctx, installation, intent, before); err != nil {
		return nil, err
	}
	pr, actor, err := checkHumanMergeTarget(ctx, intent.ActorID, q.Expected, admit)
	if err != nil {
		return nil, err
	}
	execution := execcontext.FromContext(ctx) // Exact held execution checked above.
	owner, generation := execution.Owner, execution.Generation
	var witness *humanMergeAdmission
	err = stage.WithExactTarget(ctx, pr, actor, repo_model.MergeStyle(q.Expected.Method), q.Expected.HeadOID, q.Expected.BaseOID, q.Message, func(result pull_service.PreparedMergeResult) error {
		bound := humanMergeCaptureScope(q)
		bound.NewOID = result.CommitID
		after, err := encodeScope(bound)
		if err != nil {
			return err
		}
		want := humanMergeExpectedOperation(installation, intent)
		s.humanMergeMu.Lock()
		defer s.humanMergeMu.Unlock()
		if s.humanMerges[owner] != nil {
			return nativeoperation_model.ErrBusy
		}
		if err := nativeoperation_model.BindHumanMergeScope(ctx, want, owner, generation, before, after, s.clock()); err != nil {
			return err
		}
		witness = &humanMergeAdmission{ctx: ctx, want: want, generation: generation, scope: after, query: q, stage: stage, admit: admit}
		if s.humanMerges == nil {
			s.humanMerges = make(map[string]*humanMergeAdmission)
		}
		s.humanMerges[owner] = witness
		return nil
	})
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// Serialize retirement against an in-flight decision. When this
			// returns, no later prepared admission may use this witness.
			witness.mu.Lock()
			witness.retired = true
			witness.mu.Unlock()
			s.humanMergeMu.Lock()
			if s.humanMerges[owner] == witness {
				delete(s.humanMerges, owner)
			}
			s.humanMergeMu.Unlock()
		})
	}, nil
}

func (s *Service) admitHumanMergePrepared(ctx context.Context, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation, lines []RefLine) (TransactionDecision, error) {
	s.humanMergeMu.Lock()
	w := s.humanMerges[held.Owner]
	s.humanMergeMu.Unlock()
	if w == nil {
		return TransactionDecision{Reason: nativeoperation_model.ReasonAuthorityLost}, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.retired || w.generation != held.Generation || w.scope != held.Scope || w.want.InstallationID != op.InstallationID || w.want.OperationID != op.OperationID {
		return TransactionDecision{Reason: nativeoperation_model.ReasonWrongOwner}, nil
	}
	// The generated Git hook has execution proof, but no human session. Combine
	// its cancellation with the original request and immutable admission deadline.
	live, cancel := context.WithDeadline(ctx, time.Unix(w.want.NotAfter, 0))
	defer cancel()
	stop := context.AfterFunc(w.ctx, cancel)
	defer stop()
	if w.ctx.Err() != nil {
		cancel()
	}
	owned := execcontext.NewContext(live, &execcontext.Execution{Owner: held.Owner, Generation: held.Generation})
	reason := ""
	switch {
	case len(lines) != 1:
		reason = nativeoperation_model.ReasonUnexpectedRefEffects
	case lines[0].Ref != scope.Ref || lines[0].Old != scope.OldOID || lines[0].New != scope.NewOID:
		reason = nativeoperation_model.ReasonStaleBaseOrResult
	case s.clock() >= w.want.NotAfter:
		reason = nativeoperation_model.ReasonExpiredBeforeAdmission
	case owned.Err() != nil:
		reason = nativeoperation_model.ReasonAuthorityLost
	}
	decision := func(ctx context.Context, reason string) (TransactionDecision, error) {
		// Refusals still use the request context, not a cancelled derived deadline,
		// so expiry/cancel ordering can be durably recorded without authorizing.
		recorded, yes, err := nativeoperation_model.RecordHumanMergeAdmission(ctx, w.want, held.Owner, held.Generation, held.Scope, s.clock(), reason == "", reason)
		if errors.Is(err, nativeoperation_model.ErrWrongOwner) {
			return TransactionDecision{Reason: nativeoperation_model.ReasonWrongOwner}, nil
		}
		if errors.Is(err, nativeoperation_model.ErrAdmissionLost) {
			return TransactionDecision{Reason: nativeoperation_model.ReasonNativeRefused}, nil
		}
		if err != nil {
			return TransactionDecision{}, err
		}
		if !yes {
			return TransactionDecision{Reason: lostAdmissionReason(recorded)}, nil
		}
		return TransactionDecision{Allowed: true}, nil
	}
	if reason != "" {
		return decision(ctx, reason)
	}
	pr, actor, err := checkHumanMergeTarget(owned, w.want.ActorID, w.query.Expected, w.admit)
	if err != nil {
		reason = nativeoperation_model.ReasonNativeRefused
		if errors.Is(err, ErrAuthorityLost) {
			reason = nativeoperation_model.ReasonAuthorityLost
		}
		if s.clock() >= w.want.NotAfter || errors.Is(err, nativeoperation_model.ErrOperationExpired) {
			reason = nativeoperation_model.ReasonExpiredBeforeAdmission
		}
		return decision(ctx, reason)
	}
	var result TransactionDecision
	err = w.stage.WithExactTarget(owned, pr, actor, repo_model.MergeStyle(w.query.Expected.Method), w.query.Expected.HeadOID, w.query.Expected.BaseOID, w.query.Message, func(_ pull_service.PreparedMergeResult) error {
		var err error
		// AfterFunc propagates cancellation to in-flight I/O. Also observe the
		// original request directly at admission, without waiting for its callback
		// goroutine to run after a cancellation during native validation.
		if w.ctx.Err() != nil {
			result, err = decision(ctx, nativeoperation_model.ReasonAuthorityLost)
			return err
		}
		result, err = decision(owned, "")
		return err
	})
	if err != nil {
		// Infrastructure errors retain the pending fence; a closed/foreign stage
		// is a deterministic refusal. No error can turn into an allowed decision.
		var mismatch pull_service.ErrExactMergeRefused
		if errors.As(err, &mismatch) {
			return decision(ctx, nativeoperation_model.ReasonNativeRefused)
		}
		return TransactionDecision{}, err
	}
	return result, nil
}

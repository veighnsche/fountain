// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"time"

	sdk "forgejo.org/extension-sdk"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"

	"github.com/google/uuid"
)

func humanMergeIntent(q sdk.HumanMergeRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanMergeRequest(q) {
		return nil, ErrInvalidIntent
	}
	p := q.Expected
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(p.ActorID), RepositoryID: nativeCommentID(p.RepositoryID), Kind: nativeoperation_model.KindHumanMerge, AuthRevision: sdk.HumanMergeAuthorizationRevision, ExpectedNativeRevision: p.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanMergeIntent(q)
	return intent, err
}

// This is the unprepared scope. Only the later prepared-push binder may add the
// server-computed NewOID; client captures never select a result commit.
func humanMergeCaptureScope(q sdk.HumanMergeRequest) Scope {
	p := q.Expected
	return Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: nativeCommentID(p.RepositoryID), IssueID: nativeCommentID(p.IssueID), PRNumber: nativeCommentID(p.Index), Ref: git.BranchPrefix + p.BaseBranch, OldOID: p.BaseOID, HeadRef: git.PullPrefix + p.Index + "/head", HeadOID: p.HeadOID}
}

// PrepareHumanMergeOperation consumes a previously persisted pending command
// under its exact held owner. It allocates no operation and publishes nothing.
// The caller MUST Close the private result. Push admission and durable merge
// completion are separate consumers; this function never marks an op admitted.
func (s *Service) PrepareHumanMergeOperation(ctx context.Context, installation string, q sdk.HumanMergeRequest, admit HumanReviewAdmission) (*pull_service.PreparedExactMerge, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil {
		return nil, ErrInvalidIntent
	}
	intent, err := humanMergeIntent(q)
	if err != nil {
		return nil, err
	}
	scope, err := json.Marshal(humanMergeCaptureScope(q))
	if err != nil {
		return nil, err
	}
	if err := s.checkHumanMergeCapture(ctx, installation, intent, string(scope)); err != nil {
		return nil, err
	}
	return PrepareHumanMerge(ctx, intent.ActorID, q.Expected, q.Message, func(ctx context.Context) error {
		if err := admitHumanPrimary(ctx, admit); err != nil {
			return err
		}
		return s.checkHumanMergeCapture(ctx, installation, intent, string(scope))
	})
}

func (s *Service) checkHumanMergeCapture(ctx context.Context, installation string, intent *ValidIntent, scope string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	op, err := nativeoperation_model.LookupOperation(ctx, installation, intent.OperationID)
	if err != nil {
		return err
	}
	if !humanMergeCaptureMatches(op, intent) || op.Revoked || op.Admitted || op.EffectState != nativeoperation_model.EffectPending || op.Receipt != "" || op.Completion != "" {
		return ErrInvalidIntent
	}
	now := s.clock()
	if now >= intent.NotAfter {
		return nativeoperation_model.ErrOperationExpired
	}
	if intent.NotAfter > now+int64(MaxOperationLifetime/time.Second) {
		return ErrInvalidIntent
	}
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return err
	}
	execution := execcontext.FromContext(ctx)
	if execution == nil || held.OwnerKind != nativeoperation_model.OwnerConditional || held.Owner != conditionalOwner(installation, intent.OperationID) || execution.Owner != held.Owner || execution.Generation != held.Generation {
		return nativeoperation_model.ErrWrongOwner
	}
	if held.Scope != scope {
		return ErrInvalidIntent
	}
	if err := requireHumanPrimaryOwner(ctx, intent.ExpectedNativeRevision); err != nil {
		return err
	}
	return nil
}

func humanMergeCaptureMatches(op *nativeoperation_model.Operation, intent *ValidIntent) bool {
	return op != nil && intent != nil && op.Submitted && op.TokenID == 0 && op.CredentialFingerprint == "" && op.Kind == intent.Kind && op.OperationID == intent.OperationID && op.ActorID == intent.ActorID && op.RepositoryID == intent.RepositoryID && op.AuthRevision == intent.AuthRevision && op.ExpectedNativeRevision == intent.ExpectedNativeRevision && op.NotAfter == intent.NotAfter && op.Intent == intent.Canonical && op.IntentDigest == intent.Digest
}

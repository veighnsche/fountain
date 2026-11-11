// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/google/uuid"
)

// humanSubmission exists only after canonical intent, current request authority,
// replay and deadline checks. Per-kind code still owns its exact primary and
// transaction; the live result boundary is shared by fresh and replayed work.
type humanSubmission struct {
	service      *Service
	ctx          context.Context
	installation string
	admit        func(context.Context) error
	intent       *ValidIntent
}

func (s *Service) beginHumanSubmission(ctx context.Context, installation string, admit func(context.Context) error, intentBuilder func() (*ValidIntent, error)) (*humanSubmission, sdk.OperationRecord, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil {
		return nil, sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, err := intentBuilder()
	if err != nil {
		return nil, sdk.OperationRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, sdk.OperationRecord{}, err
	}
	if admit(ctx) != nil {
		return nil, sdk.OperationRecord{}, ErrAuthorityLost
	}
	submission := &humanSubmission{service: s, ctx: ctx, installation: installation, admit: admit, intent: intent}
	existing, err := nativeoperation_model.LookupOperation(ctx, installation, intent.OperationID)
	if err != nil {
		return nil, sdk.OperationRecord{}, err
	}
	if existing != nil {
		record, err := s.reconcileExisting(existing, intent)
		record, err = submission.finish(record, err)
		return nil, record, err
	}
	if intent.NotAfter <= s.clock() || intent.NotAfter > s.clock()+int64(MaxOperationLifetime/time.Second) {
		return nil, sdk.OperationRecord{}, ErrInvalidIntent
	}
	return submission, sdk.OperationRecord{}, nil
}

func (submission *humanSubmission) run(scope Scope, execute func(context.Context, *nativeoperation_model.Operation, *ValidIntent, *execcontext.Execution) (sdk.OperationRecord, terminalResult, error)) (sdk.OperationRecord, error) {
	record, err := submission.service.claimConditionalSubmit(submission.ctx, extensionauth_model.SubmissionDecision{ActorID: submission.intent.ActorID}, submission.installation, submission.intent, scope, execute)
	return submission.finish(record, err)
}

func (submission *humanSubmission) finish(record sdk.OperationRecord, err error) (sdk.OperationRecord, error) {
	if err == nil || errors.Is(err, ErrCancelledBeforeSubmit) {
		if submission.ctx.Err() != nil || submission.admit(submission.ctx) != nil {
			return sdk.OperationRecord{}, ErrAuthorityLost
		}
	}
	return record, err
}

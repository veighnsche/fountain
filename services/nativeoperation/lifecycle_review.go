// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"io"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	perm_model "forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
)

// MaxLifecycleReviewers caps human reviewer requests per PR/event.
const MaxLifecycleReviewers = 3

// LifecycleReviewIntent carries the validated
// pull_request.review.request payload: up to three eligible human
// reviewers on one exact PR. Request removal, dismissal and team
// requests are outside this operation. Requested human review never
// replaces mandatory independent Factory review.
type LifecycleReviewIntent struct {
	IssueID     int64   `json:"issue_id"`
	ReviewerIDs []int64 `json:"reviewer_ids"`
}

// parseLifecycleReviewPayload validates the complete immutable
// reviewer-request intent. Unknown fields, duplicates and out-of-bound
// reviewer sets fail validation.
func parseLifecycleReviewPayload(payload []byte, repositoryID int64) (*LifecycleReviewIntent, error) {
	if len(payload) == 0 || repositoryID <= 0 {
		return nil, ErrInvalidIntent
	}
	var intent LifecycleReviewIntent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return nil, ErrInvalidIntent
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidIntent
	}
	if intent.IssueID <= 0 || len(intent.ReviewerIDs) == 0 || len(intent.ReviewerIDs) > MaxLifecycleReviewers {
		return nil, ErrInvalidIntent
	}
	seen := map[int64]bool{}
	for _, id := range intent.ReviewerIDs {
		if id <= 0 || seen[id] {
			return nil, ErrInvalidIntent
		}
		seen[id] = true
	}
	return &intent, nil
}

// LifecycleReviewReceipt attributes one committed reviewer-request write
// to its operation, distinguishing new requests from idempotent
// already-requested skips.
type LifecycleReviewReceipt struct {
	IssueID          int64   `json:"issue_id"`
	Requested        []int64 `json:"requested,omitempty"`
	AlreadyRequested []int64 `json:"already_requested,omitempty"`
}

// submitLifecycleReview claims the operation ID and reservation, verifies
// the exact PR/reviewers and native authority under the held owner, and
// commits the review-request rows with the operation receipt in one SQL
// transaction. Every refusal path records not_committed.
func (s *Service) submitLifecycleReview(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent) (sdk.OperationRecord, error) {
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if intent.LifecycleReview == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := Scope{
		Kind:                 nativeoperation_model.OwnerConditional,
		RepositoryID:         intent.RepositoryID,
		IssueID:              intent.LifecycleReview.IssueID,
		LifecycleReviewerIDs: intent.LifecycleReview.ReviewerIDs,
	}
	return s.claimBackgroundSubmit(ctx, decision, installationID, intent, scope)
}

// executeLifecycleReview runs the guarded reviewer-request write. All
// native work runs on the owned context; terminal records and the owner
// release run detached so a caller disconnect cannot stick the
// reservation.
func (s *Service) executeLifecycleReview(ctx context.Context, op *nativeoperation_model.Operation, intent *ValidIntent, _ Scope, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owner := execution.Owner
	submit := intent.LifecycleReview
	releaseCtx := context.WithoutCancel(ctx)
	owned := execcontext.NewContext(ctx, execution)
	refuse := conditionalRefusal(ctx, op, execution)
	repository, err := repo_model.GetRepositoryByID(owned, op.RepositoryID)
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
	issue, err := issues_model.GetIssueByID(owned, submit.IssueID)
	if err != nil || !issue.IsPull || issue.RepoID != op.RepositoryID {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	if err := issue.LoadRepo(owned); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	writable, err := access_model.HasAccessUnit(owned, doer, repository, unit.TypePullRequests, perm_model.AccessModeWrite)
	if err != nil || !writable {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	reviewers := make([]*user_model.User, 0, len(submit.ReviewerIDs))
	for _, id := range submit.ReviewerIDs {
		reviewer, err := user_model.GetUserByID(owned, id)
		if err != nil || reviewer.IsBot() {
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
		readable, err := access_model.HasAccess(owned, reviewer.ID, repository)
		if err != nil || !readable {
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
		reviewers = append(reviewers, reviewer)
	}
	if err := TestCrashBarrier(CrashPointLifecycleReviewBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	committed, err := nativeoperation_model.CommitLifecycleReviewPrimary(owned, op.InstallationID, op.OperationID, owner, s.clock(), func(tx context.Context) (string, error) {
		if err := s.revalidateSubmission(tx, op); err != nil {
			return "", err
		}
		receipt := LifecycleReviewReceipt{IssueID: submit.IssueID}
		for i, reviewer := range reviewers {
			comment, err := issues_model.AddReviewRequest(tx, issue, reviewer, doer)
			if err != nil {
				return "", err
			}
			if comment == nil {
				receipt.AlreadyRequested = append(receipt.AlreadyRequested, submit.ReviewerIDs[i])
			} else {
				receipt.Requested = append(receipt.Requested, submit.ReviewerIDs[i])
			}
		}
		encoded, _ := json.Marshal(receipt)
		return string(encoded), nil
	})
	if err != nil {
		switch {
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			current, lookupErr := nativeoperation_model.LookupOperation(releaseCtx, op.InstallationID, op.OperationID)
			if lookupErr != nil {
				return sdk.OperationRecord{}, terminalResult{}, lookupErr
			}
			if current != nil && current.Revoked {
				return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
			}
			return sdk.OperationRecord{}, terminalResult{}, err
		default:
			if issues_model.IsErrReviewRequestOnClosedPR(err) {
				return refuse(nativeoperation_model.ReasonNativeRefused)
			}
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
	}
	if err := TestCrashBarrier(CrashPointLifecycleReviewAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	op = committed
	recorded, err := nativeoperation_model.SetCompletionAndRelease(releaseCtx, op.InstallationID, op.OperationID, owner, execution.Generation, nativeoperation_model.CompletionComplete)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(recorded), terminalResult{}, nil
}

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
	notify_service "forgejo.org/services/notify"
)

// LifecycleIssueIntent carries the validated issue.lifecycle.apply
// payload: at most one configured label and one eligible assignee on one
// exact generated issue. Label creation/removal and multi-assignee writes
// are outside this operation.
type LifecycleIssueIntent struct {
	IssueID    int64 `json:"issue_id"`
	LabelID    int64 `json:"label_id,omitempty"`
	AssigneeID int64 `json:"assignee_id,omitempty"`
}

// parseLifecycleIssuePayload validates the complete immutable
// label/assignee intent. Unknown fields and empty effects fail validation
// rather than silently broadening the request.
func parseLifecycleIssuePayload(payload []byte, repositoryID int64) (*LifecycleIssueIntent, error) {
	if len(payload) == 0 || repositoryID <= 0 {
		return nil, ErrInvalidIntent
	}
	var intent LifecycleIssueIntent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return nil, ErrInvalidIntent
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidIntent
	}
	if intent.IssueID <= 0 || intent.LabelID < 0 || intent.AssigneeID < 0 {
		return nil, ErrInvalidIntent
	}
	if intent.LabelID == 0 && intent.AssigneeID == 0 {
		return nil, ErrInvalidIntent
	}
	return &intent, nil
}

// LifecycleIssueReceipt attributes one committed label/assignee write to
// its operation. Added flags distinguish a new write from an idempotent
// no-op over already-present state.
type LifecycleIssueReceipt struct {
	IssueID       int64 `json:"issue_id"`
	LabelID       int64 `json:"label_id,omitempty"`
	LabelAdded    bool  `json:"label_added,omitempty"`
	AssigneeID    int64 `json:"assignee_id,omitempty"`
	AssigneeAdded bool  `json:"assignee_added,omitempty"`
}

// submitLifecycleIssue claims the operation ID and reservation, verifies
// the exact issue/label/assignee and native authority under the held
// owner, and commits the label/assignee rows with the operation receipt
// in one SQL transaction. Every refusal path records not_committed.
func (s *Service) submitLifecycleIssue(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent) (sdk.OperationRecord, error) {
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if intent.LifecycleIssue == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := Scope{
		Kind:                nativeoperation_model.OwnerConditional,
		RepositoryID:        intent.RepositoryID,
		IssueID:             intent.LifecycleIssue.IssueID,
		LifecycleLabelID:    intent.LifecycleIssue.LabelID,
		LifecycleAssigneeID: intent.LifecycleIssue.AssigneeID,
	}
	return s.claimBackgroundSubmit(ctx, decision, installationID, intent, scope)
}

// executeLifecycleIssue runs the guarded label/assignee write. All native
// work runs on the owned context; terminal records and the owner release
// run detached so a caller disconnect cannot stick the reservation.
func (s *Service) executeLifecycleIssue(ctx context.Context, op *nativeoperation_model.Operation, intent *ValidIntent, _ Scope, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owner := execution.Owner
	submit := intent.LifecycleIssue
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
	if err != nil || issue.IsPull {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	if issue.RepoID != op.RepositoryID {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	writable, err := access_model.HasAccessUnit(owned, doer, repository, unit.TypeIssues, perm_model.AccessModeWrite)
	if err != nil || !writable {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	var label *issues_model.Label
	if submit.LabelID > 0 {
		label, err = issues_model.GetLabelByID(owned, submit.LabelID)
		if err != nil {
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
		if label.RepoID != op.RepositoryID && (label.OrgID == 0 || label.OrgID != repository.OwnerID) {
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
	}
	var assignee *user_model.User
	if submit.AssigneeID > 0 {
		assignee, err = user_model.GetUserByID(owned, submit.AssigneeID)
		if err != nil {
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
		if err := issue.LoadRepo(owned); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		eligible, err := access_model.CanBeAssigned(owned, assignee, issue.Repo, false)
		if err != nil || !eligible {
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
	}
	if err := TestCrashBarrier(CrashPointLifecycleIssueBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	committed, err := nativeoperation_model.CommitLifecycleIssuePrimary(owned, op.InstallationID, op.OperationID, owner, s.clock(), func(tx context.Context) (string, error) {
		if err := s.revalidateSubmission(tx, op); err != nil {
			return "", err
		}
		receipt := LifecycleIssueReceipt{IssueID: submit.IssueID, LabelID: submit.LabelID, AssigneeID: submit.AssigneeID}
		if label != nil && !issues_model.HasIssueLabel(tx, submit.IssueID, submit.LabelID) {
			if err := issues_model.NewIssueLabel(tx, issue, label, doer); err != nil {
				return "", err
			}
			receipt.LabelAdded = true
		}
		if assignee != nil {
			assigned, err := issues_model.IsUserAssignedToIssue(tx, issue, assignee)
			if err != nil {
				return "", err
			}
			if !assigned {
				if _, _, err := issues_model.ToggleIssueAssignee(tx, issue, doer, submit.AssigneeID); err != nil {
					return "", err
				}
				receipt.AssigneeAdded = true
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
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
	}
	if err := TestCrashBarrier(CrashPointLifecycleIssueAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	op = committed
	if label != nil {
		notify_service.IssueChangeLabels(owned, doer, issue, []*issues_model.Label{label}, nil)
	}
	recorded, err := nativeoperation_model.SetCompletionAndRelease(releaseCtx, op.InstallationID, op.OperationID, owner, execution.Generation, nativeoperation_model.CompletionComplete)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(recorded), terminalResult{}, nil
}

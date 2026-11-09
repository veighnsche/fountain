// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
)

// MaxLifecycleNoteChars caps one completion note per event.
const MaxLifecycleNoteChars = 1000

// LifecycleNoteIntent carries the validated issue.comment.publish
// payload: one bounded completion note on one exact original issue. Soda
// supplies verified merge AND original-closure applicability separately;
// the native operation requires the issue closed and binds the supplied
// merge evidence reference into the immutable intent. Edits, deletes and
// attachments are outside this operation.
type LifecycleNoteIntent struct {
	IssueID  int64  `json:"issue_id"`
	Content  string `json:"content"`
	MergeRef string `json:"merge_ref"`
}

// parseLifecycleNotePayload validates the complete immutable completion
// note intent. Unknown fields, empty or over-bound content and missing
// merge evidence fail validation.
func parseLifecycleNotePayload(payload []byte, repositoryID int64) (*LifecycleNoteIntent, error) {
	if len(payload) == 0 || repositoryID <= 0 {
		return nil, ErrInvalidIntent
	}
	var intent LifecycleNoteIntent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return nil, ErrInvalidIntent
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidIntent
	}
	if intent.IssueID <= 0 || intent.MergeRef == "" || len(intent.MergeRef) > 512 {
		return nil, ErrInvalidIntent
	}
	if strings.TrimSpace(intent.Content) == "" || utf8.RuneCountInString(intent.Content) > MaxLifecycleNoteChars || strings.ContainsRune(intent.Content, 0) || !utf8.ValidString(intent.Content) {
		return nil, ErrInvalidIntent
	}
	return &intent, nil
}

// LifecycleNoteReceipt attributes one committed completion note to its
// operation. Identical replay returns this same comment ID; a lost
// response never publishes a duplicate.
type LifecycleNoteReceipt struct {
	IssueID   int64  `json:"issue_id"`
	CommentID int64  `json:"comment_id"`
	MergeRef  string `json:"merge_ref"`
}

// submitLifecycleNote claims the operation ID and reservation, verifies
// the exact closed issue and native authority under the held owner, and
// commits the comment row with the operation receipt in one SQL
// transaction. Every refusal path records not_committed.
func (s *Service) submitLifecycleNote(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent) (sdk.OperationRecord, error) {
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if intent.LifecycleNote == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	dir, err := s.execDir()
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	capabilityPath, secret, err := execcontext.WriteCapabilityFile(dir)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	retire := func() { _ = os.Remove(capabilityPath) }
	owner := conditionalOwner(installationID, intent.OperationID)
	scope := Scope{
		Kind:         nativeoperation_model.OwnerConditional,
		RepositoryID: intent.RepositoryID,
		IssueID:      intent.LifecycleNote.IssueID,
	}
	encoded, err := encodeScope(scope)
	if err != nil {
		retire()
		return sdk.OperationRecord{}, err
	}
	op := operationForSubmission(decision, installationID, intent)
	claimed, err := nativeoperation_model.ClaimConditional(ctx, op, owner, encoded, execcontext.Verifier(secret))
	if err != nil {
		retire()
		if errors.Is(err, nativeoperation_model.ErrBusy) {
			return sdk.OperationRecord{}, ErrBusy
		}
		if errors.Is(err, nativeoperation_model.ErrInhibited) {
			return sdk.OperationRecord{}, err
		}
		if errors.Is(err, nativeoperation_model.ErrStaleRevision) {
			return s.recordStaleRevision(ctx, op)
		}
		existing, lookupErr := nativeoperation_model.LookupOperation(ctx, installationID, intent.OperationID)
		if lookupErr != nil {
			return sdk.OperationRecord{}, lookupErr
		}
		if existing != nil {
			return s.reconcileSubmission(existing, decision, intent)
		}
		return sdk.OperationRecord{}, err
	}
	execution := &execcontext.Execution{Owner: owner, Generation: claimed.Generation, CapabilityPath: capabilityPath}
	record, terminal, err := s.executeLifecycleNote(ctx, op, intent, execution)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if !terminal.retain {
		retire()
	}
	return record, nil
}

// executeLifecycleNote runs the guarded completion-note write. All native
// work runs on the owned context; terminal records and the owner release
// run detached so a caller disconnect cannot stick the reservation.
func (s *Service) executeLifecycleNote(ctx context.Context, op *nativeoperation_model.Operation, intent *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owner := execution.Owner
	submit := intent.LifecycleNote
	releaseCtx := context.WithoutCancel(ctx)
	owned := execcontext.NewContext(ctx, execution)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		recorded, err := nativeoperation_model.SetTerminal(releaseCtx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState:  nativeoperation_model.EffectNotCommitted,
			Reason:       reason,
			Cancellation: nativeoperation_model.CancellationNone,
		}, owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(recorded), terminalResult{}, nil
	}
	if err := s.revalidateSubmission(owned, op); err != nil {
		if errors.Is(err, ErrAuthorityLost) {
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		}
		return sdk.OperationRecord{}, terminalResult{}, err
	}
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
	if err != nil || issue.IsPull || issue.RepoID != op.RepositoryID || !issue.IsClosed {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	if err := issue.LoadRepo(owned); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	readable, err := access_model.HasAccess(owned, doer.ID, repository)
	if err != nil || !readable {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	committed, err := nativeoperation_model.CommitLifecycleNotePrimary(owned, op.InstallationID, op.OperationID, owner, s.clock(), func(tx context.Context) (string, error) {
		if err := s.revalidateSubmission(tx, op); err != nil {
			return "", err
		}
		comment, err := issues_model.CreateComment(tx, &issues_model.CreateCommentOptions{
			Type:    issues_model.CommentTypeComment,
			Doer:    doer,
			Repo:    issue.Repo,
			Issue:   issue,
			Content: submit.Content,
		})
		if err != nil {
			return "", err
		}
		encoded, _ := json.Marshal(LifecycleNoteReceipt{IssueID: submit.IssueID, CommentID: comment.ID, MergeRef: submit.MergeRef})
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
	op = committed
	recorded, err := nativeoperation_model.SetCompletionAndRelease(releaseCtx, op.InstallationID, op.OperationID, owner, execution.Generation, nativeoperation_model.CompletionComplete)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(recorded), terminalResult{}, nil
}

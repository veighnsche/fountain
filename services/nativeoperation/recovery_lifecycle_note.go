// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
)

func (s *Service) recoverConditionalLifecycleNote(ctx context.Context, a *RecoveryAssessment, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	payload, err := conditionalRecoveryPayload(op, nativeoperation_model.KindLifecycleNote)
	if err != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "lifecycle note canonical capture differs")
	}
	want, err := parseLifecycleNotePayload(payload, op.RepositoryID)
	if err != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "lifecycle note payload differs")
	}
	expected := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: op.RepositoryID, IssueID: want.IssueID}
	return s.recoverConditionalSQL(ctx, a, held, scope, op, expected, func(tx context.Context, current *nativeoperation_model.Operation) (string, error) {
		var receipt LifecycleNoteReceipt
		if exactConditionalReceipt(current.Receipt, &receipt) != nil || receipt.IssueID != want.IssueID || receipt.CommentID <= 0 || receipt.MergeRef != want.MergeRef {
			return "", errConditionalRecoveryEvidence
		}
		issue, err := issues_model.GetIssueByID(tx, receipt.IssueID)
		if err != nil || issue.IsPull || issue.RepoID != current.RepositoryID || !issue.IsClosed {
			return "", errConditionalRecoveryEvidence
		}
		comment, err := issues_model.GetCommentByID(tx, receipt.CommentID)
		if err != nil || comment.Type != issues_model.CommentTypeComment || comment.IssueID != issue.ID || comment.PosterID != current.ActorID ||
			comment.OriginalAuthorID != 0 || comment.Content != want.Content || comment.ContentVersion != 0 || comment.ReviewID != 0 ||
			comment.CreatedUnix < current.CreatedUnix || comment.UpdatedUnix != comment.CreatedUnix {
			return "", errConditionalRecoveryEvidence
		}
		return recoveredLifecycleCompletion(current)
	})
}

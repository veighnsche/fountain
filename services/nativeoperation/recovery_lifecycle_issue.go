// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
)

func (s *Service) recoverConditionalLifecycleIssue(ctx context.Context, a *RecoveryAssessment, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	payload, err := conditionalRecoveryPayload(op, nativeoperation_model.KindLifecycleIssue)
	if err != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "lifecycle issue canonical capture differs")
	}
	want, err := parseLifecycleIssuePayload(payload, op.RepositoryID)
	if err != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "lifecycle issue payload differs")
	}
	expected := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: op.RepositoryID, IssueID: want.IssueID, LifecycleLabelID: want.LabelID, LifecycleAssigneeID: want.AssigneeID}
	return s.recoverConditionalSQL(ctx, a, held, scope, op, expected, func(tx context.Context, current *nativeoperation_model.Operation) (string, error) {
		var receipt LifecycleIssueReceipt
		if exactConditionalReceipt(current.Receipt, &receipt) != nil || receipt.IssueID != want.IssueID || receipt.LabelID != want.LabelID || receipt.AssigneeID != want.AssigneeID ||
			receipt.LabelAdded && want.LabelID == 0 || receipt.AssigneeAdded && want.AssigneeID == 0 {
			return "", errConditionalRecoveryEvidence
		}
		issue, err := issues_model.GetIssueByID(tx, receipt.IssueID)
		if err != nil || issue.IsPull || issue.RepoID != current.RepositoryID {
			return "", errConditionalRecoveryEvidence
		}
		if want.LabelID > 0 {
			label, err := issues_model.GetLabelByID(tx, want.LabelID)
			if err != nil {
				return "", errConditionalRecoveryEvidence
			}
			repository, err := repo_model.GetRepositoryByID(tx, current.RepositoryID)
			if err != nil || label.RepoID != current.RepositoryID && (label.OrgID == 0 || label.OrgID != repository.OwnerID) {
				return "", errConditionalRecoveryEvidence
			}
			n, err := db.GetEngine(tx).Where("issue_id=? AND label_id=?", issue.ID, want.LabelID).Count(new(issues_model.IssueLabel))
			if err != nil {
				return "", err
			}
			if n != 1 {
				return "", errConditionalRecoveryEvidence
			}
			if receipt.LabelAdded {
				n, err := db.GetEngine(tx).Where("issue_id=? AND type=? AND label_id=? AND poster_id=? AND original_author_id=0 AND content=? AND created_unix>=?",
					issue.ID, issues_model.CommentTypeLabel, want.LabelID, current.ActorID, "1", current.CreatedUnix).Count(new(issues_model.Comment))
				if err != nil {
					return "", err
				}
				if n != 1 {
					return "", errConditionalRecoveryEvidence
				}
			}
		}
		if want.AssigneeID > 0 {
			if _, err := user_model.GetUserByID(tx, want.AssigneeID); err != nil {
				return "", errConditionalRecoveryEvidence
			}
			n, err := db.GetEngine(tx).Where("issue_id=? AND assignee_id=?", issue.ID, want.AssigneeID).Count(new(issues_model.IssueAssignees))
			if err != nil {
				return "", err
			}
			if n != 1 {
				return "", errConditionalRecoveryEvidence
			}
			if receipt.AssigneeAdded {
				n, err := db.GetEngine(tx).Where("issue_id=? AND type=? AND assignee_id=? AND poster_id=? AND original_author_id=0 AND removed_assignee=? AND created_unix>=?",
					issue.ID, issues_model.CommentTypeAssignees, want.AssigneeID, current.ActorID, false, current.CreatedUnix).Count(new(issues_model.Comment))
				if err != nil {
					return "", err
				}
				if n != 1 {
					return "", errConditionalRecoveryEvidence
				}
			}
		}
		return recoveredLifecycleCompletion(current)
	})
}

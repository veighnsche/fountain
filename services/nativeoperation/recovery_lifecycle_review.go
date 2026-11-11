// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"slices"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	user_model "forgejo.org/models/user"
)

func (s *Service) recoverConditionalLifecycleReview(ctx context.Context, a *RecoveryAssessment, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	payload, err := conditionalRecoveryPayload(op, nativeoperation_model.KindLifecycleReview)
	if err != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "lifecycle reviewer canonical capture differs")
	}
	want, err := parseLifecycleReviewPayload(payload, op.RepositoryID)
	if err != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "lifecycle reviewer payload differs")
	}
	expected := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: op.RepositoryID, IssueID: want.IssueID, LifecycleReviewerIDs: want.ReviewerIDs}
	return s.recoverConditionalSQL(ctx, a, held, scope, op, expected, func(tx context.Context, current *nativeoperation_model.Operation) (string, error) {
		var receipt LifecycleReviewReceipt
		if exactConditionalReceipt(current.Receipt, &receipt) != nil || receipt.IssueID != want.IssueID || len(receipt.Requested)+len(receipt.AlreadyRequested) != len(want.ReviewerIDs) {
			return "", errConditionalRecoveryEvidence
		}
		// Both receipt partitions preserve the submitted order, and every
		// requested principal occurs exactly once across them.
		seen := make(map[int64]bool)
		for _, partition := range [][]int64{receipt.Requested, receipt.AlreadyRequested} {
			previous := -1
			for _, id := range partition {
				index := slices.Index(want.ReviewerIDs, id)
				if index <= previous || seen[id] {
					return "", errConditionalRecoveryEvidence
				}
				seen[id], previous = true, index
			}
		}
		issue, err := issues_model.GetIssueByID(tx, receipt.IssueID)
		if err != nil || !issue.IsPull || issue.RepoID != current.RepositoryID || issue.LoadPullRequest(tx) != nil || issue.PullRequest == nil || issue.PullRequest.BaseRepoID != current.RepositoryID {
			return "", errConditionalRecoveryEvidence
		}
		for _, id := range want.ReviewerIDs {
			if _, err := user_model.GetUserByID(tx, id); err != nil {
				return "", errConditionalRecoveryEvidence
			}
			review, err := issues_model.GetReviewByIssueIDAndUserID(tx, issue.ID, id)
			if err != nil || review.Type != issues_model.ReviewTypeRequest || review.ReviewerID != id || review.ReviewerTeamID != 0 || review.IssueID != issue.ID {
				return "", errConditionalRecoveryEvidence
			}
			if slices.Contains(receipt.Requested, id) {
				if review.CreatedUnix < current.CreatedUnix {
					return "", errConditionalRecoveryEvidence
				}
				n, err := db.GetEngine(tx).Where("issue_id=? AND type=? AND review_id=? AND assignee_id=? AND assignee_team_id=0 AND poster_id=? AND original_author_id=0 AND removed_assignee=? AND created_unix>=?",
					issue.ID, issues_model.CommentTypeReviewRequest, review.ID, id, current.ActorID, false, current.CreatedUnix).Count(new(issues_model.Comment))
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

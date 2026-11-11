// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"errors"

	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
)

const (
	reviewRefusalBinaryOnlyInlineAnchor                     issues_model.ExactReviewRefusalCode = "binary-only_inline_anchor"
	reviewRefusalCreatedReviewerRequestDiffers              issues_model.ExactReviewRefusalCode = "created_reviewer_request_differs"
	reviewRefusalHumanThreadActionWasNotAdmitted            issues_model.ExactReviewRefusalCode = "human_thread_action_was_not_admitted"
	reviewRefusalHumanThreadConversationChanged             issues_model.ExactReviewRefusalCode = "human_thread_conversation_changed"
	reviewRefusalHumanThreadParentChanged                   issues_model.ExactReviewRefusalCode = "human_thread_parent_changed"
	reviewRefusalHumanThreadPermissionLost                  issues_model.ExactReviewRefusalCode = "human_thread_permission_lost"
	reviewRefusalHumanThreadPrimaryEvidenceDiffers          issues_model.ExactReviewRefusalCode = "human_thread_primary_evidence_differs"
	reviewRefusalHumanThreadPullChanged                     issues_model.ExactReviewRefusalCode = "human_thread_pull_changed"
	reviewRefusalInvalidHumanReviewerTarget                 issues_model.ExactReviewRefusalCode = "invalid_human_reviewer_target"
	reviewRefusalInvalidHumanThreadTarget                   issues_model.ExactReviewRefusalCode = "invalid_human_thread_target"
	reviewRefusalInvalidInlineAnchor                        issues_model.ExactReviewRefusalCode = "invalid_inline_anchor"
	reviewRefusalInvalidPersistedReviewerHistory            issues_model.ExactReviewRefusalCode = "invalid_persisted_reviewer_history"
	reviewRefusalMergeBaseUnavailable                       issues_model.ExactReviewRefusalCode = "merge_base_unavailable"
	reviewRefusalRemovedReviewerHistoryDiffers              issues_model.ExactReviewRefusalCode = "removed_reviewer_history_differs"
	reviewRefusalReviewerActionWasNotAdmitted               issues_model.ExactReviewRefusalCode = "reviewer_action_was_not_admitted"
	reviewRefusalReviewerActorCannotReadPull                issues_model.ExactReviewRefusalCode = "reviewer_actor_cannot_read_pull"
	reviewRefusalReviewerActorIsInactive                    issues_model.ExactReviewRefusalCode = "reviewer_actor_is_inactive"
	reviewRefusalReviewerBaseChanged                        issues_model.ExactReviewRefusalCode = "reviewer_base_changed"
	reviewRefusalReviewerHeadChanged                        issues_model.ExactReviewRefusalCode = "reviewer_head_changed"
	reviewRefusalReviewerHistoryChanged                     issues_model.ExactReviewRefusalCode = "reviewer_history_changed"
	reviewRefusalReviewerHistoryDiffersFromNativeEffect     issues_model.ExactReviewRefusalCode = "reviewer_history_differs_from_native_effect"
	reviewRefusalReviewerMergeBaseChanged                   issues_model.ExactReviewRefusalCode = "reviewer_merge_base_changed"
	reviewRefusalReviewerParentChanged                      issues_model.ExactReviewRefusalCode = "reviewer_parent_changed"
	reviewRefusalReviewerPolicyOrSelectedRequestsChanged    issues_model.ExactReviewRefusalCode = "reviewer_policy_or_selected_requests_changed"
	reviewRefusalReviewerPrimaryDidNotRecordExpectedHistory issues_model.ExactReviewRefusalCode = "reviewer_primary_did_not_record_expected_history"
	reviewRefusalReviewerPrimaryReadbackDiffers             issues_model.ExactReviewRefusalCode = "reviewer_primary_readback_differs"
	reviewRefusalReviewerPullChanged                        issues_model.ExactReviewRefusalCode = "reviewer_pull_changed"
	reviewRefusalReviewerWorkflowUnavailable                issues_model.ExactReviewRefusalCode = "reviewer_workflow_unavailable"
	reviewRefusalThreadActorCannotReadPullRequest           issues_model.ExactReviewRefusalCode = "thread_actor_cannot_read_pull_request"
	reviewRefusalThreadActorIsInactive                      issues_model.ExactReviewRefusalCode = "thread_actor_is_inactive"
	reviewRefusalThreadWorkflowUnavailable                  issues_model.ExactReviewRefusalCode = "thread_workflow_unavailable"
)

func exactReviewRefusalCode(refused issues_model.ErrExactReviewRefused) issues_model.ExactReviewRefusalCode {
	if refused.Code != "" {
		return refused.Code
	}
	switch refused.Reason {
	case "binary-only inline anchor":
		return reviewRefusalBinaryOnlyInlineAnchor
	case "created reviewer request differs":
		return reviewRefusalCreatedReviewerRequestDiffers
	case "human thread action was not admitted":
		return reviewRefusalHumanThreadActionWasNotAdmitted
	case "human thread conversation changed":
		return reviewRefusalHumanThreadConversationChanged
	case "human thread parent changed":
		return reviewRefusalHumanThreadParentChanged
	case "human thread permission lost":
		return reviewRefusalHumanThreadPermissionLost
	case "human thread primary evidence differs":
		return reviewRefusalHumanThreadPrimaryEvidenceDiffers
	case "human thread pull changed":
		return reviewRefusalHumanThreadPullChanged
	case "invalid human reviewer target":
		return reviewRefusalInvalidHumanReviewerTarget
	case "invalid human thread target":
		return reviewRefusalInvalidHumanThreadTarget
	case "invalid inline anchor":
		return reviewRefusalInvalidInlineAnchor
	case "invalid persisted reviewer history":
		return reviewRefusalInvalidPersistedReviewerHistory
	case "merge base unavailable":
		return reviewRefusalMergeBaseUnavailable
	case "removed reviewer history differs":
		return reviewRefusalRemovedReviewerHistoryDiffers
	case "reviewer action was not admitted":
		return reviewRefusalReviewerActionWasNotAdmitted
	case "reviewer actor cannot read pull":
		return reviewRefusalReviewerActorCannotReadPull
	case "reviewer actor is inactive":
		return reviewRefusalReviewerActorIsInactive
	case "reviewer base changed":
		return reviewRefusalReviewerBaseChanged
	case "reviewer head changed":
		return reviewRefusalReviewerHeadChanged
	case "reviewer history changed":
		return reviewRefusalReviewerHistoryChanged
	case "reviewer history differs from native effect":
		return reviewRefusalReviewerHistoryDiffersFromNativeEffect
	case "reviewer merge base changed":
		return reviewRefusalReviewerMergeBaseChanged
	case "reviewer parent changed":
		return reviewRefusalReviewerParentChanged
	case "reviewer policy or selected requests changed":
		return reviewRefusalReviewerPolicyOrSelectedRequestsChanged
	case "reviewer primary did not record expected history":
		return reviewRefusalReviewerPrimaryDidNotRecordExpectedHistory
	case "reviewer primary readback differs":
		return reviewRefusalReviewerPrimaryReadbackDiffers
	case "reviewer pull changed":
		return reviewRefusalReviewerPullChanged
	case "reviewer workflow unavailable":
		return reviewRefusalReviewerWorkflowUnavailable
	case "thread actor cannot read pull request":
		return reviewRefusalThreadActorCannotReadPullRequest
	case "thread actor is inactive":
		return reviewRefusalThreadActorIsInactive
	case "thread workflow unavailable":
		return reviewRefusalThreadWorkflowUnavailable
	default:
		return refused.RefusalCode()
	}
}

// mapExactReviewRefusal maps a deterministic exact-review refusal to its
// bounded operation reason. Changed candidates refuse as stale; identity,
// source, target and lifecycle mismatches refuse as PR mismatches; denied
// native review eligibility refuses as authority lost; a conflicting pending
// draft refuses as pending. Anything unrecognized fails closed as a native
// refusal.
func mapExactReviewRefusal(err error) string {
	var refused issues_model.ErrExactReviewRefused
	if !errors.As(err, &refused) {
		if issues_model.IsContentEmptyErr(err) {
			return nativeoperation_model.ReasonNativeRefused
		}
		return nativeoperation_model.ReasonNativeRefused
	}
	switch exactReviewRefusalCode(refused) {
	case issues_model.ReviewRefusalHeadChanged:
		return nativeoperation_model.ReasonStaleHead
	case issues_model.ReviewRefusalBaseChanged:
		return nativeoperation_model.ReasonStaleBaseOrResult
	case issues_model.ReviewRefusalPendingDraftExists:
		return nativeoperation_model.ReasonPendingReviewExists
	case issues_model.ReviewRefusalCannotReviewYourOwnPullRequest,
		issues_model.ReviewRefusalReviewerIsBlocked,
		issues_model.ReviewRefusalReviewerCannotReadThisPullRequest:
		return nativeoperation_model.ReasonAuthorityLost
	case issues_model.ReviewRefusalInvalidObjectID,
		issues_model.ReviewRefusalCommitMustEqualTheExpectedHead,
		issues_model.ReviewRefusalUnsupportedFinalReviewType,
		issues_model.ReviewRefusalInvalidInlineComment,
		reviewRefusalInvalidInlineAnchor,
		reviewRefusalBinaryOnlyInlineAnchor,
		issues_model.ReviewRefusalDuplicateInlineAnchor,
		issues_model.ReviewRefusalDuplicateInlineCommentKey,
		issues_model.ReviewRefusalTooManyInlineComments,
		reviewRefusalMergeBaseUnavailable:
		// Unreachable through the guarded path: intent parsing or the
		// diff-membership proof already rejects these. Fail closed as
		// a native refusal rather than a PR mismatch.
		return nativeoperation_model.ReasonNativeRefused
	default:
		return nativeoperation_model.ReasonPRMismatch
	}
}

func mapHumanReviewRefusal(deterministic issues_model.ErrExactReviewRefused) string {
	reason := nativeoperation_model.ReasonNativeRefused
	switch exactReviewRefusalCode(deterministic) {
	case issues_model.ReviewRefusalHeadChanged:
		reason = nativeoperation_model.ReasonStaleHead
	case issues_model.ReviewRefusalBaseChanged, issues_model.ReviewRefusalMergeBaseChanged:
		reason = nativeoperation_model.ReasonStaleBaseOrResult
	case issues_model.ReviewRefusalPendingDraftChanged:
		reason = nativeoperation_model.ReasonPendingReviewExists
	case issues_model.ReviewRefusalReviewerIsInactive, issues_model.ReviewRefusalReviewerCannotReadThisPullRequest, issues_model.ReviewRefusalReviewerIsBlocked, issues_model.ReviewRefusalReviewWorkflowUnavailable, issues_model.ReviewRefusalCannotReviewYourOwnPullRequest:
		reason = nativeoperation_model.ReasonAuthorityLost
	case issues_model.ReviewRefusalHumanReviewParentChanged, issues_model.ReviewRefusalHumanReviewPullChanged:
		reason = nativeoperation_model.ReasonPRMismatch
	}
	return reason
}

func mapHumanInlineRefusal(deterministic issues_model.ErrExactReviewRefused) string {
	reason := nativeoperation_model.ReasonNativeRefused
	switch exactReviewRefusalCode(deterministic) {
	case issues_model.ReviewRefusalHeadChanged:
		reason = nativeoperation_model.ReasonStaleHead
	case issues_model.ReviewRefusalBaseChanged, issues_model.ReviewRefusalMergeBaseChanged:
		reason = nativeoperation_model.ReasonStaleBaseOrResult
	case issues_model.ReviewRefusalPendingDraftChanged:
		reason = nativeoperation_model.ReasonPendingReviewExists
	case issues_model.ReviewRefusalReviewerIsInactive, issues_model.ReviewRefusalReviewerCannotReadThisPullRequest, issues_model.ReviewRefusalReviewerIsBlocked, issues_model.ReviewRefusalReviewWorkflowUnavailable:
		reason = nativeoperation_model.ReasonAuthorityLost
	case issues_model.ReviewRefusalHumanReviewParentChanged, issues_model.ReviewRefusalHumanReviewPullChanged:
		reason = nativeoperation_model.ReasonPRMismatch
	}
	return reason
}

func mapHumanThreadRefusal(deterministic issues_model.ErrExactReviewRefused) (string, bool) {
	reason := nativeoperation_model.ReasonNativeRefused
	switch exactReviewRefusalCode(deterministic) {
	case issues_model.ReviewRefusalHeadChanged:
		reason = nativeoperation_model.ReasonStaleHead
	case issues_model.ReviewRefusalBaseChanged, issues_model.ReviewRefusalMergeBaseChanged:
		reason = nativeoperation_model.ReasonStaleBaseOrResult
	case reviewRefusalHumanThreadConversationChanged:
		reason = nativeoperation_model.ReasonStaleConversation
	case reviewRefusalThreadActorIsInactive, reviewRefusalThreadWorkflowUnavailable, reviewRefusalThreadActorCannotReadPullRequest, reviewRefusalHumanThreadPermissionLost:
		reason = nativeoperation_model.ReasonAuthorityLost
	case reviewRefusalHumanThreadParentChanged, reviewRefusalHumanThreadPullChanged:
		reason = nativeoperation_model.ReasonPRMismatch
	case reviewRefusalHumanThreadPrimaryEvidenceDiffers:
		return "", true // Unexpected SQL effects retain the fence after rollback.
	}
	return reason, false
}

func mapHumanReviewRequestRefusal(deterministic issues_model.ErrExactReviewRefused) (string, bool) {
	reason := nativeoperation_model.ReasonNativeRefused
	switch exactReviewRefusalCode(deterministic) {
	case reviewRefusalReviewerHeadChanged:
		reason = nativeoperation_model.ReasonStaleHead
	case reviewRefusalReviewerBaseChanged, reviewRefusalReviewerMergeBaseChanged:
		reason = nativeoperation_model.ReasonStaleBaseOrResult
	case reviewRefusalReviewerPolicyOrSelectedRequestsChanged, reviewRefusalReviewerHistoryChanged:
		reason = nativeoperation_model.ReasonStaleConversation
	case reviewRefusalReviewerActorIsInactive, reviewRefusalReviewerWorkflowUnavailable, reviewRefusalReviewerActorCannotReadPull:
		reason = nativeoperation_model.ReasonAuthorityLost
	case reviewRefusalReviewerParentChanged, reviewRefusalReviewerPullChanged:
		reason = nativeoperation_model.ReasonPRMismatch
	case reviewRefusalReviewerPrimaryReadbackDiffers, reviewRefusalReviewerHistoryDiffersFromNativeEffect, reviewRefusalCreatedReviewerRequestDiffers, reviewRefusalRemovedReviewerHistoryDiffers, reviewRefusalReviewerPrimaryDidNotRecordExpectedHistory, reviewRefusalInvalidPersistedReviewerHistory:
		return "", true // Unexpected SQL effects retain the fence after rollback.
	}
	return reason, false
}

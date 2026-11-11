// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import "forgejo.org/modules/util"

// ExactReviewRefusalCode identifies a refusal independently of its diagnostic.
type ExactReviewRefusalCode string

const (
	ReviewRefusalBaseBranchMismatch                       ExactReviewRefusalCode = "base_branch_mismatch"
	ReviewRefusalBaseChanged                              ExactReviewRefusalCode = "base_changed"
	ReviewRefusalCannotReviewYourOwnPullRequest           ExactReviewRefusalCode = "cannot_review_your_own_pull_request"
	ReviewRefusalCommitMustEqualTheExpectedHead           ExactReviewRefusalCode = "commit_must_equal_the_expected_head"
	ReviewRefusalCrossRepositoryPullRequestIsNotSupported ExactReviewRefusalCode = "cross-repository_pull_request_is_not_supported"
	ReviewRefusalDuplicateInlineAnchor                    ExactReviewRefusalCode = "duplicate_inline_anchor"
	ReviewRefusalDuplicateInlineCommentKey                ExactReviewRefusalCode = "duplicate_inline_comment_key"
	ReviewRefusalHeadBranchMismatch                       ExactReviewRefusalCode = "head_branch_mismatch"
	ReviewRefusalHeadChanged                              ExactReviewRefusalCode = "head_changed"
	ReviewRefusalHeadRepositoryMismatch                   ExactReviewRefusalCode = "head_repository_mismatch"
	ReviewRefusalHumanReviewParentChanged                 ExactReviewRefusalCode = "human_review_parent_changed"
	ReviewRefusalHumanReviewPullChanged                   ExactReviewRefusalCode = "human_review_pull_changed"
	ReviewRefusalInlineProofChanged                       ExactReviewRefusalCode = "inline_proof_changed"
	ReviewRefusalInvalidHumanInlineBody                   ExactReviewRefusalCode = "invalid_human_inline_body"
	ReviewRefusalInvalidHumanInlineTarget                 ExactReviewRefusalCode = "invalid_human_inline_target"
	ReviewRefusalInvalidHumanReviewBody                   ExactReviewRefusalCode = "invalid_human_review_body"
	ReviewRefusalInvalidHumanReviewEvent                  ExactReviewRefusalCode = "invalid_human_review_event"
	ReviewRefusalInvalidHumanReviewTarget                 ExactReviewRefusalCode = "invalid_human_review_target"
	ReviewRefusalInvalidInlineComment                     ExactReviewRefusalCode = "invalid_inline_comment"
	ReviewRefusalInvalidObjectID                          ExactReviewRefusalCode = "invalid_object_id"
	ReviewRefusalMergeBaseChanged                         ExactReviewRefusalCode = "merge_base_changed"
	ReviewRefusalOriginalDiffBaseUnavailable              ExactReviewRefusalCode = "original_diff_base_unavailable"
	ReviewRefusalPendingDraftChanged                      ExactReviewRefusalCode = "pending_draft_changed"
	ReviewRefusalPendingDraftCommentIdentityDiffers       ExactReviewRefusalCode = "pending_draft_comment_identity_differs"
	ReviewRefusalPendingDraftExists                       ExactReviewRefusalCode = "pending_draft_exists"
	ReviewRefusalPendingDraftIdentityDiffers              ExactReviewRefusalCode = "pending_draft_identity_differs"
	ReviewRefusalPRAuthorMismatch                         ExactReviewRefusalCode = "pr_author_mismatch"
	ReviewRefusalPRIndexMismatch                          ExactReviewRefusalCode = "pr_index_mismatch"
	ReviewRefusalPullRequestIsClosedOrMerged              ExactReviewRefusalCode = "pull_request_is_closed_or_merged"
	ReviewRefusalReviewWorkflowUnavailable                ExactReviewRefusalCode = "review_workflow_unavailable"
	ReviewRefusalReviewerCannotReadThisPullRequest        ExactReviewRefusalCode = "reviewer_cannot_read_this_pull_request"
	ReviewRefusalReviewerIsBlocked                        ExactReviewRefusalCode = "reviewer_is_blocked"
	ReviewRefusalReviewerIsInactive                       ExactReviewRefusalCode = "reviewer_is_inactive"
	ReviewRefusalTooManyInlineComments                    ExactReviewRefusalCode = "too_many_inline_comments"
	ReviewRefusalUnsupportedFinalReviewType               ExactReviewRefusalCode = "unsupported_final_review_type"
)

// ErrExactReviewRefused preserves the concrete public error and its prose.
// Code is supplied by the producer; Reason-only literals remain legacy adapters.
type ErrExactReviewRefused struct {
	Reason string
	Code   ExactReviewRefusalCode `json:"-"`
}

func NewExactReviewRefusal(code ExactReviewRefusalCode, reason string) ErrExactReviewRefused {
	return ErrExactReviewRefused{Reason: reason, Code: code}
}

func IsErrExactReviewRefused(err error) bool {
	_, ok := err.(ErrExactReviewRefused)
	return ok
}

func (err ErrExactReviewRefused) Error() string { return "exact review refused: " + err.Reason }
func (err ErrExactReviewRefused) Unwrap() error { return util.ErrInvalidArgument }

// RefusalCode adapts old Reason-only construction at the owning model boundary.
func (err ErrExactReviewRefused) RefusalCode() ExactReviewRefusalCode {
	if err.Code != "" {
		return err.Code
	}
	switch err.Reason {
	case "base branch mismatch":
		return ReviewRefusalBaseBranchMismatch
	case "base changed":
		return ReviewRefusalBaseChanged
	case "cannot review your own pull request":
		return ReviewRefusalCannotReviewYourOwnPullRequest
	case "commit must equal the expected head":
		return ReviewRefusalCommitMustEqualTheExpectedHead
	case "cross-repository pull request is not supported":
		return ReviewRefusalCrossRepositoryPullRequestIsNotSupported
	case "duplicate inline anchor":
		return ReviewRefusalDuplicateInlineAnchor
	case "duplicate inline comment key":
		return ReviewRefusalDuplicateInlineCommentKey
	case "head branch mismatch":
		return ReviewRefusalHeadBranchMismatch
	case "head changed":
		return ReviewRefusalHeadChanged
	case "head repository mismatch":
		return ReviewRefusalHeadRepositoryMismatch
	case "human review parent changed":
		return ReviewRefusalHumanReviewParentChanged
	case "human review pull changed":
		return ReviewRefusalHumanReviewPullChanged
	case "inline proof changed":
		return ReviewRefusalInlineProofChanged
	case "invalid human inline body":
		return ReviewRefusalInvalidHumanInlineBody
	case "invalid human inline target":
		return ReviewRefusalInvalidHumanInlineTarget
	case "invalid human review body":
		return ReviewRefusalInvalidHumanReviewBody
	case "invalid human review event":
		return ReviewRefusalInvalidHumanReviewEvent
	case "invalid human review target":
		return ReviewRefusalInvalidHumanReviewTarget
	case "invalid inline comment":
		return ReviewRefusalInvalidInlineComment
	case "invalid object id":
		return ReviewRefusalInvalidObjectID
	case "merge base changed":
		return ReviewRefusalMergeBaseChanged
	case "original diff base unavailable":
		return ReviewRefusalOriginalDiffBaseUnavailable
	case "pending draft changed":
		return ReviewRefusalPendingDraftChanged
	case "pending draft comment identity differs":
		return ReviewRefusalPendingDraftCommentIdentityDiffers
	case "pending draft exists":
		return ReviewRefusalPendingDraftExists
	case "pending draft identity differs":
		return ReviewRefusalPendingDraftIdentityDiffers
	case "pr author mismatch":
		return ReviewRefusalPRAuthorMismatch
	case "pr index mismatch":
		return ReviewRefusalPRIndexMismatch
	case "pull request is closed or merged":
		return ReviewRefusalPullRequestIsClosedOrMerged
	case "review workflow unavailable":
		return ReviewRefusalReviewWorkflowUnavailable
	case "reviewer cannot read this pull request":
		return ReviewRefusalReviewerCannotReadThisPullRequest
	case "reviewer is blocked":
		return ReviewRefusalReviewerIsBlocked
	case "reviewer is inactive":
		return ReviewRefusalReviewerIsInactive
	case "too many inline comments":
		return ReviewRefusalTooManyInlineComments
	case "unsupported final review type":
		return ReviewRefusalUnsupportedFinalReviewType
	default:
		return ""
	}
}

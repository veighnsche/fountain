// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package operationcontract owns stable operation namespaces, independent of
// enrollment policy, execution implementations and transport.
package operationcontract

import "strings"

const (
	KindIssueGraph      = "issue.graph.mutate"
	KindRefPublish      = "git.ref.publish"
	KindPRCreate        = "pull_request.create"
	KindReviewSubmit    = "pull_request.review.submit"
	KindThreadResolve   = "pull_request.review.thread.resolve"
	KindMerge           = "pull_request.merge"
	KindLifecycleIssue  = "issue.lifecycle.apply"
	KindLifecycleReview = "pull_request.review.request"
	KindLifecycleNote   = "issue.comment.publish"
	KindExecutionLaunch = "execution.launch"

	HumanCommentPrefix       = "human-comment:"
	HumanEditPrefix          = "human-edit:"
	HumanIssueCreatePrefix   = "human-issue-create:"
	HumanLockPrefix          = "human-lock:"
	HumanStatePrefix         = "human-state:"
	HumanReviewPrefix        = "human-review:"
	HumanInlinePrefix        = "human-inline:"
	HumanThreadPrefix        = "human-thread:"
	HumanReviewRequestPrefix = "human-review-request:"
	HumanMergePrefix         = "human-merge:"
	HumanManualMergePrefix   = "human-manual-merge:"
	HumanPullCreatePrefix    = "human-pull-create:"
)

// BackgroundKinds returns the stable journal vocabulary, in handshake order.
// Enrollment-only execution.launch is deliberately absent.
func BackgroundKinds() []string {
	return []string{
		KindRefPublish, KindLifecycleNote, KindIssueGraph,
		KindLifecycleIssue, KindPRCreate, KindMerge, KindLifecycleReview,
		KindReviewSubmit, KindThreadResolve,
	}
}

func ValidBackgroundKind(kind string) bool {
	for _, candidate := range BackgroundKinds() {
		if candidate == kind {
			return true
		}
	}
	return false
}

func ValidEnrollmentKind(kind string) bool {
	return kind == KindExecutionLaunch || ValidBackgroundKind(kind)
}

func ValidID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}

func IsHumanID(id string) bool {
	for _, prefix := range []string{
		HumanCommentPrefix, HumanEditPrefix,
		HumanIssueCreatePrefix, HumanLockPrefix, HumanStatePrefix, HumanReviewPrefix,
		HumanInlinePrefix, HumanThreadPrefix, HumanReviewRequestPrefix,
		HumanMergePrefix, HumanManualMergePrefix, HumanPullCreatePrefix,
	} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

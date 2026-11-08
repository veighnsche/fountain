// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"testing"

	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestParseCollabResource(t *testing.T) {
	cases := []struct {
		resource string
		kind     string
		id, id2  int64
		op       string
		ok       bool
	}{
		{execcontext.IssueResource(7, "title"), "issue", 7, 0, "title", true},
		{execcontext.IssueResource(7, "content"), "issue", 7, 0, "content", true},
		{execcontext.IssueResource(7, "status"), "issue", 7, 0, "status", true},
		{execcontext.IssueResource(7, "delete"), "issue", 7, 0, "delete", true},
		{execcontext.IssueCreateResource(1), "issue", 1, 0, "create", true},
		{execcontext.CommentResource(9, "update"), "comment", 9, 0, "update", true},
		{execcontext.CommentResource(9, "conversation"), "comment", 9, 0, "conversation", true},
		{execcontext.CommentResource(9, "attachment"), "comment", 9, 0, "attachment", true},
		{execcontext.CommentCreateResource(7), "comment", 7, 0, "create", true},
		{execcontext.DependencyResource(7, 8), "dependency", 7, 8, "", true},
		{execcontext.PullResource(3, "retarget"), "pull", 3, 0, "retarget", true},
		{execcontext.PullResource(3, "automerge"), "pull", 3, 0, "automerge", true},
		{execcontext.PullResource(3, "manual"), "pull", 3, 0, "manual", true},
		{execcontext.PullResource(3, "edits"), "pull", 3, 0, "edits", true},
		{execcontext.PullCreateResource(7), "pull", 7, 0, "create", true},
		{execcontext.ReviewResource(5, "dismiss"), "review", 5, 0, "dismiss", true},
		{execcontext.ReviewRequestResource(7, 2), "review", 7, 2, "request-user", true},
		{execcontext.ReviewTeamRequestResource(7, 3), "review", 7, 3, "request-team", true},
		{execcontext.ReviewSubmitResource(7), "review", 7, 0, "submit", true},
		{execcontext.ReviewDeleteResource(7, 5), "review", 7, 5, "delete", true},
		{execcontext.ReactionResource(7, 0, 2), "reaction", 7, 0, "2", true},
		{execcontext.ReactionResource(7, 9, 2), "reaction", 7, 9, "2", true},
		{execcontext.LabelResource(4, "update"), "label", 4, 0, "update", true},
		{execcontext.LabelResource(1, "create"), "label", 1, 0, "create", true},
		{execcontext.MilestoneResource(6, "status"), "milestone", 6, 0, "status", true},
		{execcontext.MilestoneResource(1, "create"), "milestone", 1, 0, "create", true},
		{execcontext.HistoryResource(11), "history", 11, 0, "delete", true},
		{execcontext.AttachmentCreateResource(1), "attachment", 1, 0, "create", true},
		{execcontext.AttachmentResource(9, "delete"), "attachment", 9, 0, "delete", true},
		{execcontext.AttachmentResource(9, "update"), "attachment", 9, 0, "update", true},
		{execcontext.CollabBatchResource("orphan-fix"), "batch", 0, 0, "orphan-fix", true},
		{execcontext.CollabBatchResource("push-commit"), "batch", 0, 0, "push-commit", true},
		{execcontext.CollabBatchResource("pr-sweep"), "batch", 0, 0, "pr-sweep", true},
		{"issue/7/approve", "", 0, 0, "", false},
		{"issue/0/title", "", 0, 0, "", false},
		{"issue/seven/title", "", 0, 0, "", false},
		{"issue/7", "", 0, 0, "", false},
		{"comment/9/publish", "", 0, 0, "", false},
		{"comment/new/0", "", 0, 0, "", false},
		{"dependency/7", "", 0, 0, "", false},
		{"dependency/7/0", "", 0, 0, "", false},
		{"pull/3/merge", "", 0, 0, "", false},
		{"review/5/approve", "", 0, 0, "", false},
		{"review/5/request", "", 0, 0, "", false},
		{"review/7/5/close", "", 0, 0, "", false},
		{"review/7/request-user/0", "", 0, 0, "", false},
		{"reaction/7/0", "", 0, 0, "", false},
		{"reaction/7/-1/2", "", 0, 0, "", false},
		{"label/4/rename", "", 0, 0, "", false},
		{"milestone/6/close", "", 0, 0, "", false},
		{"history/11/soft", "", 0, 0, "", false},
		{"attachment/9/rename", "", 0, 0, "", false},
		{"attachment/new/0", "", 0, 0, "", false},
		{"attachment/0/delete", "", 0, 0, "", false},
		{"batch/something", "", 0, 0, "", false},
		{"watch/7", "", 0, 0, "", false},
		{"", "", 0, 0, "", false},
	}
	for _, tc := range cases {
		claim, err := execcontext.ParseCollabResource(tc.resource)
		require.Equal(t, tc.ok, err == nil, "resource %q", tc.resource)
		if !tc.ok {
			continue
		}
		require.Equal(t, tc.kind, claim.Kind, "resource %q", tc.resource)
		require.Equal(t, tc.id, claim.ID, "resource %q", tc.resource)
		require.Equal(t, tc.id2, claim.ID2, "resource %q", tc.resource)
		require.Equal(t, tc.op, claim.Op, "resource %q", tc.resource)
	}
}

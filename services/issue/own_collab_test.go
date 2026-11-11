// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package issue_test

import (
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"testing"

	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

// TestCollabOwnershipWireContract pins the retained ordinary scope and resource bytes.
func TestCollabOwnershipWireContract(t *testing.T) {
	raw, err := json.Marshal(struct {
		Kind         string `json:"kind"`
		Family       string `json:"family,omitempty"`
		RepositoryID int64  `json:"repository_id"`
	}{Kind: "ordinary", Family: "collaboration", RepositoryID: 7})
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, map[string]any{"kind": "ordinary", "family": "collaboration", "repository_id": float64(7)}, decoded)

	require.Equal(t, "issue/7/title", execcontext.IssueResource(7, "title"))
	require.Equal(t, "comment/9/update", execcontext.CommentResource(9, "update"))
	require.Equal(t, "comment/new/7", execcontext.CommentCreateResource(7))
	require.Equal(t, "dependency/7/8", execcontext.DependencyResource(7, 8))
	require.Equal(t, "review/new/7", execcontext.ReviewSubmitResource(7))
	require.Equal(t, "reaction/7/0/2", execcontext.ReactionResource(7, 0, 2))
	require.Equal(t, "batch/push-commit", execcontext.CollabBatchResource("push-commit"))
	require.Equal(t, "issue/new/1", execcontext.IssueCreateResource(1))
	require.Equal(t, "review/7/request-user/2", execcontext.ReviewRequestResource(7, 2))
	require.Equal(t, "review/7/request-team/3", execcontext.ReviewTeamRequestResource(7, 3))
}

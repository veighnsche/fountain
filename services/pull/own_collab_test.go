// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull_test

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

	require.Equal(t, "pull/3/retarget", execcontext.PullResource(3, "retarget"))
	require.Equal(t, "pull/new/7", execcontext.PullCreateResource(7))
	require.Equal(t, "review/5/dismiss", execcontext.ReviewResource(5, "dismiss"))
	require.Equal(t, "review/new/7", execcontext.ReviewSubmitResource(7))
	require.Equal(t, "comment/new/7", execcontext.CommentCreateResource(7))
	require.Equal(t, "issue/7/title", execcontext.IssueResource(7, "title"))
	require.Equal(t, "batch/pr-sweep", execcontext.CollabBatchResource("pr-sweep"))
}

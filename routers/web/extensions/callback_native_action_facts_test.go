// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"net/http"
	"testing"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

func TestNativeActionPreparationsAgreeOnBlockedCommentAndTitleWithoutRemovingBodyEdit(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	// Fixture user 4 blocks administrator 1. A block still applies to the
	// native comment/title routes while body editing keeps its own rule.
	_, err := db.GetEngine(t.Context()).ID(1).Cols("poster_id").Update(&issues_model.Issue{PosterID: 4})
	require.NoError(t, err)
	admission := &callbackAdmission{
		actorID: 1, sessionID: "facts", session: &sourceTestSession{uid: 1},
		capabilities: map[string]struct{}{extension.CapabilityRepositoryIssuesRead: {}},
	}
	comment, status := readCommentSnapshot(t.Context(), admission, "1", extension.RepositoryCommentSnapshotRequest{Subject: "issue", Index: "1"})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, capabilityFact("denied", "actor_blocked"), comment.Action)
	capabilities, status := readRepositoryCapabilities(t.Context(), admission, "1", extension.RepositoryCapabilitiesRequest{Subject: "issue", Index: "1"})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, comment.Action, capabilities.Actions["issue.comment"])
	edit, status := readEditSnapshot(t.Context(), admission, "1", extension.RepositoryEditSnapshotRequest{Subject: "issue", Index: "1"})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, capabilityFact("denied", "issue_edit_denied"), edit.TitleAction)
	require.Equal(t, edit.TitleAction, capabilities.Actions["issue.edit"])
	require.Equal(t, capabilityFact("allowed", "issue_poster_or_writer"), edit.BodyAction)
}

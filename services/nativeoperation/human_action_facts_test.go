// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"testing"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

func TestHumanActionParentsKeepBlockedBodyEditSeparateFromTitleAndComment(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	_, err := db.GetEngine(t.Context()).ID(1).Cols("poster_id").Update(&issues_model.Issue{PosterID: 4})
	require.NoError(t, err)
	admit := func(context.Context) error { return nil }
	_, _, err = humanCommentParent(t.Context(), sdk.HumanCommentRequest{Expected: sdk.RepositoryCommentSnapshot{
		RepositoryID: "1", ActorID: "1", Subject: "issue", SubjectID: "1", IssueID: "1", Index: "1",
	}}, admit)
	require.ErrorIs(t, err, ErrAuthorityLost)
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	body, title := "edited body", "edited title"
	request := sdk.HumanEditRequest{Expected: sdk.RepositoryEditSnapshot{
		RepositoryID: "1", ActorID: "1", Subject: "issue", SubjectID: "1", IssueID: "1", Index: "1",
		Title: issue.Title, BodyDigest: editBodyDigest(issue.Content), ContentVersion: int64(issue.ContentVersion),
	}, Body: &body}
	_, _, err = humanEditParent(t.Context(), request, admit)
	require.NoError(t, err)
	request.Title, request.Body = &title, nil
	_, _, err = humanEditParent(t.Context(), request, admit)
	require.ErrorIs(t, err, ErrAuthorityLost)
}

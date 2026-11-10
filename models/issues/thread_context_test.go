// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues_test

import (
	"strings"
	"testing"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/require"
)

func threadContextComment(id, issueID, reviewID int64, anchor *issues_model.CodeCommentAnchor) *issues_model.Comment {
	return &issues_model.Comment{
		ID: id, IssueID: issueID, ReviewID: reviewID, Type: issues_model.CommentTypeCode,
		PosterID: 2, TreePath: "context.go", Line: 7, OriginalAnchor: anchor,
		Content: "retained comment", ContentVersion: 1,
		CreatedUnix: timeutil.TimeStamp(1700000000 + id), UpdatedUnix: timeutil.TimeStamp(1700100000),
	}
}

func threadContextAnchor() *issues_model.CodeCommentAnchor {
	return &issues_model.CodeCommentAnchor{
		Base: strings.Repeat("1", 40), Head: strings.Repeat("2", 40), Path: "context.go",
		Side: "new", Scope: "lines", StartLine: 7, EndLine: 7,
	}
}

func TestThreadCommentBoundedReadRetainsCompleteNativeContext(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	issue := &issues_model.Issue{ID: 10000, RepoID: 1, Index: 301, PosterID: 2, IsPull: true}
	review := &issues_model.Review{ID: 10000, IssueID: issue.ID, ReviewerID: 4, Type: issues_model.ReviewTypePending}
	unittest.AssertSuccessfulInsert(t, issue, review)
	first := threadContextComment(10000, issue.ID, review.ID, nil)
	first.ResolveDoerID = 2
	second := threadContextComment(10001, issue.ID, review.ID, threadContextAnchor())
	unittest.AssertSuccessfulInsert(t, first, second)

	read, err := issues_model.FetchThreadCommentsBounded(t.Context(), issue, 2)
	require.NoError(t, err)
	require.True(t, read.Complete)
	require.Len(t, read.Comments, 2)
	require.Equal(t, int64(10000), read.Comments[0].ID)
	require.NotNil(t, read.Comments[0].Poster)
	require.NotNil(t, read.Comments[0].ResolveDoer)
	require.Equal(t, issues_model.ReviewTypePending, read.Comments[0].Review.Type)
	require.Equal(t, int64(4), read.Reviews[review.ID].ReviewerID)
	grouped := issues_model.GroupCodeThreads(read.Comments)
	require.Len(t, grouped, 1)
	require.Equal(t, first.ID, grouped[0].Root().ID, "the earliest retained member remains the native root")
	require.Nil(t, grouped[0].Root().OriginalAnchor, "grouping must not invent persisted anchor data")

	all, _, err := issues_model.FetchThreadComments(t.Context(), issue)
	require.NoError(t, err)
	require.Equal(t, grouped, issues_model.GroupCodeThreads(all), "exact writers retain the full-context contract")

	overflow, err := issues_model.FetchThreadCommentsBounded(t.Context(), issue, 1)
	require.NoError(t, err)
	require.False(t, overflow.Complete)
	require.Empty(t, overflow.Comments, "an incomplete prefix cannot establish immutable identity")
	require.Empty(t, overflow.Reviews)

	issue.Repo, issue.RepoID = nil, 999999
	all, reviews, err := issues_model.FetchThreadComments(t.Context(), issue)
	require.Error(t, err)
	require.Nil(t, all, "the existing exact-reader error contract returns no partial rows")
	require.Nil(t, reviews)
}

func TestCodeThreadIdentityUsesWholeLocationContext(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	first := threadContextComment(10000, 3, 6, nil)
	second := threadContextComment(10001, 3, 6, threadContextAnchor())
	unittest.AssertSuccessfulInsert(t, first, second)
	read := func(limit int) issues_model.CodeThreadIdentity {
		t.Helper()
		identity, err := issues_model.ReadCodeThreadIdentity(t.Context(), 3, 6, "context.go", 7, limit)
		require.NoError(t, err)
		return identity
	}
	require.Equal(t, issues_model.CodeThreadIdentity{RootID: first.ID, Complete: true}, read(2))

	other := threadContextAnchor()
	other.Head = strings.Repeat("3", 40)
	unittest.AssertSuccessfulInsert(t, threadContextComment(10002, 3, 6, other))
	require.Equal(t, issues_model.CodeThreadIdentity{Complete: false}, read(2), "overflow is checked before sole-context assignment")
	require.Equal(t, issues_model.CodeThreadIdentity{Complete: true, Ambiguous: true}, read(3), "later anchored contexts retain separate unknown conversation")

	malformed := threadContextAnchor()
	malformed.Side = "invalid"
	_, err := db.GetEngine(t.Context()).ID(second.ID).Cols("original_anchor").Update(&issues_model.Comment{OriginalAnchor: malformed})
	require.NoError(t, err)
	unittest.AssertSuccessfulDelete(t, &issues_model.Comment{ID: 10002})
	require.Equal(t, issues_model.CodeThreadIdentity{Complete: true, Ambiguous: true}, read(2), "malformed anchors never absorb unknown members")

	empty, err := issues_model.ReadCodeThreadIdentity(t.Context(), 3, 6, "absent.go", 7, 2)
	require.NoError(t, err)
	require.Equal(t, issues_model.CodeThreadIdentity{Complete: true}, empty, "empty location preserves the native new-comment fallback")
}

func TestCodeThreadIdentityPreservesLegacyTenThousandBound(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	for offset := 0; offset < 10001; offset += 100 {
		var rows []*issues_model.Comment
		for i := offset; i < min(offset+100, 10001); i++ {
			rows = append(rows, threadContextComment(int64(20000+i), 3, 6, nil))
		}
		unittest.AssertSuccessfulInsert(t, rows)
	}
	identity, err := issues_model.ReadCodeThreadIdentity(t.Context(), 3, 6, "context.go", 7, 10000)
	require.NoError(t, err)
	require.False(t, identity.Complete)
	require.Zero(t, identity.RootID)

	unittest.AssertSuccessfulDelete(t, &issues_model.Comment{ID: 30000})
	identity, err = issues_model.ReadCodeThreadIdentity(t.Context(), 3, 6, "context.go", 7, 10000)
	require.NoError(t, err)
	require.Equal(t, issues_model.CodeThreadIdentity{RootID: 20000, Complete: true}, identity)
}

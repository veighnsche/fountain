// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"crypto/sha1"
	"fmt"
	"strconv"
	"testing"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/require"
)

func TestSnapshotCommentSelectorsShareVisibilityAndProjection(t *testing.T) {
	unittest.PrepareTestEnv(t)
	svc := NewService()
	issue := insertSnapshotIssue(t, 1, 301, "visible issue", "body", 0)
	comment := insertSnapshotComment(t, issue.ID, issues_model.CommentTypeComment, "same projection")
	comment.Invalidated = true
	_, err := db.GetEngine(t.Context()).ID(comment.ID).Cols("invalidated").Update(comment)
	require.NoError(t, err)

	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyComments})
	req.IssueIndex = "301"
	decision := snapshotAuthority(2, req)
	decision.IssuesOnly = true
	listed, err := svc.ReadSnapshot(t.Context(), decision, req)
	require.NoError(t, err)
	require.Len(t, listed.Comments.Items, 1)

	req.IssueIndex = ""
	req.CommentIDs = []string{snapshotIDString(comment.ID)}
	selected, err := svc.ReadSnapshot(t.Context(), decision, req)
	require.NoError(t, err)
	require.Equal(t, listed.Comments.Items, selected.Comments.Items)
	require.True(t, selected.Comments.Complete)

	pullComment := insertSnapshotComment(t, 3, issues_model.CommentTypeComment, "pull-only content")
	req.CommentIDs = []string{snapshotIDString(pullComment.ID)}
	_, err = svc.ReadSnapshot(t.Context(), decision, req)
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	req.CommentIDs = nil
	req.IssueIndex = "3"
	_, err = svc.ReadSnapshot(t.Context(), decision, req)
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	decision.IssuesOnly = false
	req.IssueIndex = ""
	req.CommentIDs = []string{snapshotIDString(pullComment.ID)}
	selected, err = svc.ReadSnapshot(t.Context(), decision, req)
	require.NoError(t, err)
	require.Equal(t, "pull-only content", selected.Comments.Items[0].Content)
}

func TestSnapshotCommentCursorReachesBeyondFirstFetch(t *testing.T) {
	unittest.PrepareTestEnv(t)
	issue := insertSnapshotIssue(t, 1, 302, "many comments", "body", 0)
	var rows []*issues_model.Comment
	for i := range 510 {
		rows = append(rows, &issues_model.Comment{
			ID: int64(10000 + i), IssueID: issue.ID, PosterID: 2, Type: issues_model.CommentTypeComment,
			Content: fmt.Sprint(i), ContentVersion: 1,
			CreatedUnix: timeutil.TimeStamp(1700001000 - i), UpdatedUnix: timeutil.TimeStamp(1700002000),
		})
	}
	unittest.AssertSuccessfulInsert(t, rows)
	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyComments})
	req.IssueIndex, req.Limit = "302", 7
	svc := NewService()
	first, err := svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.Equal(t, 510, first.Comments.Total)
	require.False(t, first.Comments.Complete)
	require.Len(t, first.Comments.Items, 7)
	require.Equal(t, "10000", first.Comments.Items[0].ID, "ID cursors cannot use creation-time order")

	req.Cursor = "10501"
	last, err := svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.Equal(t, 8, last.Comments.Total)
	require.False(t, last.Comments.Complete)
	require.Len(t, last.Comments.Items, 7)
	require.Equal(t, "10502", last.Comments.Items[0].ID)
	req.Cursor = last.Comments.Items[6].ID
	terminal, err := svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.Equal(t, 1, terminal.Comments.Total)
	require.True(t, terminal.Comments.Complete)
	require.Equal(t, "10509", terminal.Comments.Items[0].ID)
}

func TestSnapshotReviewCursorReachesBeyondFirstFetch(t *testing.T) {
	unittest.PrepareTestEnv(t)
	issue := insertSnapshotIssue(t, 1, 303, "many reviews", "body", 0)
	var rows []*issues_model.Review
	for i := range 510 {
		rows = append(rows, &issues_model.Review{
			ID: int64(10000 + i), IssueID: issue.ID, ReviewerID: 2, Type: issues_model.ReviewTypeComment,
			CommitID: snapshotBranch2, Content: fmt.Sprint(i),
			CreatedUnix: timeutil.TimeStamp(1700001000 - i), UpdatedUnix: timeutil.TimeStamp(1700002000),
		})
	}
	unittest.AssertSuccessfulInsert(t, rows)
	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyReviews})
	req.IssueIndex, req.Limit = "303", 7
	svc := NewService()
	first, err := svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.Equal(t, 510, first.Reviews.Total)
	require.False(t, first.Reviews.Complete)
	require.Len(t, first.Reviews.Items, 7)
	require.Equal(t, "10000", first.Reviews.Items[0].ID)

	req.Cursor, req.Limit = "10501", 8
	last, err := svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.Equal(t, 8, last.Reviews.Total)
	require.True(t, last.Reviews.Complete)
	require.Len(t, last.Reviews.Items, 8)
	for i, item := range last.Reviews.Items {
		require.Equal(t, strconv.Itoa(10502+i), item.ID)
	}
	req.Cursor = "10509"
	empty, err := svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.Empty(t, empty.Reviews.Items)
	require.Zero(t, empty.Reviews.Total)
	require.True(t, empty.Reviews.Complete)
}

func TestSnapshotCheckCursorUsesLatestContextsBeyondFirstFetch(t *testing.T) {
	unittest.PrepareTestEnv(t)
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var rows []*git_model.CommitStatus
	for i := range 510 {
		context := fmt.Sprintf("context/%d", i)
		rows = append(rows, &git_model.CommitStatus{
			ID: int64(10000 + i), RepoID: 1, SHA: sha, Index: int64(1 + i),
			Context: context, ContextHash: fmt.Sprintf("%x", sha1.Sum([]byte(context))), State: "success", CreatorID: 2,
			CreatedUnix: timeutil.TimeStamp(1700001000 - i), UpdatedUnix: timeutil.TimeStamp(1700002000),
		})
	}
	// A later native index can have an earlier ID in retained/imported data.
	// Its older high-ID row must not reappear after the cursor.
	rows = append(rows, &git_model.CommitStatus{
		ID: 9999, RepoID: 1, SHA: sha, Index: 511,
		Context: rows[509].Context, ContextHash: rows[509].ContextHash, State: "failure", CreatorID: 2,
		CreatedUnix: timeutil.TimeStamp(1700001000), UpdatedUnix: timeutil.TimeStamp(1700002000),
	})
	unittest.AssertSuccessfulInsert(t, rows)
	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyChecks})
	req.SHA, req.Limit = sha, 7
	svc := NewService()
	first, err := svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.Equal(t, 510, first.Checks.Total)
	require.False(t, first.Checks.Complete)
	require.Len(t, first.Checks.Items, 7)
	require.Equal(t, "9999", first.Checks.Items[0].ID)
	require.Equal(t, "failure", first.Checks.Items[0].State)
	require.Equal(t, int64(511), first.Checks.Items[0].Index)

	req.Cursor = "10501"
	last, err := svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.Equal(t, 7, last.Checks.Total)
	require.True(t, last.Checks.Complete)
	require.Len(t, last.Checks.Items, 7)
	for i, item := range last.Checks.Items {
		require.Equal(t, strconv.Itoa(10502+i), item.ID)
		require.NotEqual(t, "context/509", item.Context, "latest selection precedes cursor filtering")
	}
}

func TestSnapshotThreadsRefuseIncompleteIssueContext(t *testing.T) {
	unittest.PrepareTestEnv(t)
	issue, _ := threadFixture(t)
	review := insertThreadReview(t, issue.ID, 2, issues_model.ReviewTypeComment)
	var rows []*issues_model.Comment
	for i := range snapshotFetchCap {
		rows = append(rows, &issues_model.Comment{
			ID: int64(10000 + i), IssueID: issue.ID, PosterID: 2, Type: issues_model.CommentTypeCode,
			ReviewID: review.ID, TreePath: "bounded.go", Line: 1, Content: "retained reply", ContentVersion: 1,
			CommitSHA:   snapshotBranch2,
			CreatedUnix: timeutil.TimeStamp(1700001000 + i), UpdatedUnix: timeutil.TimeStamp(1700002000),
		})
	}
	rows[1].OriginalAnchor = &issues_model.CodeCommentAnchor{
		Base: snapshotMaster, Head: snapshotBranch2, Path: "bounded.go", Side: "new", Scope: "lines", StartLine: 1, EndLine: 1,
	}
	other := *rows[1].OriginalAnchor
	other.Base = snapshotProofSHA
	rows[len(rows)-1].OriginalAnchor = &other
	unittest.AssertSuccessfulInsert(t, rows)
	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyReviewThreads})
	req.PullNumber, req.Limit = "3", 1
	svc := NewService()
	_, err := svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.ErrorIs(t, err, ErrSnapshotUnavailable, "later contexts must not change an inferred prefix identity")
	// A cursor beyond the bounded prefix must not turn unknown evidence into
	// a complete empty page either.
	req.Cursor = "20000"
	_, err = svc.ReadSnapshot(t.Context(), snapshotAuthority(2, req), req)
	require.ErrorIs(t, err, ErrSnapshotUnavailable)
}

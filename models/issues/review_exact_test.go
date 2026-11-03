// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	exactHeadOID = "1111111111111111111111111111111111111111"
	exactBaseOID = "2222222222222222222222222222222222222222"
)

// exactTargetForIssue11 binds PR 5 (issue 11, user1's open same-repo PR from
// pr-to-update onto branch2) at arbitrary full OIDs.
func exactTargetForIssue11() issues_model.ExactReviewTarget {
	return issues_model.ExactReviewTarget{
		PRIndex:        5,
		AuthorID:       1,
		HeadRepoID:     1,
		HeadBranch:     "pr-to-update",
		BaseBranch:     "branch2",
		HeadOID:        exactHeadOID,
		BaseOID:        exactBaseOID,
		CommitID:       exactHeadOID,
		CurrentHeadOID: exactHeadOID,
		CurrentBaseOID: exactBaseOID,
		DiffBaseOID:    exactBaseOID,
	}
}

// exactTargetForIssue3 binds PR 2 (issue 3, user1's open same-repo PR from
// branch2 onto master) at arbitrary full OIDs.
func exactTargetForIssue3() issues_model.ExactReviewTarget {
	return issues_model.ExactReviewTarget{
		PRIndex:        3,
		AuthorID:       1,
		HeadRepoID:     1,
		HeadBranch:     "branch2",
		BaseBranch:     "master",
		HeadOID:        exactHeadOID,
		BaseOID:        exactBaseOID,
		CommitID:       exactHeadOID,
		CurrentHeadOID: exactHeadOID,
		CurrentBaseOID: exactBaseOID,
		DiffBaseOID:    exactBaseOID,
	}
}

func TestSubmitExactReviewApprove(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext

	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 11})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	review, comm, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeApprove, "looks good", exactTargetForIssue11())
	require.NoError(t, err)
	require.NotNil(t, review)
	require.NotNil(t, comm)

	assert.Equal(t, issues_model.ReviewTypeApprove, review.Type)
	assert.True(t, review.Official)
	assert.False(t, review.Stale)
	assert.False(t, review.Dismissed)
	assert.Equal(t, exactHeadOID, review.CommitID)
	assert.Equal(t, issues_model.CommentTypeReview, comm.Type)
	assert.Equal(t, "looks good", comm.Content)
	assert.Equal(t, review.ID, comm.ReviewID)

	// No pending draft is created or left behind for the exact path.
	_, err = issues_model.GetCurrentReview(ctx, doer, issue)
	require.Error(t, err)
	assert.True(t, issues_model.IsErrReviewNotExist(err))
}

func TestSubmitExactReviewReject(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext

	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 11})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	review, comm, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeReject, "needs work", exactTargetForIssue11())
	require.NoError(t, err)
	assert.Equal(t, issues_model.ReviewTypeReject, review.Type)
	assert.True(t, review.Official)
	assert.False(t, review.Stale)
	assert.Equal(t, "needs work", comm.Content)
}

func TestSubmitExactReviewRefusals(t *testing.T) {
	mkTarget11 := exactTargetForIssue11
	mkTarget3 := exactTargetForIssue3

	otherHead := strings.Repeat("3", 40)
	otherBase := strings.Repeat("4", 40)

	cases := []struct {
		name    string
		issueID int64
		doerID  int64
		mutate  func(*issues_model.ExactReviewTarget) issues_model.ReviewType
		reason  string
	}{
		{
			name: "comment type unsupported", issueID: 11, doerID: 2,
			mutate: func(want *issues_model.ExactReviewTarget) issues_model.ReviewType {
				*want = mkTarget11()
				return issues_model.ReviewTypeComment
			},
			reason: "only approve or reject reviews are supported",
		},
		{
			name: "self review", issueID: 11, doerID: 1,
			mutate: func(want *issues_model.ExactReviewTarget) issues_model.ReviewType {
				*want = mkTarget11()
				return issues_model.ReviewTypeApprove
			},
			reason: "cannot review your own pull request",
		},
	}

	t.Run("table", func(t *testing.T) {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				require.NoError(t, unittest.PrepareTestDatabase())
				ctx := db.DefaultContext
				issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: tc.issueID})
				doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: tc.doerID})
				var want issues_model.ExactReviewTarget
				reviewType := tc.mutate(&want)
				_, _, err := issues_model.SubmitExactReview(ctx, doer, issue, reviewType, "body", want)
				require.Error(t, err)
				require.True(t, issues_model.IsErrExactReviewRefused(err), "expected exact refusal, got %v", err)
				assert.Contains(t, err.Error(), tc.reason)
			})
		}
	})

	t.Run("merged pr", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		ctx := db.DefaultContext
		issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 2})
		doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		want := issues_model.ExactReviewTarget{
			PRIndex: 2, AuthorID: 1, HeadRepoID: 1, HeadBranch: "branch1", BaseBranch: "master",
			HeadOID: exactHeadOID, BaseOID: exactBaseOID, CommitID: exactHeadOID,
			CurrentHeadOID: exactHeadOID, CurrentBaseOID: exactBaseOID,
		}
		_, _, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeApprove, "body", want)
		require.Error(t, err)
		assert.True(t, issues_model.IsErrExactReviewRefused(err))
		assert.Contains(t, err.Error(), "closed or merged")
	})

	t.Run("fork pr unsupported", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		ctx := db.DefaultContext
		issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 8})
		doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		want := issues_model.ExactReviewTarget{
			PRIndex: 1, AuthorID: 11, HeadRepoID: 11, HeadBranch: "branch2", BaseBranch: "master",
			HeadOID: exactHeadOID, BaseOID: exactBaseOID, CommitID: exactHeadOID,
			CurrentHeadOID: exactHeadOID, CurrentBaseOID: exactBaseOID,
		}
		_, _, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeApprove, "body", want)
		require.Error(t, err)
		assert.True(t, issues_model.IsErrExactReviewRefused(err))
		assert.Contains(t, err.Error(), "cross-repository")
	})

	t.Run("binding mismatches", func(t *testing.T) {
		bindings := []struct {
			name   string
			mutate func(*issues_model.ExactReviewTarget)
			reason string
		}{
			{"pr index", func(w *issues_model.ExactReviewTarget) { w.PRIndex = 99 }, "pr index mismatch"},
			{"pr author", func(w *issues_model.ExactReviewTarget) { w.AuthorID = 2 }, "pr author mismatch"},
			{"head repo", func(w *issues_model.ExactReviewTarget) { w.HeadRepoID = 2 }, "head repository mismatch"},
			{"head branch", func(w *issues_model.ExactReviewTarget) { w.HeadBranch = "other" }, "head branch mismatch"},
			{"base branch", func(w *issues_model.ExactReviewTarget) { w.BaseBranch = "other" }, "base branch mismatch"},
			{"invalid oid", func(w *issues_model.ExactReviewTarget) { w.CommitID = "xyz" }, "invalid object id"},
			{"commit not head", func(w *issues_model.ExactReviewTarget) { w.CommitID = exactBaseOID }, "commit must equal the expected head"},
			{"head changed", func(w *issues_model.ExactReviewTarget) { w.CurrentHeadOID = otherHead }, "head changed"},
			{"base changed", func(w *issues_model.ExactReviewTarget) { w.CurrentBaseOID = otherBase }, "base changed"},
		}
		for _, bc := range bindings {
			t.Run(bc.name, func(t *testing.T) {
				require.NoError(t, unittest.PrepareTestDatabase())
				ctx := db.DefaultContext
				issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 11})
				doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
				want := mkTarget11()
				bc.mutate(&want)
				_, _, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeApprove, "body", want)
				require.Error(t, err)
				require.True(t, issues_model.IsErrExactReviewRefused(err), "expected exact refusal, got %v", err)
				assert.Contains(t, err.Error(), bc.reason)
			})
		}
	})

	t.Run("pending draft refuses and stays untouched", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		ctx := db.DefaultContext
		issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 3})
		doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

		_, _, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeApprove, "body", mkTarget3())
		require.Error(t, err)
		require.True(t, issues_model.IsErrExactReviewRefused(err))
		assert.Contains(t, err.Error(), "pending draft exists")

		// The draft is neither adopted nor deleted.
		pending, err := issues_model.GetCurrentReview(ctx, doer, issue)
		require.NoError(t, err)
		assert.Equal(t, int64(6), pending.ID)
		assert.Equal(t, issues_model.ReviewTypePending, pending.Type)
	})

	t.Run("blocked reviewer refuses", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		ctx := db.DefaultContext
		// The issue poster (user1) blocks the reviewer (user2).
		require.NoError(t, db.Insert(ctx, &user_model.BlockedUser{UserID: 1, BlockID: 2}))
		issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 11})
		doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		_, _, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeApprove, "body", mkTarget11())
		require.Error(t, err)
		require.True(t, issues_model.IsErrExactReviewRefused(err))
		assert.Contains(t, err.Error(), "reviewer is blocked")
	})

	t.Run("reject requires body", func(t *testing.T) {
		require.NoError(t, unittest.PrepareTestDatabase())
		ctx := db.DefaultContext
		issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 11})
		doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		_, _, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeReject, "   ", mkTarget11())
		require.Error(t, err)
		assert.True(t, issues_model.IsContentEmptyErr(err))
	})
}

func TestSubmitExactReviewSupersedesPriorApproval(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext

	// user4 holds approve review 8 on issue 3; user4 can read public repo1
	// but has no write access there, so the new review is unofficial.
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 3})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})

	review, _, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeReject, "changed my mind", exactTargetForIssue3())
	require.NoError(t, err)
	assert.False(t, review.Official)

	prior := unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: 8})
	assert.True(t, prior.Dismissed)
	assert.False(t, prior.Official)
}

func TestSubmitExactReviewJoinsOuterTransaction(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	// The later conditional operation commits the receipt in the same SQL
	// transaction as the primary review rows: the model call must join the
	// caller's transaction instead of committing early.
	t.Run("outer rollback removes rows", func(t *testing.T) {
		issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 11})
		doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		forced := errors.New("force outer rollback")
		err := db.WithTx(db.DefaultContext, func(ctx context.Context) error {
			review, comm, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeApprove, "nested", exactTargetForIssue11())
			require.NoError(t, err)
			require.NotZero(t, review.ID)
			require.NotZero(t, comm.ID)
			return forced
		})
		require.ErrorIs(t, err, forced)

		reviews, err := issues_model.FindReviews(db.DefaultContext, issues_model.FindReviewOptions{
			IssueID: 11, ReviewerID: 2,
			Types: []issues_model.ReviewType{issues_model.ReviewTypeApprove},
		})
		require.NoError(t, err)
		assert.Empty(t, reviews)
	})

	t.Run("outer commit keeps rows", func(t *testing.T) {
		issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 11})
		doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		require.NoError(t, db.WithTx(db.DefaultContext, func(ctx context.Context) error {
			_, _, err := issues_model.SubmitExactReview(ctx, doer, issue, issues_model.ReviewTypeApprove, "nested", exactTargetForIssue11())
			return err
		}))

		reviews, err := issues_model.FindReviews(db.DefaultContext, issues_model.FindReviewOptions{
			IssueID: 11, ReviewerID: 2,
			Types: []issues_model.ReviewType{issues_model.ReviewTypeApprove},
		})
		require.NoError(t, err)
		require.Len(t, reviews, 1)
		assert.Equal(t, "nested", reviews[0].Content)
	})
}

func TestSubmitReviewCommentStaysUncounted(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext

	// Plain comment reviews never count as official, even from a writer.
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 11})
	require.NoError(t, issue.LoadRepo(ctx))
	require.NoError(t, issue.Repo.LoadOwner(ctx))
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	review, _, err := issues_model.SubmitReview(ctx, doer, issue, issues_model.ReviewTypeComment, "a remark", exactHeadOID, false, nil)
	require.NoError(t, err)
	assert.Equal(t, issues_model.ReviewTypeComment, review.Type)
	assert.False(t, review.Official)
}

func TestCodeCommentsSortedList(t *testing.T) {
	comments := issues_model.CodeComments{
		"b.md": {
			4: {{ID: 9}, {ID: 3}},
		},
		"a.md": {
			5:  {{ID: 7}},
			-1: {{ID: 1}},
		},
	}
	got := comments.SortedList()
	require.Len(t, got, 4)
	assert.Equal(t, int64(1), got[0].ID)
	assert.Equal(t, int64(7), got[1].ID)
	assert.Equal(t, int64(3), got[2].ID)
	assert.Equal(t, int64(9), got[3].ID)
	assert.Empty(t, issues_model.CodeComments{}.SortedList())
}

func TestResolveMentionsDeterministicOrder(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext

	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 11})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})

	// Mention order in the text must not affect the resolved order.
	for range 5 {
		users, err := issues_model.ResolveIssueMentionsByVisibility(ctx, issue, doer, []string{"user5", "user4"})
		require.NoError(t, err)
		require.Len(t, users, 2)
		assert.Equal(t, int64(4), users[0].ID)
		assert.Equal(t, int64(5), users[1].ID)
	}
}

func TestGetReviewsByIssueIDTiebreak(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ctx := db.DefaultContext

	// Force an updated_unix tie between reviews 7 and 8 on issue 3; the id
	// tiebreak keeps the list deterministic.
	_, err := db.Exec(ctx, "UPDATE `review` SET updated_unix=? WHERE id IN (7, 8)", 946684900)
	require.NoError(t, err)

	reviews, err := issues_model.GetReviewsByIssueID(ctx, 3)
	require.NoError(t, err)
	ids := make([]int64, 0, len(reviews))
	for _, r := range reviews {
		ids = append(ids, r.ID)
	}
	// Review 9 (updated 946684814) first, then the tied 7 and 8 by id.
	assert.Equal(t, []int64{9, 7, 8}, ids)
}

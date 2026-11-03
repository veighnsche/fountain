// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"strconv"
	"testing"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/timeutil"
	pull_service "forgejo.org/services/pull"

	"github.com/stretchr/testify/require"
)

// FW17 thread/eligibility proof: paged native conversation evidence with
// root/location/commit/resolver fields and digests, and the actor-bound
// merge-eligibility read. Reads run against fixture PR index 3 (open,
// branch2 into master on repo1) with inserted code conversations.

// insertThreadReview records one native review row for conversation tests.
func insertThreadReview(t *testing.T, issueID, reviewerID int64, reviewType issues_model.ReviewType) *issues_model.Review {
	t.Helper()
	review := &issues_model.Review{
		IssueID: issueID, ReviewerID: reviewerID, Type: reviewType,
		CommitID: snapshotBranch2, Content: "thread review",
		CreatedUnix: timeutil.TimeStamp(1700000000), UpdatedUnix: timeutil.TimeStamp(1700000100),
	}
	unittest.AssertSuccessfulInsert(t, review)
	return review
}

// insertThreadComment records one code comment in the named conversation.
func insertThreadComment(t *testing.T, issue *issues_model.Issue, doer *user_model.User, reviewID int64, path string, line int64, commit, body string) *issues_model.Comment {
	t.Helper()
	comment, err := issues_model.CreateComment(t.Context(), &issues_model.CreateCommentOptions{
		Type: issues_model.CommentTypeCode, Doer: doer, Repo: issue.Repo, Issue: issue,
		ReviewID: reviewID, TreePath: path, LineNum: line, CommitSHA: commit, Content: body,
	})
	require.NoError(t, err)
	return comment
}

func threadFixture(t *testing.T) (*issues_model.Issue, *user_model.User) {
	t.Helper()
	ctx := t.Context()
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 3})
	require.NoError(t, issue.LoadRepo(ctx))
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	// Fixture code comment 7 is a legacy outdated thread (invalidated,
	// no recorded commit) with an unset update stamp that predates the
	// snapshot converters. Repair the stamp so it converts as the
	// outdated unresolved evidence it is; fixtures reload per test.
	legacy := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: 7})
	legacy.UpdatedUnix = legacy.CreatedUnix
	_, err := db.GetEngine(ctx).ID(legacy.ID).Cols("updated_unix").Update(legacy)
	require.NoError(t, err)
	return issue, doer
}

// legacyThreadDigest is the expected digest of the repaired legacy
// outdated thread on the fixture PR.
func legacyThreadDigest(t *testing.T) string {
	t.Helper()
	legacy := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: 7})
	return expectedThreadDigest(t, legacy)
}

func threadLocationFor(path string, line int64) (string, string) {
	side := sdk.ThreadSideNew
	if line == 0 {
		side = ""
	}
	unsigned := line
	if line < 0 {
		side, unsigned = sdk.ThreadSideOld, -line
	}
	return side, sdk.RenderThreadLocation(path, side, unsigned)
}

// expectedThreadDigest recomputes the conversation digest from live rows,
// trusting only stored bodies/versions plus the shared rendering.
func expectedThreadDigest(t *testing.T, comments ...*issues_model.Comment) string {
	t.Helper()
	root := comments[0]
	_, location := threadLocationFor(root.TreePath, root.Line)
	lines := make([]string, 0, len(comments))
	for _, comment := range comments {
		lines = append(lines, sdk.ResolveConversationLine(sdk.ResolveConversationMember{
			OriginalAnchor: threadOriginalAnchor(comment.OriginalAnchor),
			ID:             comment.ID,
			Body:           comment.Content,
			ContentVer:     int64(comment.ContentVersion),
			Location:       location,
			ResolverState:  sdk.ThreadResolverState(comment.ResolveDoerID),
		}))
	}
	digest, err := sdk.ResolveConversationDigest(lines)
	require.NoError(t, err)
	return digest
}

func TestSnapshotReadThreadsExposeRootsAndDigests(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := NewService()
	issue, doer := threadFixture(t)

	review := insertThreadReview(t, issue.ID, 2, issues_model.ReviewTypeComment)
	root := insertThreadComment(t, issue, doer, review.ID, "a.go", 4, snapshotMaster, "needs a bounds check")
	reply := insertThreadComment(t, issue, doer, review.ID, "a.go", 4, snapshotMaster, "added in the next push")

	freshRoot := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: root.ID})
	freshReply := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: reply.ID})

	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyReviewThreads})
	req.PullNumber = "3"
	snapshot, err := svc.ReadSnapshot(ctx, snapshotAuthority(2, req), req)
	require.NoError(t, err)
	page := snapshot.Threads
	require.NotNil(t, page)
	require.True(t, page.Complete, "fully evidenced page must complete")
	require.Equal(t, 2, page.Total, "the legacy outdated thread is retained")
	require.Empty(t, page.NextCursor)
	require.Len(t, page.Items, 2)
	legacy := page.Items[0]
	require.Equal(t, "7", legacy.RootID)
	require.Equal(t, "README.md", legacy.Path)
	require.Equal(t, "old", legacy.Side)
	require.Equal(t, int64(4), legacy.Line)
	require.True(t, legacy.Outdated, "invalidated roots stay outdated")
	require.False(t, legacy.Resolved)
	require.Equal(t, 1, legacy.MemberTotal)
	require.Equal(t, legacyThreadDigest(t), legacy.Digest)
	thread := page.Items[1]
	require.True(t, thread.Visible)
	require.True(t, thread.Complete)
	require.Equal(t, strconv.FormatInt(root.ID, 10), thread.RootID)
	require.Equal(t, "3", thread.IssueID)
	require.Equal(t, strconv.FormatInt(review.ID, 10), thread.ReviewID)
	require.Equal(t, "a.go", thread.Path)
	require.Equal(t, "new", thread.Side)
	require.Equal(t, int64(4), thread.Line)
	require.Equal(t, snapshotMaster, thread.OriginalCommit)
	require.Equal(t, snapshotBranch2, thread.HeadCommit)
	require.True(t, thread.Outdated, "commit drift retains the outdated thread")
	require.False(t, thread.Resolved)
	require.Empty(t, thread.ResolverID)
	require.Equal(t, 2, thread.MemberTotal)
	require.Len(t, thread.Members, 2)
	require.Equal(t, expectedThreadDigest(t, freshRoot, freshReply), thread.Digest)

	// The same conversations resolve by issue selector too.
	byIssue := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyReviewThreads})
	byIssue.IssueIndex = "3"
	again, err := svc.ReadSnapshot(ctx, snapshotAuthority(2, byIssue), byIssue)
	require.NoError(t, err)
	require.True(t, again.Threads.Complete)
	require.Equal(t, thread.Digest, again.Threads.Items[1].Digest)
}

func TestSnapshotReadThreadsPageAcrossOneRevision(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := NewService()
	issue, doer := threadFixture(t)

	review := insertThreadReview(t, issue.ID, 2, issues_model.ReviewTypeComment)
	roots := []int64{7}
	for i, path := range []string{"a.go", "b.go", "c.go"} {
		root := insertThreadComment(t, issue, doer, review.ID, path, int64(i+1), snapshotBranch2, "finding "+path)
		roots = append(roots, root.ID)
	}

	var seen []string
	cursor := ""
	for {
		req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyReviewThreads})
		req.PullNumber = "3"
		req.Limit = 1
		req.Cursor = cursor
		snapshot, err := svc.ReadSnapshot(ctx, snapshotAuthority(2, req), req)
		require.NoError(t, err)
		page := snapshot.Threads
		require.NotEmpty(t, page.Items)
		for _, item := range page.Items {
			require.True(t, item.Complete)
			seen = append(seen, item.RootID)
		}
		if page.Complete {
			require.Empty(t, page.NextCursor)
			break
		}
		require.NotEmpty(t, page.NextCursor)
		cursor = page.NextCursor
		require.Less(t, len(seen), 10, "paging must terminate")
	}
	require.Len(t, seen, 4, "the legacy outdated thread pages too")
	for i, id := range roots {
		require.Equal(t, strconv.FormatInt(id, 10), seen[i], "pages walk ascending roots")
	}
}

func TestSnapshotReadThreadsHidesPendingContent(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := NewService()
	issue, _ := threadFixture(t)
	author := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})

	review := insertThreadReview(t, issue.ID, 4, issues_model.ReviewTypePending)
	pending := insertThreadComment(t, issue, author, review.ID, "secret.go", 0, snapshotBranch2, "pending remark")
	pending.OriginalAnchor = &issues_model.CodeCommentAnchor{Base: snapshotMaster, Head: snapshotBranch2, Path: "secret.go", Side: "old", Scope: "file"}
	_, err := db.GetEngine(ctx).ID(pending.ID).Cols("original_anchor").Update(pending)
	require.NoError(t, err)

	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyReviewThreads})
	req.PullNumber = "3"
	snapshot, err := svc.ReadSnapshot(ctx, snapshotAuthority(2, req), req)
	require.NoError(t, err)
	page := snapshot.Threads
	require.False(t, page.Complete, "hidden evidence must block")
	require.Len(t, page.Items, 2, "the legacy thread stays listed beside the hidden one")
	var hidden *sdk.SnapshotThread
	for i := range page.Items {
		if !page.Items[i].Visible {
			hidden = &page.Items[i]
		}
	}
	require.NotNil(t, hidden, "exactly the pending thread hides")
	require.Empty(t, hidden.Digest, "hidden threads carry no digest")
	require.Empty(t, hidden.Members)
	require.Nil(t, hidden.OriginalAnchor)
	require.Empty(t, hidden.Path)
	require.Empty(t, hidden.Side)

	// The pending author observes its own conversation completely.
	ownReq := snapshotProofRequest("1", "4", []string{sdk.SnapshotFamilyReviewThreads})
	ownReq.PullNumber = "3"
	own, err := svc.ReadSnapshot(ctx, snapshotAuthority(4, ownReq), ownReq)
	require.NoError(t, err)
	require.True(t, own.Threads.Complete)
	require.Len(t, own.Threads.Items, 2)
	for _, item := range own.Threads.Items {
		require.True(t, item.Visible)
		require.NotEmpty(t, item.Digest)
		if item.RootID == snapshotIDString(pending.ID) {
			require.Equal(t, threadOriginalAnchor(pending.OriginalAnchor), item.OriginalAnchor)
			require.Len(t, item.Members, 1)
			require.Equal(t, item.OriginalAnchor, item.Members[0].OriginalAnchor)
			require.Zero(t, item.Line)
			require.Empty(t, item.Side)
		}
	}
}

func TestSnapshotReadEligibilityAgreesWithEvaluator(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := NewService()

	// The read verdict agrees with the evaluator behind
	// CheckPullMergeable for the same actor, whatever it reports.
	pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 2})
	require.NoError(t, pr.LoadIssue(ctx))
	actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repository := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	permission, err := access_model.GetUserRepoPermission(ctx, repository, actor)
	require.NoError(t, err)
	evalErr := pull_service.CheckPullMergeable(ctx, actor, &permission, pr, pull_service.MergeCheckTypeGeneral, false)

	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyMergeEligibility})
	req.PullNumber = "3"
	snapshot, err := svc.ReadSnapshot(ctx, snapshotAuthority(2, req), req)
	require.NoError(t, err)
	got := snapshot.Eligibility
	require.NotNil(t, got)
	require.True(t, got.Visible)
	require.True(t, got.Complete)
	require.Equal(t, "3", got.PullNumber)
	require.Equal(t, evalErr == nil, got.Eligible)
	if evalErr == nil {
		require.Empty(t, got.Blocker)
	} else {
		require.NotEmpty(t, got.Blocker)
	}
}

func TestSnapshotReadEligibilityNamesApprovalBlocker(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := NewService()
	issue, doer := threadFixture(t)
	_ = doer

	rule := &git_model.ProtectedBranch{RepoID: 1, RuleName: "master", RequiredApprovals: 1}
	unittest.AssertSuccessfulInsert(t, rule)

	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyMergeEligibility})
	req.PullNumber = "3"
	blocked, err := svc.ReadSnapshot(ctx, snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.False(t, blocked.Eligibility.Eligible)
	require.Equal(t, sdk.MergeBlockerApprovals, blocked.Eligibility.Blocker)

	// The configured reviewer supplying the missing approval clears the
	// read blocker without repeated reviews.
	approval := &issues_model.Review{
		IssueID: issue.ID, ReviewerID: 2, Type: issues_model.ReviewTypeApprove,
		CommitID: snapshotBranch2, Content: "looks good", Official: true,
		CreatedUnix: timeutil.TimeStamp(1700000000), UpdatedUnix: timeutil.TimeStamp(1700000100),
	}
	unittest.AssertSuccessfulInsert(t, approval)
	clear, err := svc.ReadSnapshot(ctx, snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.True(t, clear.Eligibility.Eligible, "approval blocker got %q", clear.Eligibility.Blocker)
	require.Empty(t, clear.Eligibility.Blocker)
}

func TestSnapshotReadThreadsOnPlainIssueIsEmpty(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := NewService()

	req := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyReviewThreads})
	req.IssueIndex = "1"
	snapshot, err := svc.ReadSnapshot(ctx, snapshotAuthority(2, req), req)
	require.NoError(t, err)
	require.True(t, snapshot.Threads.Complete)
	require.Empty(t, snapshot.Threads.Items)
}

func TestSnapshotReadThreadsAndEligibilityRefuseGatedReads(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := NewService()

	// Actor without repository access sees the same outcome: user4 cannot
	// read user2's private repo2, so gated reads share not_found.
	deniedReq := snapshotProofRequest("2", "4", []string{sdk.SnapshotFamilyReviewThreads})
	deniedReq.IssueIndex = "1"
	_, err := svc.ReadSnapshot(ctx, snapshotAuthority(4, deniedReq), deniedReq)
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	deniedEligibility := snapshotProofRequest("2", "4", []string{sdk.SnapshotFamilyMergeEligibility})
	deniedEligibility.PullNumber = "3"
	_, err = svc.ReadSnapshot(ctx, snapshotAuthority(4, deniedEligibility), deniedEligibility)
	require.ErrorIs(t, err, ErrSnapshotNotFound)

	// Missing pulls share not_found identically.
	missing := snapshotProofRequest("1", "2", []string{sdk.SnapshotFamilyMergeEligibility})
	missing.PullNumber = "9999"
	_, err = svc.ReadSnapshot(ctx, snapshotAuthority(2, missing), missing)
	require.ErrorIs(t, err, ErrSnapshotNotFound)
}

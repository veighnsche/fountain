// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/git"
	"forgejo.org/modules/json"

	"github.com/stretchr/testify/require"
)

// SUPERVISOR-046 producer proof: the native review-submit producer derives
// the merge base for the already-verified ExpectedBaseOID/ExpectedHeadOID
// even when Comments==0, before any native review/inline/comment write, and
// the receipt certifies the exact admitted tuple. Runs on disposable PG
// (FORGEJO_TEST_PG_DSN) against the real fixture git repository.

// reviewMergeBaseDecision enrolls a write-scoped binding for the review
// submission operation and returns its submission decision.
func reviewMergeBaseDecision(t *testing.T, actorID int64) extensionauth_model.SubmissionDecision {
	t.Helper()
	ctx := t.Context()
	token := &auth_model.AccessToken{
		UID: actorID, Name: "review-merge-base",
		Scope: auth_model.AccessTokenScopeWriteRepository, ResourceAllRepos: true,
	}
	require.NoError(t, auth_model.NewAccessToken(ctx, token))
	_, err := extensionauth_model.EnrollBinding(ctx, operationTestInstallation, token.ID, actorID, 1, nativeoperation_model.KindReviewSubmit)
	require.NoError(t, err)
	return extensionauth_model.SubmissionDecision{TokenID: token.ID, ActorID: actorID, CredentialFingerprint: extensionauth_model.CredentialFingerprint(token.TokenHash, token.TokenSalt)}
}

// reviewMergeBaseIntent builds a comment-free APPROVED intent bound to the
// exact admitted OIDs. Comments stays empty: this is the zero-inline path.
func reviewMergeBaseIntent(t *testing.T, id, headOID, baseOID string) *ValidIntent {
	t.Helper()
	r, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	now := time.Now().Unix()
	raw := []byte(reviewSubmitPayload(3, 1, 1,
		"refs/heads/branch2", "refs/heads/master",
		headOID, baseOID, headOID, "APPROVED", "looks good"))
	intent, err := ValidateIntent(id, 2, 1, nativeoperation_model.KindReviewSubmit, "accepted-revision", r.Revision, now+300, raw, now)
	require.NoError(t, err)
	require.NotNil(t, intent.ReviewSubmit)
	require.Empty(t, intent.ReviewSubmit.Comments)
	return intent
}

// reviewMergeBaseSetup clears user2's pending fixture draft on issue 3 so
// the exact path reaches the producer instead of refusing on the draft.
func reviewMergeBaseSetup(t *testing.T) {
	t.Helper()
	_, err := db.GetEngine(t.Context()).Where("reviewer_id = ? AND issue_id = ? AND type = ?",
		2, 3, issues_model.ReviewTypePending).Delete(new(issues_model.Review))
	require.NoError(t, err)
}

func reviewMergeBaseTips(t *testing.T, svc *Service) (repoPath, head, base string) {
	t.Helper()
	ctx := t.Context()
	repository := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	head, err := svc.readRef(ctx, repository.RepoPath(), git.BranchPrefix+"branch2")
	require.NoError(t, err)
	base, err = svc.readRef(ctx, repository.RepoPath(), git.BranchPrefix+"master")
	require.NoError(t, err)
	require.NotEmpty(t, head)
	require.NotEmpty(t, base)
	return repository.RepoPath(), head, base
}

// gitMergeBase is the independent oracle: real git merge-base of the two
// admitted OIDs, never the producer under test.
func gitMergeBase(t *testing.T, repoPath, base, head string) string {
	t.Helper()
	cmd := exec.Command("git", "merge-base", base, head)
	cmd.Dir = repoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), stderr.String())
	return strings.ToLower(strings.TrimSpace(stdout.String()))
}

func gitRun(t *testing.T, repoPath string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), stderr.String())
	return strings.TrimSpace(stdout.String())
}

// gitAdvanceBranch mints one new commit object on top of base (or orphaned
// without parents when orphan is true) and moves the named branch to it.
// The fixture repository copy is per-test, so this never leaks.
func gitAdvanceBranch(t *testing.T, repoPath, branch, base, message string, orphan bool) string {
	t.Helper()
	tree := gitRun(t, repoPath, nil, "rev-parse", base+"^{tree}")
	identity := []string{
		"GIT_AUTHOR_NAME=merge-base-proof", "GIT_AUTHOR_EMAIL=proof@localhost",
		"GIT_COMMITTER_NAME=merge-base-proof", "GIT_COMMITTER_EMAIL=proof@localhost",
	}
	args := []string{"commit-tree", tree, "-m", message}
	if !orphan {
		args = append(args, "-p", base)
	}
	minted := gitRun(t, repoPath, identity, args...)
	require.True(t, validOID(minted))
	gitRun(t, repoPath, nil, "update-ref", git.BranchPrefix+branch, minted)
	return strings.ToLower(minted)
}

func reviewMergeBaseCounts(t *testing.T) (reviews, comments int64) {
	t.Helper()
	ctx := t.Context()
	var err error
	reviews, err = issues_model.CountReviews(ctx, issues_model.FindReviewOptions{IssueID: 3})
	require.NoError(t, err)
	// Every comment row for the issue, of any type: the zero-inline path
	// must add exactly the summary review comment and no inline rows.
	comments, err = db.GetEngine(ctx).Where("issue_id = ?", 3).Count(new(issues_model.Comment))
	require.NoError(t, err)
	return reviews, comments
}

func TestReviewMergeBaseProducerOnPostgres(t *testing.T) {
	useEnrolledAuthorityPostgres(t)
	t.Run("zero comments emit live merge-base", testReviewSubmitZeroCommentsEmitsLiveMergeBase)
	t.Run("divergent base head", testReviewSubmitZeroCommentsDivergentBaseHead)
	t.Run("stale head refuses before rows", testReviewSubmitStaleHeadRefusesBeforeRows)
	t.Run("unrelated histories refuse before rows", testReviewSubmitUnrelatedHistoriesRefuse)
}

func testReviewSubmitZeroCommentsEmitsLiveMergeBase(t *testing.T) {
	unittest.PrepareTestEnv(t)
	useIsolatedAppData(t)
	reviewMergeBaseSetup(t)
	ctx := t.Context()
	svc := NewService()
	decision := reviewMergeBaseDecision(t, 2)

	repoPath, head, base := reviewMergeBaseTips(t, svc)
	require.Equal(t, snapshotBranch2, head, "fixture head identity")
	require.Equal(t, snapshotMaster, base, "fixture base identity")
	oracle := gitMergeBase(t, repoPath, base, head)

	beforeReviews, beforeComments := reviewMergeBaseCounts(t)

	intent := reviewMergeBaseIntent(t, "review-merge-base-ok", head, base)
	record, err := svc.Submit(ctx, decision, operationTestInstallation, intent)
	require.NoError(t, err)
	require.Equal(t, nativeoperation_model.EffectCommitted, record.EffectState, record.ReasonCode)
	var receipt ReviewSubmitReceipt
	require.NoError(t, json.Unmarshal(record.Receipt, &receipt))
	require.NotEmpty(t, receipt.MergeBaseOID, "a genuinely zero-inline-comment approval must certify a merge-base")
	require.True(t, validOID(receipt.MergeBaseOID))
	require.Equal(t, oracle, strings.ToLower(receipt.MergeBaseOID), "receipt merge-base equals real git merge-base of admitted OIDs")
	require.Equal(t, head, receipt.HeadOID)
	require.Equal(t, base, receipt.BaseOID)
	require.Equal(t, head, receipt.CommitID)
	require.Empty(t, receipt.Comments)

	stored := unittest.AssertExistsAndLoadBean(t, &issues_model.Review{ID: receipt.ReviewID})
	require.Equal(t, int64(2), stored.ReviewerID)
	require.Equal(t, head, stored.CommitID)
	unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: receipt.CommentID})

	afterReviews, afterComments := reviewMergeBaseCounts(t)
	require.Equal(t, beforeReviews+1, afterReviews, "exactly one review row committed")
	require.Equal(t, beforeComments+1, afterComments, "only the summary comment committed, no inline rows")

	// Exact replay returns the recorded receipt, still bound to the
	// admitted OIDs rather than any later tuple.
	replay, err := svc.Submit(ctx, decision, operationTestInstallation, intent)
	require.NoError(t, err)
	require.Equal(t, string(record.Receipt), string(replay.Receipt))

	// A second real candidate adds one known line. Exercise the conditional
	// producer, not just the model: the stored old side is the derived merge
	// base, independently checked against Git, and survives replay/head moves.
	work := t.TempDir()
	blobPath := filepath.Join(work, "anchor.txt")
	require.NoError(t, os.WriteFile(blobPath, []byte("persist original anchor\n"), 0o600))
	blob := gitRun(t, repoPath, nil, "hash-object", "-w", blobPath)
	indexEnv := []string{"GIT_INDEX_FILE=" + filepath.Join(work, "index")}
	gitRun(t, repoPath, indexEnv, "read-tree", head)
	gitRun(t, repoPath, indexEnv, "update-index", "--add", "--cacheinfo", "100644,"+blob+",original-anchor.txt")
	tree := gitRun(t, repoPath, indexEnv, "write-tree")
	identity := []string{"GIT_AUTHOR_NAME=anchor-proof", "GIT_AUTHOR_EMAIL=anchor@example.invalid", "GIT_COMMITTER_NAME=anchor-proof", "GIT_COMMITTER_EMAIL=anchor@example.invalid"}
	inlineHead := gitRun(t, repoPath, identity, "commit-tree", tree, "-p", head, "-m", "original anchor candidate")
	gitRun(t, repoPath, nil, "update-ref", git.BranchPrefix+"branch2", inlineHead)
	inlineBase := gitMergeBase(t, repoPath, base, inlineHead)
	var payload ReviewSubmitIntent
	require.NoError(t, json.Unmarshal([]byte(reviewSubmitPayload(3, 1, 1, "refs/heads/branch2", "refs/heads/master", inlineHead, base, inlineHead, "APPROVED", "inline anchor")), &payload))
	payload.Comments = []ReviewSubmitComment{{Key: "original", Path: "original-anchor.txt", Side: "new", Line: 1, Body: "retain original context"}}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	now := time.Now().Unix()
	inlineIntent, err := ValidateIntent("review-original-anchor", 2, 1, nativeoperation_model.KindReviewSubmit, "accepted-revision", reservation.Revision, now+300, raw, now)
	require.NoError(t, err)
	inlineRecord, err := svc.Submit(ctx, decision, operationTestInstallation, inlineIntent)
	require.NoError(t, err)
	require.Equal(t, nativeoperation_model.EffectCommitted, inlineRecord.EffectState, inlineRecord.ReasonCode)
	var inlineReceipt ReviewSubmitReceipt
	require.NoError(t, json.Unmarshal(inlineRecord.Receipt, &inlineReceipt))
	require.Equal(t, inlineBase, inlineReceipt.MergeBaseOID)
	require.Len(t, inlineReceipt.Comments, 1)
	storedInline, err := issues_model.GetCommentByID(ctx, inlineReceipt.Comments[0].CommentID)
	require.NoError(t, err)
	require.NotNil(t, storedInline.OriginalAnchor)
	original := *storedInline.OriginalAnchor
	require.Equal(t, inlineBase, original.Base)
	require.Equal(t, inlineHead, original.Head)
	require.Equal(t, "original-anchor.txt", original.Path)
	require.Equal(t, "new", original.Side)
	require.Equal(t, int64(1), original.StartLine)
	gitAdvanceBranch(t, repoPath, "branch2", inlineHead, "later unrelated display tip", false)
	inlineReplay, err := svc.Submit(ctx, decision, operationTestInstallation, inlineIntent)
	require.NoError(t, err)
	require.Equal(t, string(inlineRecord.Receipt), string(inlineReplay.Receipt))
	storedInline, err = issues_model.GetCommentByID(ctx, storedInline.ID)
	require.NoError(t, err)
	require.Equal(t, &original, storedInline.OriginalAnchor)
}

func testReviewSubmitZeroCommentsDivergentBaseHead(t *testing.T) {
	unittest.PrepareTestEnv(t)
	useIsolatedAppData(t)
	reviewMergeBaseSetup(t)
	ctx := t.Context()
	svc := NewService()
	decision := reviewMergeBaseDecision(t, 2)

	repoPath, head, base := reviewMergeBaseTips(t, svc)
	advanced := gitAdvanceBranch(t, repoPath, "master", base, "advance base past the merge-base", false)
	oracle := gitMergeBase(t, repoPath, advanced, head)
	require.Equal(t, base, oracle, "advancing base keeps the old tip as the merge-base")
	require.NotEqual(t, advanced, oracle, "the merge-base differs from the admitted base")

	record, err := svc.Submit(ctx, decision, operationTestInstallation, reviewMergeBaseIntent(t, "review-merge-base-divergent", head, advanced))
	require.NoError(t, err)
	require.Equal(t, nativeoperation_model.EffectCommitted, record.EffectState, record.ReasonCode)
	var receipt ReviewSubmitReceipt
	require.NoError(t, json.Unmarshal(record.Receipt, &receipt))
	require.Equal(t, oracle, strings.ToLower(receipt.MergeBaseOID))
	require.NotEqual(t, advanced, strings.ToLower(receipt.MergeBaseOID), "base is not itself the merge-base")
	require.Equal(t, head, receipt.HeadOID)
	require.Equal(t, advanced, receipt.BaseOID)
}

func testReviewSubmitStaleHeadRefusesBeforeRows(t *testing.T) {
	unittest.PrepareTestEnv(t)
	useIsolatedAppData(t)
	reviewMergeBaseSetup(t)
	ctx := t.Context()
	svc := NewService()
	decision := reviewMergeBaseDecision(t, 2)

	_, _, base := reviewMergeBaseTips(t, svc)
	beforeReviews, beforeComments := reviewMergeBaseCounts(t)

	record, err := svc.Submit(ctx, decision, operationTestInstallation, reviewMergeBaseIntent(t, "review-merge-base-stale", strings.Repeat("0", 40), base))
	require.NoError(t, err)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, record.EffectState)
	require.Equal(t, nativeoperation_model.ReasonStaleHead, record.ReasonCode)

	afterReviews, afterComments := reviewMergeBaseCounts(t)
	require.Equal(t, beforeReviews, afterReviews, "stale ref writes no review rows")
	require.Equal(t, beforeComments, afterComments, "stale ref writes no comment rows")
}

func testReviewSubmitUnrelatedHistoriesRefuse(t *testing.T) {
	unittest.PrepareTestEnv(t)
	useIsolatedAppData(t)
	reviewMergeBaseSetup(t)
	ctx := t.Context()
	svc := NewService()
	decision := reviewMergeBaseDecision(t, 2)

	repoPath, head, base := reviewMergeBaseTips(t, svc)
	orphan := gitAdvanceBranch(t, repoPath, "master", base, "orphan base without common history", true)
	beforeReviews, beforeComments := reviewMergeBaseCounts(t)

	record, err := svc.Submit(ctx, decision, operationTestInstallation, reviewMergeBaseIntent(t, "review-merge-base-orphan", head, orphan))
	require.NoError(t, err)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, record.EffectState)
	require.Equal(t, nativeoperation_model.ReasonNativeRefused, record.ReasonCode, "merge-base derivation failure refuses")

	afterReviews, afterComments := reviewMergeBaseCounts(t)
	require.Equal(t, beforeReviews, afterReviews, "derivation failure writes no review rows")
	require.Equal(t, beforeComments, afterComments, "derivation failure writes no comment rows")
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"testing"

	"forgejo.org/models"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/require"
)

func TestBranchProtectionDecisionKeepsNativeOrderAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blocker string
		reason  string
		adjust  func(*git_model.ProtectedBranch)
	}{
		{"status", issues_model.MergeBlockedStatusChecks, "Not all required status checks successful", func(pb *git_model.ProtectedBranch) { pb.EnableStatusCheck = true }},
		{"approvals", issues_model.MergeBlockedApprovals, "Does not have enough approvals", func(pb *git_model.ProtectedBranch) {}},
		{"rejected", issues_model.MergeBlockedRequestedChanges, "There are requested changes", func(pb *git_model.ProtectedBranch) { pb.RequiredApprovals = 0 }},
		{"requested", issues_model.MergeBlockedReviewRequests, "There are official review requests", func(pb *git_model.ProtectedBranch) {
			pb.RequiredApprovals, pb.BlockOnRejectedReviews = 0, false
		}},
		{"outdated", issues_model.MergeBlockedOutdatedBranch, "The head branch is behind the base branch", func(pb *git_model.ProtectedBranch) {
			pb.RequiredApprovals, pb.BlockOnRejectedReviews, pb.BlockOnOfficialReviewRequests = 0, false, false
		}},
		{"files", issues_model.MergeBlockedProtectedFiles, "Changed protected files", func(pb *git_model.ProtectedBranch) {
			pb.RequiredApprovals, pb.BlockOnRejectedReviews, pb.BlockOnOfficialReviewRequests, pb.BlockOnOutdatedBranch = 0, false, false, false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, unittest.PrepareTestDatabase())
			pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 2})
			pr.CommitsBehind, pr.ChangedProtectedFiles = 1, []string{"protected.go"}
			pb := &git_model.ProtectedBranch{
				RepoID: 1, RuleName: "master", StatusCheckContexts: []string{"required/missing"}, RequiredApprovals: 100,
				BlockOnRejectedReviews: true, BlockOnOfficialReviewRequests: true, BlockOnOutdatedBranch: true, ProtectedFilePatterns: "*.go",
			}
			tc.adjust(pb)
			unittest.AssertSuccessfulInsert(t, pb, &issues_model.Review{
				IssueID: pr.IssueID, ReviewerID: 2, Type: issues_model.ReviewTypeRequest, Official: true,
			})
			rule, blocker, err := checkPullBranchProtections(t.Context(), pr, false)
			require.Equal(t, pb.ID, rule.ID)
			require.Equal(t, tc.blocker, blocker)
			require.True(t, models.IsErrDisallowedToMerge(err), "native direct-type checks must still work")
			require.Equal(t, models.ErrDisallowedToMerge{Reason: tc.reason}, err)
			legacyRule, legacyErr := CheckPullBranchProtections(t.Context(), pr, false)
			require.Equal(t, rule.ID, legacyRule.ID)
			require.Equal(t, err, legacyErr)
			if tc.name == "files" {
				rule, blocker, err = checkPullBranchProtections(t.Context(), pr, true)
				require.NoError(t, err)
				require.Nil(t, rule)
				require.Empty(t, blocker)
			}
		})
	}
}

func TestEvaluatePullMergeablePreservesNativeSkips(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 2})
	actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repository := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	permission, err := access_model.GetUserRepoPermission(t.Context(), repository, actor)
	require.NoError(t, err)
	pb := &git_model.ProtectedBranch{RepoID: 1, RuleName: "master", RequiredApprovals: 100}
	unittest.AssertSuccessfulInsert(t, pb)

	decision, err := EvaluatePullMergeable(t.Context(), actor, &permission, pr, MergeCheckTypeGeneral, false)
	require.Equal(t, issues_model.MergeBlockedApprovals, decision.Blocker)
	require.True(t, models.IsErrDisallowedToMerge(err))
	require.Equal(t, err, CheckPullMergeable(t.Context(), actor, &permission, pr, MergeCheckTypeGeneral, false))
	for _, mode := range []MergeCheckType{MergeCheckTypeAuto, MergeCheckTypeManually} {
		decision, err = EvaluatePullMergeable(t.Context(), actor, &permission, pr, mode, false)
		require.NoError(t, err)
		require.Empty(t, decision.Blocker)
	}
	decision, err = EvaluatePullMergeable(t.Context(), actor, &permission, pr, MergeCheckTypeGeneral, true)
	require.NoError(t, err, "repository administrator can skip a rule that does not apply to admins")
	require.Empty(t, decision.Blocker)
	pb.ApplyToAdmins = true
	_, err = db.GetEngine(t.Context()).ID(pb.ID).Cols("apply_to_admins").Update(pb)
	require.NoError(t, err)
	decision, err = EvaluatePullMergeable(t.Context(), actor, &permission, pr, MergeCheckTypeGeneral, true)
	require.Equal(t, issues_model.MergeBlockedApprovals, decision.Blocker)
	require.True(t, models.IsErrDisallowedToMerge(err))
	actor.IsAdmin = true
	decision, err = EvaluatePullMergeable(t.Context(), actor, &permission, pr, MergeCheckTypeGeneral, true)
	require.NoError(t, err, "instance administrator retains the native override")
	require.Empty(t, decision.Blocker)
}

func TestEvaluatePullMergeableRetainsSentinelsAndUnknownErrors(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 2})
	actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	repository := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	permission, err := access_model.GetUserRepoPermission(t.Context(), repository, actor)
	require.NoError(t, err)
	pr.HasMerged = true
	decision, err := EvaluatePullMergeable(t.Context(), actor, &permission, pr, MergeCheckTypeGeneral, false)
	require.Equal(t, issues_model.MergeBlockedMerged, decision.Blocker)
	require.Same(t, ErrHasMerged, err)
	pr.HasMerged = false
	decision, err = EvaluatePullMergeable(t.Context(), nil, &permission, pr, MergeCheckTypeGeneral, false)
	require.Equal(t, issues_model.MergeBlockedPermission, decision.Blocker)
	require.Equal(t, ErrUserNotAllowedToMerge, err)
	require.True(t, models.IsErrDisallowedToMerge(err))

	pr.BaseRepoID, pr.BaseRepo = 999999, nil
	decision, err = EvaluatePullMergeable(t.Context(), actor, &permission, pr, MergeCheckTypeGeneral, false)
	require.Error(t, err)
	require.Empty(t, decision.Blocker, "an untyped repository failure cannot become a protection verdict")
}

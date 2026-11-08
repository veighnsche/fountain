// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"context"
	"errors"
	"fmt"

	execcontext "forgejo.org/modules/nativeoperation"

	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/log"
	"forgejo.org/modules/repository"
)

var ErrPullRequestIsUpdateToDate = errors.New("update failed: already up-to-date")

// Update updates pull request with base branch.
func Update(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User, message string, rebase bool) error {
	// The ref writer below owns the head repository and branch, including
	// fork heads, and advances the native revision before Git effects.
	return doUpdate(ctx, pr, doer, message, rebase)
}

func doUpdate(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User, message string, rebase bool) error {
	if pr.Flow == issues_model.PullRequestFlowAGit {
		// TODO: update of agit flow pull request's head branch is unsupported
		return errors.New("update of agit flow pull request's head branch is unsupported")
	}

	diffCount, err := GetDiverging(ctx, pr)
	if err != nil {
		return err
	} else if diffCount.Behind == 0 {
		return fmt.Errorf("HeadBranch of PR %d is up to date: %w", pr.Index, ErrPullRequestIsUpdateToDate)
	}

	if err := pr.LoadHeadRepo(ctx); err != nil {
		return fmt.Errorf("unable to load HeadRepo for PR[%d] during update: %w", pr.ID, err)
	}

	// The current head tip is a read, safe before the claim; the update
	// push below is the fenced effect.
	headRef := git.BranchPrefix + pr.HeadBranch
	oldTip, _ := git.GetFullCommitID(ctx, pr.HeadRepo.RepoPath(), headRef)

	// One ref write owns the reservation before the native update push,
	// advancing the revision so stale observations go stale. Nested
	// calls reuse the enclosing ownership instead of claiming again.
	err = withRefWriteOwnership(ctx,
		execcontext.RefWriteResource(pr.HeadRepoID, refWritePRUpdate, headRef),
		refWriteScope{
			RepositoryID: pr.HeadRepoID,
			Ref:          headRef,
			OldOID:       oldTip,
			PRNumber:     pr.Index,
		},
		func(ctx context.Context) error {
			return updateOwned(ctx, pr, doer, message, rebase)
		})
	// The recheck task always follows the attempt, as before, but only
	// after the ownership releases.
	AddTestPullRequestTask(ctx, doer, pr.BaseRepo.ID, pr.BaseBranch, false, "", "", 0)
	return err
}

// updateOwned runs the head-branch update push under the caller's
// ownership, recording the realized tip for family reconciliation.
func updateOwned(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User, message string, rebase bool) error {
	pullWorkingPool.CheckIn(fmt.Sprint(pr.ID))
	defer pullWorkingPool.CheckOut(fmt.Sprint(pr.ID))

	if rebase {
		if err := updateHeadByRebaseOnToBase(ctx, pr, doer); err != nil {
			return err
		}
		// The push succeeded; a tip-read failure only degrades the
		// recorded scope, never the update itself.
		tip, err := git.GetFullCommitID(ctx, pr.HeadRepo.RepoPath(), git.BranchPrefix+pr.HeadBranch)
		if err != nil {
			log.Error("Unable to read updated head tip for PR[%d]: %v", pr.ID, err)
			return nil
		}
		return recordRefWriteResult(ctx, tip)
	}

	if err := pr.LoadBaseRepo(ctx); err != nil {
		log.Error("unable to load BaseRepo for %-v during update-by-merge: %v", pr, err)
		return fmt.Errorf("unable to load BaseRepo for PR[%d] during update-by-merge: %w", pr.ID, err)
	}
	if err := pr.LoadHeadRepo(ctx); err != nil {
		log.Error("unable to load HeadRepo for PR %-v during update-by-merge: %v", pr, err)
		return fmt.Errorf("unable to load HeadRepo for PR[%d] during update-by-merge: %w", pr.ID, err)
	}
	if pr.HeadRepo == nil {
		// LoadHeadRepo will swallow ErrRepoNotExist so if pr.HeadRepo is still nil recreate the error
		err := repo_model.ErrRepoNotExist{
			ID: pr.HeadRepoID,
		}
		log.Error("unable to load HeadRepo for PR %-v during update-by-merge: %v", pr, err)
		return fmt.Errorf("unable to load HeadRepo for PR[%d] during update-by-merge: %w", pr.ID, err)
	}

	// use merge functions but switch repos and branches
	reversePR := &issues_model.PullRequest{
		ID: pr.ID,

		HeadRepoID: pr.BaseRepoID,
		HeadRepo:   pr.BaseRepo,
		HeadBranch: pr.BaseBranch,

		BaseRepoID: pr.HeadRepoID,
		BaseRepo:   pr.HeadRepo,
		BaseBranch: pr.HeadBranch,
	}

	mergeCommitID, err := doMergeAndPush(ctx, reversePR, doer, repo_model.MergeStyleMerge, "", message, repository.PushTriggerPRUpdateWithBase)
	if err != nil {
		return err
	}
	return recordRefWriteResult(ctx, mergeCommitID)
}

// IsUserAllowedToUpdate check if user is allowed to update PR with given permissions and branch protections
func IsUserAllowedToUpdate(ctx context.Context, pull *issues_model.PullRequest, user *user_model.User, headRepoPerm, baseRepoPerm access_model.Permission) (mergeAllowed, rebaseAllowed bool, err error) {
	if pull.Flow == issues_model.PullRequestFlowAGit {
		return false, false, nil
	}

	if user == nil {
		return false, false, nil
	}

	if err := pull.LoadBaseRepo(ctx); err != nil {
		return false, false, err
	}

	pr := &issues_model.PullRequest{
		HeadRepoID: pull.BaseRepoID,
		HeadRepo:   pull.BaseRepo,
		BaseRepoID: pull.HeadRepoID,
		BaseRepo:   pull.HeadRepo,
		HeadBranch: pull.BaseBranch,
		BaseBranch: pull.HeadBranch,
	}

	pb, err := git_model.GetFirstMatchProtectedBranchRule(ctx, pr.BaseRepoID, pr.BaseBranch)
	if err != nil {
		return false, false, err
	}

	// can't do rebase on protected branch because need force push
	if pb == nil {
		if err := pr.LoadBaseRepo(ctx); err != nil {
			return false, false, err
		}
		prUnit, err := pr.BaseRepo.GetUnit(ctx, unit.TypePullRequests)
		if err != nil {
			if repo_model.IsErrUnitTypeNotExist(err) {
				return false, false, nil
			}
			log.Error("pr.BaseRepo.GetUnit(unit.TypePullRequests): %v", err)
			return false, false, err
		}
		rebaseAllowed = prUnit.PullRequestsConfig().AllowRebaseUpdate
	}

	// Update function need push permission
	if pb != nil {
		pb.Repo = pull.HeadRepo
		if !pb.CanUserPush(ctx, user) {
			return false, false, nil
		}
	}

	mergeAllowed, err = IsUserAllowedToMerge(ctx, pr, headRepoPerm, user)
	if err != nil {
		return false, false, err
	}

	if pull.AllowMaintainerEdit {
		mergeAllowedMaintainer, err := IsUserAllowedToMerge(ctx, pr, baseRepoPerm, user)
		if err != nil {
			return false, false, err
		}

		mergeAllowed = mergeAllowed || mergeAllowedMaintainer
	}

	return mergeAllowed, rebaseAllowed, nil
}

// GetDiverging determines how many commits a PR is ahead or behind the PR base branch
func GetDiverging(ctx context.Context, pr *issues_model.PullRequest) (*git.DivergeObject, error) {
	log.Trace("GetDiverging[%-v]: compare commits", pr)
	prCtx, cancel, err := createTemporaryRepoForPR(ctx, pr)
	if err != nil {
		if !git_model.IsErrBranchNotExist(err) {
			log.Error("CreateTemporaryRepoForPR %-v: %v", pr, err)
		}
		return nil, err
	}
	defer cancel()

	diff, err := git.GetDivergingCommits(ctx, prCtx.tmpBasePath, baseBranch, trackingBranch, nil)
	return &diff, err
}

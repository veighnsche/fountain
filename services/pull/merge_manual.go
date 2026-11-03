// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT
package pull

import (
	"context"
	"errors"
	"fmt"

	"forgejo.org/models"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/log"
	"forgejo.org/modules/timeutil"
	notify_service "forgejo.org/services/notify"
)

// ErrManualMergeCommit preserves the ordinary route's invalid-commit response.
var ErrManualMergeCommit = errors.New("Wrong commit ID")

// ReadManualMergeCommit is the native read-only commit selection rule shared by
// ordinary manual marking and conditional preparation. The selected full object
// must resolve to a commit already reachable from the current target branch.
// It requires neither a source repository nor a live PR head, and writes no ref.
func ReadManualMergeCommit(baseGitRepo *git.Repository, objectFormatName, baseBranch, commitID string) (*git.Commit, error) {
	if baseGitRepo == nil || len(commitID) != git.ObjectFormatFromName(objectFormatName).FullLength() {
		return nil, ErrManualMergeCommit
	}
	commit, err := baseGitRepo.GetCommit(commitID)
	if err != nil {
		if git.IsErrNotExist(err) {
			return nil, ErrManualMergeCommit
		}
		return nil, err
	}
	ok, err := baseGitRepo.IsCommitInBranch(commit.ID.String(), baseBranch)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrManualMergeCommit
	}
	return commit, nil
}

// MergedManually mark pr as merged manually
func MergedManually(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User, baseGitRepo *git.Repository, commitID string) error {
	// One collaboration writer owns the manual merge mark before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale.
	return withCollabOwnership(ctx, CollabPullResource(pr.ID, "manual"), pr.BaseRepoID, func(ctx context.Context) error {
		return doMergedManually(ctx, pr, doer, baseGitRepo, commitID)
	})
}

func doMergedManually(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User, baseGitRepo *git.Repository, commitID string) error {
	pullWorkingPool.CheckIn(fmt.Sprint(pr.ID))
	defer pullWorkingPool.CheckOut(fmt.Sprint(pr.ID))

	marked, err := ApplyManualMergeMetadata(ctx, pr.ID, doer, baseGitRepo, commitID)
	if err != nil {
		return err
	}
	*pr = *marked
	return FinishManualMerge(ctx, pr, doer)
}

// FinishManualMerge shares the ordinary manual writer's notification and
// cross-reference actions with receipt-driven completion. The caller owns its
// single dispatch; no merge push, branch sync or push event belongs here.
func FinishManualMerge(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User) error {
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return err
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return err
	}
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return err
	}
	pr.Issue.Repo = pr.BaseRepo
	notify_service.MergePullRequest(ctx, doer, pr)
	log.Info("manuallyMerged[%d]: Marked as manually merged into %s/%s by commit id: %s", pr.ID, pr.BaseRepo.Name, pr.BaseBranch, pr.MergedCommitID)
	return handleCloseCrossReferences(ctx, pr, doer)
}

// ApplyManualMergeMetadata is the shared native SQL owner, without dispatch or
// ref writes. It reloads/locks the actual PR and uses SetMerged for its exact
// native status, issue closure, merge history and counters in one transaction.
func ApplyManualMergeMetadata(ctx context.Context, pullID int64, doer *user_model.User, baseGitRepo *git.Repository, commitID string) (marked *issues_model.PullRequest, err error) {
	if pullID < 1 || doer == nil || doer.ID < 1 || baseGitRepo == nil {
		return nil, ErrManualMergeCommit
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
			return err
		}
		if _, err := db.GetEngine(ctx).Exec("UPDATE `pull_request` SET `issue_id`=`issue_id` WHERE `id`=?", pullID); err != nil {
			return err
		}
		pr, err := issues_model.GetPullRequestByID(ctx, pullID)
		if err != nil {
			return err
		}
		if pr.HasMerged {
			return ErrHasMerged
		}
		if err := pr.LoadBaseRepo(ctx); err != nil {
			return err
		}
		prUnit, err := pr.BaseRepo.GetUnit(ctx, unit.TypePullRequests)
		if err != nil {
			return err
		}
		prConfig := prUnit.PullRequestsConfig()

		// Check if merge style is correct and allowed
		if !prConfig.IsMergeStyleAllowed(repo_model.MergeStyleManuallyMerged) {
			return models.ErrInvalidMergeStyle{ID: pr.BaseRepo.ID, Style: repo_model.MergeStyleManuallyMerged}
		}

		commit, err := ReadManualMergeCommit(baseGitRepo, pr.BaseRepo.ObjectFormatName, pr.BaseBranch, commitID)
		if err != nil {
			return err
		}
		commitID = commit.ID.String()

		pr.MergedCommitID = commitID
		pr.MergedUnix = timeutil.TimeStamp(commit.Author.When.Unix())
		pr.Status = issues_model.PullRequestStatusManuallyMerged
		pr.Merger = doer
		pr.MergerID = doer.ID

		var merged bool
		if merged, err = pr.SetMerged(ctx); err != nil {
			return err
		} else if !merged {
			return errors.New("SetMerged failed")
		}
		marked = pr
		return nil
	})
	if err != nil {
		return nil, err
	}
	return marked, nil
}

// manuallyMerged checks if a pull request got manually merged
// When a pull request got manually merged mark the pull request as merged
func manuallyMerged(ctx context.Context, pr *issues_model.PullRequest) bool {
	if err := pr.LoadBaseRepo(ctx); err != nil {
		log.Error("%-v LoadBaseRepo: %v", pr, err)
		return false
	}

	if unit, err := pr.BaseRepo.GetUnit(ctx, unit.TypePullRequests); err == nil {
		config := unit.PullRequestsConfig()
		if !config.AutodetectManualMerge {
			return false
		}
	} else {
		log.Error("%-v BaseRepo.GetUnit(unit.TypePullRequests): %v", pr, err)
		return false
	}

	commit, err := getMergeCommit(ctx, pr)
	if err != nil {
		log.Error("%-v getMergeCommit: %v", pr, err)
		return false
	}

	if commit == nil {
		// no merge commit found
		return false
	}

	pr.MergedCommitID = commit.ID.String()
	pr.MergedUnix = timeutil.TimeStamp(commit.Author.When.Unix())
	pr.Status = issues_model.PullRequestStatusManuallyMerged
	merger, _ := user_model.GetUserByEmail(ctx, commit.Author.Email)

	// When the commit author is unknown set the BaseRepo owner as merger
	if merger == nil {
		if pr.BaseRepo.Owner == nil {
			if err = pr.BaseRepo.LoadOwner(ctx); err != nil {
				log.Error("%-v BaseRepo.LoadOwner: %v", pr, err)
				return false
			}
		}
		merger = pr.BaseRepo.Owner
	}
	pr.Merger = merger
	pr.MergerID = merger.ID

	if merged, err := pr.SetMerged(ctx); err != nil {
		log.Error("%-v setMerged : %v", pr, err)
		return false
	} else if !merged {
		return false
	}

	notify_service.MergePullRequest(ctx, merger, pr)

	log.Info("manuallyMerged[%-v]: Marked as manually merged into %s/%s by commit id: %s", pr, pr.BaseRepo.Name, pr.BaseBranch, commit.ID.String())
	return true
}

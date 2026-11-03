// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"fmt"

	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/modules/cache"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/timeutil"
)

var syncBranchLock = cache.MutexMap{}

// SyncBranchesToDB runs the existing native branch-catalog sync under held
// ownership. A fresh catalog is bootstrapped with SyncRepoBranches. Callers own
// authorization and the outer mutation reservation.
func SyncBranchesToDB(ctx context.Context, repoID, pusherID int64, branchNames []string, getBranchCommit func(branchName string) (*git.Commit, error)) error {
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return err
	}
	x := execcontext.FromContext(ctx)
	if x == nil || held.Owner == "" || x.Owner != held.Owner || x.Generation != held.Generation {
		return nativeoperation_model.ErrWrongOwner
	}
	// Implementation notes: hook post-receive will be executing this function in small batches (see `hookBatchSize`),
	// not with all branches that are received at once. Also, concurrent execution will happen, even for the same
	// branches, when pushes are executed quickly; the git repository is the source-of-truth for the current state as
	// all in-memory data may be outdated by concurrent modifications.
	return db.WithTx(ctx, func(ctx context.Context) error {
		branches, err := git_model.GetBranches(ctx, repoID, branchNames, true)
		if err != nil {
			return fmt.Errorf("git_model.GetBranches: %v", err)
		}

		if len(branches) == 0 {
			// if user haven't visit UI but directly push to a branch after upgrading from 1.20 -> 1.21,
			// we cannot simply insert the branch but need to check we have branches or not
			hasBranch, err := db.Exist[git_model.Branch](ctx, git_model.FindBranchOptions{
				RepoID:          repoID,
				IsDeletedBranch: optional.Some(false),
			}.ToConds())
			if err != nil {
				return err
			}
			if !hasBranch {
				if _, err = SyncRepoBranches(ctx, repoID, pusherID); err != nil {
					return fmt.Errorf("repo_module.SyncRepoBranches %d failed: %v", repoID, err)
				}
				return nil
			}
		}

		branchMap := make(map[string]*git_model.Branch, len(branches))
		for _, branch := range branches {
			branchMap[branch.Name] = branch
		}

		for _, branchName := range branchNames {
			err := syncSingleBranch(ctx, repoID, pusherID, branchName, getBranchCommit, branchMap)
			if err != nil {
				return err
			}
		}

		return nil
	})
}

// Sync branchName's state from the git repo to the DB.
func syncSingleBranch(
	ctx context.Context,
	repoID, pusherID int64,
	branchName string,
	getBranchCommit func(branchName string) (*git.Commit, error),
	branchMap map[string]*git_model.Branch,
) error {
	// Multiple invocations to syncSingleBranch can occur concurrently for the same branch if pushes occur in close
	// succession to each other; by locking here, and by reading the git repo's state of the branch (rather than being
	// provided the state from the post-receive hook), we guarantee that the branch table will reach the current state
	// even with concurrent requests (excluding error states).
	defer syncBranchLock.Lock(fmt.Sprintf("%d/%s", repoID, branchName))()

	commit, err := getBranchCommit(branchName)
	if git.IsErrNotExist(err) {
		// Branch has been removed from the repo since this SyncBranchesToDB was invoked; no update is required here
		// because when the branch was deleted then post-receive-hook would have updated the record directly via
		// git_model.AddDeletedBranch.
		return nil
	} else if err != nil {
		return fmt.Errorf("get commit of %d:%s failed: %v", repoID, branchName, err)
	}

	branch, exist := branchMap[branchName]
	if exist && branch.CommitID == commit.ID.String() && !branch.IsDeleted {
		// Already up-to-date.
		return nil
	}

	if exist {
		if rowCount, err := git_model.UpdateBranch(ctx, repoID, pusherID, branchName, commit); err != nil {
			return fmt.Errorf("git_model.UpdateBranch %d:%s failed: %v", repoID, branchName, err)
		} else if rowCount != 1 {
			return fmt.Errorf("git_model.UpdateBranch %d:%s failed: %d records updated", repoID, branchName, rowCount)
		}
		return nil
	}

	if err := db.Insert(ctx, &git_model.Branch{
		RepoID:        repoID,
		Name:          branchName,
		CommitID:      commit.ID.String(),
		CommitMessage: commit.Summary(),
		PusherID:      pusherID,
		CommitTime:    timeutil.TimeStamp(commit.Committer.When.Unix()),
	}); err != nil {
		return fmt.Errorf("inserting new branch %d:%s: %w", repoID, branchName, err)
	}

	return nil
}

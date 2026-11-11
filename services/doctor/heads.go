// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package doctor

import (
	"context"

	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	"forgejo.org/modules/log"
	execcontext "forgejo.org/modules/nativeoperation"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

func synchronizeRepoHeads(ctx context.Context, logger log.Logger, autofix bool) error {
	numRepos := 0
	numHeadsBroken := 0
	numDefaultBranchesBroken := 0
	numReposUpdated := 0
	err := iterateRepositories(ctx, func(repo *repo_model.Repository) error {
		numRepos++
		_, _, defaultBranchErr := git.NewCommand(ctx, "rev-parse").AddDashesAndList(repo.DefaultBranch).RunStdString(&git.RunOpts{Dir: repo.RepoPath()})

		head, _, headErr := git.NewCommand(ctx, "symbolic-ref", "--short", "HEAD").RunStdString(&git.RunOpts{Dir: repo.RepoPath()})

		// what we expect: default branch is valid, and HEAD points to it
		if headErr == nil && defaultBranchErr == nil && head == repo.DefaultBranch {
			return nil
		}

		if headErr != nil {
			numHeadsBroken++
		}
		if defaultBranchErr != nil {
			numDefaultBranchesBroken++
		}

		// if default branch is broken, let the user fix that in the UI
		if defaultBranchErr != nil {
			logger.Warn("Default branch for %s/%s doesn't point to a valid commit", repo.OwnerName, repo.Name)
			return nil
		}

		// if we're not autofixing, that's all we can do
		if !autofix {
			return nil
		}

		// otherwise, let's try fixing HEAD under one default-branch
		// ref-write ownership so no writer interleaves the repair.
		err := nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
			nativeoperation_service.FamilyRefWrite,
			execcontext.RefWriteResource(repo.ID, nativeoperation_service.RefWriteDefaultBranch, git.BranchPrefix+repo.DefaultBranch),
			nativeoperation_service.Scope{
				Family:       nativeoperation_service.FamilyRefWrite,
				RepositoryID: repo.ID,
				Ref:          git.BranchPrefix + repo.DefaultBranch,
			},
			func(ctx context.Context) error {
				return git.NewCommand(ctx, "symbolic-ref").AddDashesAndList("HEAD", git.BranchPrefix+repo.DefaultBranch).Run(&git.RunOpts{Dir: repo.RepoPath()})
			})
		if err != nil {
			logger.Warn("Failed to fix HEAD for %s/%s: %v", repo.OwnerName, repo.Name, err)
			return nil
		}
		numReposUpdated++
		return nil
	})
	if err != nil {
		logger.Critical("Error when fixing repo HEADs: %v", err)
	}

	if autofix {
		logger.Info("Out of %d repos, HEADs for %d are now fixed and HEADS for %d are still broken", numRepos, numReposUpdated, numDefaultBranchesBroken+numHeadsBroken-numReposUpdated)
	} else {
		if numHeadsBroken == 0 && numDefaultBranchesBroken == 0 {
			logger.Info("All %d repos have their HEADs in the correct state", numRepos)
		} else {
			if numHeadsBroken == 0 && numDefaultBranchesBroken != 0 {
				logger.Critical("Default branches are broken for %d/%d repos", numDefaultBranchesBroken, numRepos)
			} else if numHeadsBroken != 0 && numDefaultBranchesBroken == 0 {
				logger.Warn("HEADs are broken for %d/%d repos", numHeadsBroken, numRepos)
			} else {
				logger.Critical("Out of %d repos, HEADS are broken for %d and default branches are broken for %d", numRepos, numHeadsBroken, numDefaultBranchesBroken)
			}
		}
	}

	return err
}

func init() {
	Register(&Check{
		Title:     "Synchronize repo HEADs",
		Name:      "synchronize-repo-heads",
		IsDefault: true,
		Run:       synchronizeRepoHeads,
		Priority:  7,
	})
}

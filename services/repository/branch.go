// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"forgejo.org/models"
	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/log"
	nativeoperation "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/process"
	"forgejo.org/modules/queue"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/util"
	webhook_module "forgejo.org/modules/webhook"
	actions_service "forgejo.org/services/actions"
	nativeoperation_service "forgejo.org/services/nativeoperation"
	notify_service "forgejo.org/services/notify"
	pull_service "forgejo.org/services/pull"
	files_service "forgejo.org/services/repository/files"

	"xorm.io/builder"
)

// CreateNewBranch creates a new repository branch
func CreateNewBranch(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, gitRepo *git.Repository, oldBranchName, branchName string) (err error) {
	branch, err := git_model.GetBranch(ctx, repo.ID, oldBranchName)
	if err != nil {
		return err
	}

	return CreateNewBranchFromCommit(ctx, doer, repo, gitRepo, branch.CommitID, branchName)
}

// Branch contains the branch information
type Branch struct {
	DBBranch          *git_model.Branch
	IsProtected       bool
	IsIncluded        bool
	CommitsAhead      int
	CommitsBehind     int
	LatestPullRequest *issues_model.PullRequest
	MergeMovedOn      bool
}

// LoadBranches loads branches from the repository limited by page & pageSize.
func LoadBranches(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, isDeletedBranch optional.Option[bool], keyword string, page, pageSize int) (*Branch, []*Branch, int64, error) {
	defaultDBBranch, err := git_model.GetBranch(ctx, repo.ID, repo.DefaultBranch)
	if err != nil {
		return nil, nil, 0, err
	}

	branchOpts := git_model.FindBranchOptions{
		RepoID:          repo.ID,
		IsDeletedBranch: isDeletedBranch,
		ListOptions: db.ListOptions{
			Page:     page,
			PageSize: pageSize,
		},
		Keyword:            keyword,
		ExcludeBranchNames: []string{repo.DefaultBranch},
	}

	dbBranches, totalNumOfBranches, err := db.FindAndCount[git_model.Branch](ctx, branchOpts)
	if err != nil {
		return nil, nil, 0, err
	}

	if err := git_model.BranchList(dbBranches).LoadDeletedBy(ctx); err != nil {
		return nil, nil, 0, err
	}
	if err := git_model.BranchList(dbBranches).LoadPusher(ctx); err != nil {
		return nil, nil, 0, err
	}

	rules, err := git_model.FindRepoProtectedBranchRules(ctx, repo.ID)
	if err != nil {
		return nil, nil, 0, err
	}

	repoIDToRepo := map[int64]*repo_model.Repository{}
	repoIDToRepo[repo.ID] = repo

	repoIDToGitRepo := map[int64]*git.Repository{}
	repoIDToGitRepo[repo.ID] = gitRepo

	branches := make([]*Branch, 0, len(dbBranches))
	for i := range dbBranches {
		branch, err := loadOneBranch(ctx, repo, dbBranches[i], &rules, repoIDToRepo, repoIDToGitRepo)
		if err != nil {
			log.Error("loadOneBranch() on repo #%d, branch '%s' failed: %v", repo.ID, dbBranches[i].Name, err)

			// TODO: Ideally, we would only do this if the branch doesn't exist
			// anymore. That is not practical to check here currently, so we do
			// this for all kinds of errors.
			totalNumOfBranches--
			continue
		}

		branches = append(branches, branch)
	}

	// Always add the default branch
	log.Debug("loadOneBranch: load default: '%s'", defaultDBBranch.Name)
	defaultBranch, err := loadOneBranch(ctx, repo, defaultDBBranch, &rules, repoIDToRepo, repoIDToGitRepo)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("loadOneBranch: %v", err)
	}

	return defaultBranch, branches, totalNumOfBranches, nil
}

func loadOneBranch(ctx context.Context, repo *repo_model.Repository, dbBranch *git_model.Branch, protectedBranches *git_model.ProtectedBranchRules,
	repoIDToRepo map[int64]*repo_model.Repository,
	repoIDToGitRepo map[int64]*git.Repository,
) (*Branch, error) {
	log.Trace("loadOneBranch: '%s'", dbBranch.Name)

	branchName := dbBranch.Name
	p := protectedBranches.GetFirstMatched(branchName)
	isProtected := p != nil

	var divergence *git.DivergeObject

	// it's not default branch
	if repo.DefaultBranch != dbBranch.Name && !dbBranch.IsDeleted {
		var err error
		divergence, err = files_service.CountDivergingCommits(ctx, repo, git.BranchPrefix+branchName)
		if err != nil {
			return nil, fmt.Errorf("CountDivergingCommits: %v", err)
		}
	}

	if divergence == nil {
		// tolerate the error that we cannot get divergence
		divergence = &git.DivergeObject{Ahead: -1, Behind: -1}
	}

	pr, err := issues_model.GetLatestPullRequestByHeadInfo(ctx, repo.ID, branchName)
	if err != nil {
		return nil, fmt.Errorf("GetLatestPullRequestByHeadInfo: %v", err)
	}
	headCommit := dbBranch.CommitID

	mergeMovedOn := false
	if pr != nil {
		pr.HeadRepo = repo
		if err := pr.LoadIssue(ctx); err != nil {
			return nil, fmt.Errorf("LoadIssue: %v", err)
		}
		if repo, ok := repoIDToRepo[pr.BaseRepoID]; ok {
			pr.BaseRepo = repo
		} else if err := pr.LoadBaseRepo(ctx); err != nil {
			return nil, fmt.Errorf("LoadBaseRepo: %v", err)
		} else {
			repoIDToRepo[pr.BaseRepoID] = pr.BaseRepo
		}
		pr.Issue.Repo = pr.BaseRepo

		if pr.HasMerged {
			baseGitRepo, ok := repoIDToGitRepo[pr.BaseRepoID]
			if !ok {
				baseGitRepo, err = gitrepo.OpenRepository(ctx, pr.BaseRepo)
				if err != nil {
					return nil, fmt.Errorf("OpenRepository: %v", err)
				}
				defer baseGitRepo.Close()
				repoIDToGitRepo[pr.BaseRepoID] = baseGitRepo
			}
			pullCommit, err := baseGitRepo.GetRefCommitID(pr.GetGitRefName())
			if err != nil && !git.IsErrNotExist(err) {
				return nil, fmt.Errorf("GetBranchCommitID: %v", err)
			}
			if err == nil && headCommit != pullCommit {
				// the head has moved on from the merge - we shouldn't delete
				mergeMovedOn = true
			}
		}
	}

	isIncluded := divergence.Ahead == 0 && repo.DefaultBranch != branchName
	return &Branch{
		DBBranch:          dbBranch,
		IsProtected:       isProtected,
		IsIncluded:        isIncluded,
		CommitsAhead:      divergence.Ahead,
		CommitsBehind:     divergence.Behind,
		LatestPullRequest: pr,
		MergeMovedOn:      mergeMovedOn,
	}, nil
}

// checkBranchName validates branch name with existing repository branches
func checkBranchName(ctx context.Context, repo *repo_model.Repository, name string) error {
	_, err := gitrepo.WalkReferences(ctx, repo, func(_, refName string) error {
		branchRefName := strings.TrimPrefix(refName, git.BranchPrefix)
		switch {
		case branchRefName == name:
			return git_model.ErrBranchAlreadyExists{
				BranchName: name,
			}
		// If branchRefName like a/b but we want to create a branch named a then we have a conflict
		case strings.HasPrefix(branchRefName, name+"/"):
			return git_model.ErrBranchNameConflict{
				BranchName: branchRefName,
			}
			// Conversely if branchRefName like a but we want to create a branch named a/b then we also have a conflict
		case strings.HasPrefix(name, branchRefName+"/"):
			return git_model.ErrBranchNameConflict{
				BranchName: branchRefName,
			}
		case refName == git.TagPrefix+name:
			return models.ErrTagAlreadyExists{
				TagName: name,
			}
		}
		return nil
	})

	return err
}

// SyncBranchesToDB sync the branch information in the database.
// It will check whether the branches of the repository have never been synced before.
// If so, it will sync all branches of the repository.
// Otherwise, it will sync the branches that need to be updated.
func SyncBranchesToDB(ctx context.Context, repoID, pusherID int64, branchNames []string, getBranchCommit func(branchName string) (*git.Commit, error)) error {
	// One ref sync owns the reservation before its database effects,
	// advancing the revision so earlier observations go stale. Nested
	// calls reuse the enclosing ownership instead of claiming again.
	// Tips are read under each branch lock, so no expected commits are
	// recorded from stale hook data. Recovery fences an interrupted
	// standalone sync rather than guessing which branch writes landed.
	return nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
		nativeoperation_service.FamilyRefSync,
		fmt.Sprintf("%d/sync-%d", repoID, len(branchNames)),
		nativeoperation_service.Scope{
			Family:       nativeoperation_service.FamilyRefSync,
			RepositoryID: repoID,
		},
		func(ctx context.Context) error {
			return repo_module.SyncBranchesToDB(ctx, repoID, pusherID, branchNames, getBranchCommit)
		})
}

// CreateNewBranchFromCommit creates a new repository branch
func CreateNewBranchFromCommit(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, gitRepo *git.Repository, commitID, branchName string) (err error) {
	err = repo.MustNotBeArchived()
	if err != nil {
		return err
	}

	// Check if branch name can be used
	if err := checkBranchName(ctx, repo, branchName); err != nil {
		return err
	}

	// This ordinary writer participates in the native-operation
	// reservation: it claims before its Git effect, advancing the shared
	// revision, and refuses while another owner is held. Its Git child
	// carries the execution capability for hook binding.
	return nativeoperation_service.Default().WithOrdinaryOwnership(ctx, "branch", fmt.Sprintf("%d/%s", repo.ID, branchName), nativeoperation_service.Scope{
		RepositoryID: repo.ID,
		Ref:          git.BranchPrefix + branchName,
	}, func(ctx context.Context) error {
		env := repo_module.PushingEnvironment(doer, repo)
		env = nativeoperation.AppendExecEnv(env, nativeoperation.FromContext(ctx))
		if err := git.Push(ctx, repo.RepoPath(), git.PushOptions{
			Remote: repo.RepoPath(),
			Branch: fmt.Sprintf("%s:%s%s", commitID, git.BranchPrefix, branchName),
			Env:    env,
		}); err != nil {
			if git.IsErrPushOutOfDate(err) || git.IsErrPushRejected(err) {
				return err
			}
			return fmt.Errorf("push: %w", err)
		}
		return nil
	})
}

// RenameBranch rename a branch
func RenameBranch(ctx context.Context, repo *repo_model.Repository, doer *user_model.User, gitRepo *git.Repository, from, to string) (string, error) {
	err := repo.MustNotBeArchived()
	if err != nil {
		return "", err
	}

	if from == to {
		return "target_exist", nil
	}

	if gitRepo.IsBranchExist(to) {
		return "target_exist", nil
	}

	if !gitRepo.IsBranchExist(from) {
		return "from_not_exist", nil
	}

	// The current tip is a read, safe before the claim; a rename
	// preserves it across both ref effects.
	tip, err := gitRepo.GetRefCommitID(git.RefNameFromBranch(from).String())
	if err != nil {
		return "", err
	}

	// One ref write owns the reservation before the native rename and
	// all database effects, advancing the revision so stale
	// observations go stale. The rename spans two refs, which the
	// single-ref gate cannot express, so the native child stays
	// unbound and the held reservation alone serializes it. Nested
	// calls reuse the enclosing ownership instead of claiming again.
	objectFormat := git.ObjectFormatFromName(repo.ObjectFormatName)
	err = nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
		nativeoperation_service.FamilyRefWrite,
		nativeoperation_service.RefWriteResource(repo.ID, nativeoperation_service.RefWriteRename, from+"->"+to),
		nativeoperation_service.Scope{
			Family:       nativeoperation_service.FamilyRefWrite,
			RepositoryID: repo.ID,
			Refs: []nativeoperation_service.ScopedRef{
				{Ref: git.BranchPrefix + from, OldOID: tip, NewOID: objectFormat.EmptyObjectID().String()},
				{Ref: git.BranchPrefix + to, NewOID: tip},
			},
		},
		func(ctx context.Context) error {
			return renameBranch(ctx, repo, gitRepo, from, to)
		})
	if err != nil {
		return "", err
	}
	refNameTo := git.RefNameFromBranch(to)
	refID, err := gitRepo.GetRefCommitID(refNameTo.String())
	if err != nil {
		return "", err
	}

	notify_service.DeleteRef(ctx, doer, repo, git.RefNameFromBranch(from))
	notify_service.CreateRef(ctx, doer, repo, refNameTo, refID)

	return "", nil
}

// renameBranch runs the branch-rename database and native effects under
// the caller's ownership.
func renameBranch(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, from, to string) error {
	if err := git_model.RenameBranch(ctx, repo, from, to, func(ctx context.Context, isDefault bool) error {
		err2 := gitRepo.RenameBranch(from, to)
		if err2 != nil {
			return err2
		}

		if isDefault {
			// if default branch changed, we need to delete all schedules and cron jobs
			if err := actions_model.DeleteScheduleTaskByRepo(ctx, repo.ID); err != nil {
				log.Error("DeleteCronTaskByRepo: %v", err)
			}
			// cancel running cron jobs of this repository and delete old schedules
			if err := actions_service.CancelPreviousJobs(
				ctx,
				repo.ID,
				from,
				"",
				webhook_module.HookEventSchedule,
			); err != nil {
				log.Error("CancelPreviousJobs: %v", err)
			}

			err2 = gitrepo.SetDefaultBranch(ctx, repo, to)
			if err2 != nil {
				return err2
			}
		}

		return nil
	}); err != nil {
		return err
	}
	return nil
}

// enmuerates all branch related errors
var (
	ErrBranchIsDefault = errors.New("branch is default")
)

// DeleteBranch delete branch
func DeleteBranch(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, gitRepo *git.Repository, branchName string) error {
	return deleteBranch(ctx, doer, repo, gitRepo, branchName, 0)
}

// wait permits post-merge completion to wait for independent deferred owners.
// Ordinary branch deletion retains its single-attempt admission behavior.
func deleteBranch(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, gitRepo *git.Repository, branchName string, wait time.Duration) error {
	err := repo.MustNotBeArchived()
	if err != nil {
		return err
	}

	if branchName == repo.DefaultBranch {
		return ErrBranchIsDefault
	}

	isProtected, err := git_model.IsBranchProtected(ctx, repo.ID, branchName)
	if err != nil {
		return err
	}
	if isProtected {
		return git_model.ErrBranchIsProtected
	}

	rawBranch, err := git_model.GetBranch(ctx, repo.ID, branchName)
	if err != nil && !git_model.IsErrBranchNotExist(err) {
		return fmt.Errorf("GetBranch: %v", err)
	}

	// database branch record not exist or it's a deleted branch
	notExist := git_model.IsErrBranchNotExist(err) || rawBranch.IsDeleted

	commit, err := gitRepo.GetBranchCommit(branchName)
	if err != nil {
		return err
	}

	// This transaction-plus-direct-Git writer claims the reservation before
	// its database transaction and Git effect, advancing the shared
	// revision, and refuses while another owner is held or offline recovery
	// inhibits the domain. Its direct Git child carries hook repository
	// identity and the execution capability so the reference-transaction
	// checkpoint binds the delete to this owner.
	objectFormat := git.ObjectFormatFromName(repo.ObjectFormatName)
	err = nativeoperation_service.Default().WithOrdinaryOwnershipWait(ctx, nativeoperation_service.FamilyBranchDelete, fmt.Sprintf("%d/%s", repo.ID, branchName), nativeoperation_service.Scope{
		RepositoryID: repo.ID,
		Ref:          git.BranchPrefix + branchName,
		OldOID:       commit.ID.String(),
		NewOID:       objectFormat.EmptyObjectID().String(),
	}, wait, func(ctx context.Context) error {
		if err := db.WithTx(ctx, func(ctx context.Context) error {
			if !notExist {
				if err := git_model.AddDeletedBranch(ctx, repo.ID, branchName, doer.ID); err != nil {
					return err
				}
			}

			env := repo_module.PushingEnvironment(doer, repo)
			env = nativeoperation.AppendExecEnv(env, nativeoperation.FromContext(ctx))
			return gitRepo.DeleteBranch(branchName, git.DeleteBranchOptions{
				Force: true,
				Env:   env,
			})
		}); err != nil {
			return err
		}
		// Test-only crash barrier for offline-recovery proof: with effects
		// committed and the owner still held, the driver SIGKILLs the
		// server here to simulate a crash before release.
		return nativeoperation_service.TestCrashBarrier(nativeoperation_service.CrashPointBranchDeleteAfterEffects)
	})
	if err != nil {
		return err
	}

	// Don't return error below this
	if err := PushUpdate(
		&repo_module.PushUpdateOptions{
			RefFullName:  git.RefNameFromBranch(branchName),
			OldCommitID:  commit.ID.String(),
			NewCommitID:  objectFormat.EmptyObjectID().String(),
			PusherID:     doer.ID,
			PusherName:   doer.Name,
			RepoUserName: repo.OwnerName,
			RepoName:     repo.Name,
		}); err != nil {
		log.Error("Update: %v", err)
	}

	return nil
}

// DeleteBranchAfterMerge deletes the head branch after a PR was merged associated with the head branch.
func DeleteBranchAfterMerge(ctx context.Context, doer *user_model.User, pr *issues_model.PullRequest, headRepo *git.Repository) error {
	// Don't cleanup when there are other PR's that use this branch as head branch.
	exist, err := issues_model.HasUnmergedPullRequestsByHeadInfo(ctx, pr.HeadRepoID, pr.HeadBranch)
	if err != nil {
		return err
	}
	if exist {
		return nil
	}

	// Ensure the doer has write permissions to the head repository of the branch it wants to delete.
	perm, err := access.GetUserRepoPermission(ctx, pr.HeadRepo, doer)
	if err != nil {
		return err
	}
	if !perm.CanWrite(unit.TypeCode) {
		return util.NewPermissionDeniedErrorf("Must have write permission to the head repository")
	}

	if err := pull_service.RetargetChildrenOnMerge(ctx, doer, pr); err != nil {
		return err
	}
	if err := deleteBranch(ctx, doer, pr.HeadRepo, headRepo, pr.HeadBranch, time.Second); err != nil {
		return err
	}

	// Record the completion comment under a fresh collaboration owner; the
	// branch-delete recovery contract stays limited to its own Git effect.
	if err := nativeoperation_service.Default().WithOrdinaryOwnershipWait(ctx,
		nativeoperation_service.FamilyCollaboration,
		nativeoperation_service.CommentCreateResource(pr.Issue.ID),
		nativeoperation_service.Scope{RepositoryID: pr.BaseRepoID}, time.Second,
		func(ctx context.Context) error {
			return issues_model.AddDeletePRBranchComment(ctx, doer, pr.BaseRepo, pr.Issue.ID, pr.HeadBranch)
		}); err != nil {
		// Do not fail here as branch has already been deleted
		log.Error("DeleteBranchAfterMerge: %v", err)
	}

	return nil
}

type BranchSyncOptions struct {
	RepoID int64
}

// branchSyncQueue represents a queue to handle branch sync jobs.
var branchSyncQueue *queue.WorkerPoolQueue[*BranchSyncOptions]

func handlerBranchSync(items ...*BranchSyncOptions) []*BranchSyncOptions {
	var unhandled []*BranchSyncOptions
	for _, opts := range items {
		ctx, _, finished := process.GetManager().AddContext(graceful.GetManager().HammerContext(), fmt.Sprintf("SyncRepoBranches: %d", opts.RepoID))

		// One deferred ref sync claims fresh ownership before its
		// effects; it never inherits another owner's privilege.
		err := nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
			nativeoperation_service.FamilyRefSync,
			fmt.Sprintf("%d/sync", opts.RepoID),
			nativeoperation_service.Scope{
				Family:       nativeoperation_service.FamilyRefSync,
				RepositoryID: opts.RepoID,
			},
			func(ctx context.Context) error {
				_, err := repo_module.SyncRepoBranches(ctx, opts.RepoID, 0)
				return err
			})
		finished()
		if err != nil {
			if nativeoperation_service.IsBusy(err) {
				// Retain busy work: the queue requeues this sync
				// for a later fresh-ownership attempt instead of
				// dropping it.
				unhandled = append(unhandled, opts)
				continue
			}
			log.Error("syncRepoBranches [%d] failed: %v", opts.RepoID, err)
		}
	}
	return unhandled
}

func addRepoToBranchSyncQueue(repoID int64) error {
	return branchSyncQueue.Push(&BranchSyncOptions{
		RepoID: repoID,
	})
}

func initBranchSyncQueue(ctx context.Context) error {
	branchSyncQueue = queue.CreateUniqueQueue(ctx, "branch_sync", handlerBranchSync)
	if branchSyncQueue == nil {
		return errors.New("unable to create branch_sync queue")
	}
	go graceful.GetManager().RunWithCancel(branchSyncQueue)

	return nil
}

func AddAllRepoBranchesToSyncQueue(ctx context.Context) error {
	if err := db.Iterate(ctx, builder.Eq{"is_empty": false}, func(ctx context.Context, repo *repo_model.Repository) error {
		return addRepoToBranchSyncQueue(repo.ID)
	}); err != nil {
		return fmt.Errorf("run sync all branches failed: %v", err)
	}
	return nil
}

func SetRepoDefaultBranch(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, newBranchName string) error {
	if repo.DefaultBranch == newBranchName {
		return nil
	}

	if !gitRepo.IsBranchExist(newBranchName) {
		return git_model.ErrBranchNotExist{
			BranchName: newBranchName,
		}
	}

	oldDefaultBranchName := repo.DefaultBranch
	repo.DefaultBranch = newBranchName

	// One ref write owns the reservation before the native symbolic-ref
	// update and all database effects, advancing the revision so stale
	// observations go stale. Nested calls reuse the enclosing ownership
	// instead of claiming again.
	if err := nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
		nativeoperation_service.FamilyRefWrite,
		nativeoperation_service.RefWriteResource(repo.ID, nativeoperation_service.RefWriteDefaultBranch, newBranchName),
		nativeoperation_service.Scope{
			Family:       nativeoperation_service.FamilyRefWrite,
			RepositoryID: repo.ID,
			Ref:          git.BranchPrefix + newBranchName,
		},
		func(ctx context.Context) error {
			return db.WithTx(ctx, func(ctx context.Context) error {
				if err := repo_model.UpdateDefaultBranch(ctx, repo); err != nil {
					return err
				}

				if err := actions_model.DeleteScheduleTaskByRepo(ctx, repo.ID); err != nil {
					log.Error("DeleteCronTaskByRepo: %v", err)
				}
				// cancel running cron jobs of this repository and delete old schedules
				if err := actions_service.CancelPreviousJobs(
					ctx,
					repo.ID,
					oldDefaultBranchName,
					"",
					webhook_module.HookEventSchedule,
				); err != nil {
					log.Error("CancelPreviousJobs: %v", err)
				}

				return gitrepo.SetDefaultBranch(ctx, repo, newBranchName)
			})
		}); err != nil {
		return err
	}

	notify_service.ChangeDefaultBranch(ctx, repo)

	return nil
}

// Copyright 2015 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// Copyright 2024 The Forgejo Authors c/o Codeberg e.V.. All rights reserved.
// SPDX-License-Identifier: MIT

package wiki

import (
	"context"
	"fmt"
	"os"
	"strings"

	repo_model "forgejo.org/models/repo"
	system_model "forgejo.org/models/system"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/log"
	nativeoperation "forgejo.org/modules/nativeoperation"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/sync"
	asymkey_service "forgejo.org/services/asymkey"
	nativeoperation_service "forgejo.org/services/nativeoperation"
	repo_service "forgejo.org/services/repository"
)

// TODO: use clustered lock (unique queue? or *abuse* cache)
var wikiWorkingPool = sync.NewExclusivePool()

const (
	DefaultRemote = "origin"
)

// InitWiki initializes a wiki for repository,
// it does nothing when repository already has wiki.
func InitWiki(ctx context.Context, repo *repo_model.Repository) error {
	if repo.HasWiki() {
		return nil
	}

	branch := repo.GetWikiBranchName()

	if err := git.InitRepository(ctx, repo.WikiPath(), true, repo.ObjectFormatName); err != nil {
		return fmt.Errorf("InitRepository: %w", err)
	} else if err = repo_module.CreateDelegateHooks(repo.WikiPath()); err != nil {
		return fmt.Errorf("createDelegateHooks: %w", err)
	} else if _, _, err = git.NewCommand(ctx, "symbolic-ref", "HEAD").AddDynamicArguments(git.BranchPrefix + branch).RunStdString(&git.RunOpts{Dir: repo.WikiPath()}); err != nil {
		return fmt.Errorf("unable to set default wiki branch to %s: %w", branch, err)
	}
	return nil
}

// NormalizeWikiBranch renames a repository wiki's branch to `setting.Repository.DefaultBranch`
func NormalizeWikiBranch(ctx context.Context, repo *repo_model.Repository, to string) error {
	from := repo.GetWikiBranchName()

	if err := repo.MustNotBeArchived(); err != nil {
		return err
	}

	if !repo.HasWiki() {
		// No wiki refs exist; the branch-name record is a plain
		// settings write under its own ownership.
		return nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
			nativeoperation_service.FamilyRepoSettings,
			fmt.Sprintf("%d/wiki-branch", repo.ID),
			nativeoperation_service.Scope{
				Family:       nativeoperation_service.FamilyRepoSettings,
				RepositoryID: repo.ID,
			},
			func(ctx context.Context) error {
				return normalizeWikiBranchDB(ctx, repo, to)
			})
	}

	if from == to {
		return nil
	}

	gitRepo, err := git.OpenRepository(ctx, repo.WikiPath())
	if err != nil {
		return err
	}
	defer gitRepo.Close()

	if gitRepo.IsBranchExist(to) {
		return nil
	}

	if !gitRepo.IsBranchExist(from) {
		return nil
	}

	// The current tip is a read, safe before the claim; a rename
	// preserves it across both ref effects.
	tip, err := gitRepo.GetRefCommitID(git.BranchPrefix + from)
	if err != nil {
		return err
	}

	// One ref write owns the reservation before the native wiki
	// rename, the default-branch update and the database write. The
	// rename spans two refs, which the single-ref gate cannot
	// express, so the native child stays unbound and the held
	// reservation alone serializes it. Nested calls reuse the
	// enclosing ownership instead of claiming again.
	objectFormat := git.ObjectFormatFromName(repo.ObjectFormatName)
	return nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
		nativeoperation_service.FamilyRefWrite,
		nativeoperation.RefWriteResource(repo.ID, nativeoperation_service.RefWriteRename, "wiki/"+from+"->"+to),
		nativeoperation_service.Scope{
			Family:       nativeoperation_service.FamilyRefWrite,
			RepositoryID: repo.ID,
			Refs: []nativeoperation_service.ScopedRef{
				{Ref: git.BranchPrefix + from, OldOID: tip, NewOID: objectFormat.EmptyObjectID().String()},
				{Ref: git.BranchPrefix + to, NewOID: tip},
			},
		},
		func(ctx context.Context) error {
			return normalizeWikiBranchOwned(ctx, repo, gitRepo, from, to)
		})
}

// normalizeWikiBranchOwned runs the wiki branch rename, default-branch
// update and database write under the caller's ownership.
func normalizeWikiBranchOwned(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, from, to string) error {
	if err := gitRepo.RenameBranch(from, to); err != nil {
		return err
	}

	if err := gitrepo.SetDefaultBranch(ctx, repo, to); err != nil {
		return err
	}

	return normalizeWikiBranchDB(ctx, repo, to)
}

// normalizeWikiBranchDB records the wiki branch name in the database.
func normalizeWikiBranchDB(ctx context.Context, repo *repo_model.Repository, to string) error {
	repo.WikiBranch = to
	return repo_model.UpdateRepositoryCols(ctx, repo, "wiki_branch")
}

// prepareGitPath try to find a suitable file path with file name by the given raw wiki name.
// return: existence, prepared file path with name, error
func prepareGitPath(gitRepo *git.Repository, branch string, wikiPath WebPath) (bool, string, error) {
	unescaped := string(wikiPath) + ".md"
	gitPath := WebPathToGitPath(wikiPath)

	// Look for both files
	filesInIndex, err := gitRepo.LsTree(branch, unescaped, gitPath)
	if err != nil {
		if strings.Contains(err.Error(), "Not a valid object name "+branch) {
			return false, gitPath, nil
		}
		log.Error("%v", err)
		return false, gitPath, err
	}

	foundEscaped := false
	for _, filename := range filesInIndex {
		switch filename {
		case unescaped:
			// if we find the unescaped file return it
			return true, unescaped, nil
		case gitPath:
			foundEscaped = true
		}
	}

	// If not return whether the escaped file exists, and the escaped filename to keep backwards compatibility.
	return foundEscaped, gitPath, nil
}

// updateWikiPage adds a new page or edits an existing page in repository wiki.
func updateWikiPage(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, oldWikiName, newWikiName WebPath, content, message string, isNew bool) (err error) {
	err = repo.MustNotBeArchived()
	if err != nil {
		return err
	}

	if err = validateWebPath(newWikiName); err != nil {
		return err
	}

	// The current wiki tip is a read, safe before the claim; the page
	// commit and push below are the fenced effects.
	wikiRef := git.BranchPrefix + repo.GetWikiBranchName()
	oldTip, _ := git.GetFullCommitID(ctx, repo.WikiPath(), wikiRef)

	// One file ref write owns the reservation before the native wiki
	// push, advancing the revision so stale observations go stale.
	// Nested calls reuse the enclosing ownership instead of claiming
	// again.
	return nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
		nativeoperation_service.FamilyRefWrite,
		nativeoperation.RefWriteResource(repo.ID, nativeoperation_service.RefWriteFile, "wiki/"+wikiRef),
		nativeoperation_service.Scope{
			Family:       nativeoperation_service.FamilyRefWrite,
			RepositoryID: repo.ID,
			Ref:          wikiRef,
			OldOID:       oldTip,
		},
		func(ctx context.Context) error {
			return updateWikiPageOwned(ctx, doer, repo, oldWikiName, newWikiName, content, message, isNew)
		})
}

// updateWikiPageOwned runs the wiki page commit and push under the
// caller's ownership, recording the realized tip for family
// reconciliation.
func updateWikiPageOwned(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, oldWikiName, newWikiName WebPath, content, message string, isNew bool) (err error) {
	wikiWorkingPool.CheckIn(fmt.Sprint(repo.ID))
	defer wikiWorkingPool.CheckOut(fmt.Sprint(repo.ID))

	if err = InitWiki(ctx, repo); err != nil {
		return fmt.Errorf("InitWiki: %w", err)
	}

	hasMasterBranch := git.IsBranchExist(ctx, repo.WikiPath(), repo.GetWikiBranchName())

	basePath, err := repo_module.CreateTemporaryPath("update-wiki")
	if err != nil {
		return err
	}
	defer func() {
		if err := repo_module.RemoveTemporaryPath(basePath); err != nil {
			log.Error("Merge: RemoveTemporaryPath: %s", err)
		}
	}()

	cloneOpts := git.CloneRepoOptions{
		Bare:   true,
		Shared: true,
	}

	if hasMasterBranch {
		cloneOpts.Branch = repo.GetWikiBranchName()
	}

	if err := git.Clone(ctx, repo.WikiPath(), basePath, cloneOpts); err != nil {
		log.Error("Failed to clone repository: %s (%v)", repo.FullName(), err)
		return fmt.Errorf("failed to clone repository: %s (%w)", repo.FullName(), err)
	}

	gitRepo, err := git.OpenRepository(ctx, basePath)
	if err != nil {
		log.Error("Unable to open temporary repository: %s (%v)", basePath, err)
		return fmt.Errorf("failed to open new temporary repository in: %s %w", basePath, err)
	}
	defer gitRepo.Close()

	if hasMasterBranch {
		if err := gitRepo.ReadTreeToIndex("HEAD"); err != nil {
			log.Error("Unable to read HEAD tree to index in: %s %v", basePath, err)
			return fmt.Errorf("fnable to read HEAD tree to index in: %s %w", basePath, err)
		}
	}

	isWikiExist, newWikiPath, err := prepareGitPath(gitRepo, repo.GetWikiBranchName(), newWikiName)
	if err != nil {
		return err
	}

	if isNew {
		if isWikiExist {
			return repo_model.ErrWikiAlreadyExist{
				Title: newWikiPath,
			}
		}
	} else {
		// avoid check existence again if wiki name is not changed since gitRepo.LsFiles(...) is not free.
		isOldWikiExist := true
		oldWikiPath := newWikiPath
		if oldWikiName != newWikiName {
			isOldWikiExist, oldWikiPath, err = prepareGitPath(gitRepo, repo.GetWikiBranchName(), oldWikiName)
			if err != nil {
				return err
			}
		}

		if isOldWikiExist {
			err := gitRepo.RemoveFilesFromIndex(oldWikiPath)
			if err != nil {
				log.Error("RemoveFilesFromIndex failed: %v", err)
				return err
			}
		}
	}

	// FIXME: The wiki doesn't have lfs support at present - if this changes need to check attributes here

	objectHash, err := gitRepo.HashObject(strings.NewReader(content))
	if err != nil {
		log.Error("HashObject failed: %v", err)
		return err
	}

	if err := gitRepo.AddObjectToIndex("100644", objectHash, newWikiPath); err != nil {
		log.Error("AddObjectToIndex failed: %v", err)
		return err
	}

	tree, err := gitRepo.WriteTree()
	if err != nil {
		log.Error("WriteTree failed: %v", err)
		return err
	}

	commitTreeOpts := git.CommitTreeOpts{
		Message: message,
	}

	committer := doer.NewGitSig()

	sign, signingKey, signer, _ := asymkey_service.SignWikiCommit(ctx, repo, doer)
	if sign {
		commitTreeOpts.KeyID = signingKey
		if repo.GetTrustModel() == repo_model.CommitterTrustModel || repo.GetTrustModel() == repo_model.CollaboratorCommitterTrustModel {
			committer = signer
		}
	} else {
		commitTreeOpts.NoGPGSign = true
	}
	if hasMasterBranch {
		commitTreeOpts.Parents = []string{"HEAD"}
	}

	commitHash, err := gitRepo.CommitTree(doer.NewGitSig(), committer, tree, commitTreeOpts)
	if err != nil {
		log.Error("CommitTree failed: %v", err)
		return err
	}

	if err := pushWikiBranch(ctx, doer, repo, basePath, commitHash.String()); err != nil {
		log.Error("Push failed: %v", err)
		if git.IsErrPushOutOfDate(err) || git.IsErrPushRejected(err) {
			return err
		}
		return fmt.Errorf("failed to push: %w", err)
	}

	return nativeoperation_service.RecordScopeRefResult(ctx, commitHash.String())
}

// pushWikiBranch pushes one wiki commit to the wiki branch, carrying the
// caller's execution capability for hook binding.
func pushWikiBranch(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, basePath, commitHash string) error {
	env := repo_module.FullPushingEnvironment(
		doer,
		doer,
		repo,
		repo.Name+".wiki",
		0,
	)
	env = nativeoperation.AppendExecEnv(env, nativeoperation.FromContext(ctx))
	return git.Push(ctx, basePath, git.PushOptions{
		Remote: DefaultRemote,
		Branch: fmt.Sprintf("%s:%s%s", commitHash, git.BranchPrefix, repo.GetWikiBranchName()),
		Env:    env,
	})
}

// AddWikiPage adds a new wiki page with a given wikiPath.
func AddWikiPage(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, wikiName WebPath, content, message string) error {
	return updateWikiPage(ctx, doer, repo, "", wikiName, content, message, true)
}

// EditWikiPage updates a wiki page identified by its wikiPath,
// optionally also changing wikiPath.
func EditWikiPage(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, oldWikiName, newWikiName WebPath, content, message string) error {
	return updateWikiPage(ctx, doer, repo, oldWikiName, newWikiName, content, message, false)
}

// DeleteWikiPage deletes a wiki page identified by its path.
func DeleteWikiPage(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, wikiName WebPath) (err error) {
	err = repo.MustNotBeArchived()
	if err != nil {
		return err
	}

	// The current wiki tip is a read, safe before the claim; the page
	// deletion commit and push below are the fenced effects.
	wikiRef := git.BranchPrefix + repo.GetWikiBranchName()
	oldTip, _ := git.GetFullCommitID(ctx, repo.WikiPath(), wikiRef)

	// One file ref write owns the reservation before the native wiki
	// push, advancing the revision so stale observations go stale.
	// Nested calls reuse the enclosing ownership instead of claiming
	// again.
	return nativeoperation_service.Default().WithOrdinaryOwnership(ctx,
		nativeoperation_service.FamilyRefWrite,
		nativeoperation.RefWriteResource(repo.ID, nativeoperation_service.RefWriteFile, "wiki/"+wikiRef),
		nativeoperation_service.Scope{
			Family:       nativeoperation_service.FamilyRefWrite,
			RepositoryID: repo.ID,
			Ref:          wikiRef,
			OldOID:       oldTip,
		},
		func(ctx context.Context) error {
			return deleteWikiPageOwned(ctx, doer, repo, wikiName)
		})
}

// deleteWikiPageOwned runs the wiki page deletion commit and push under
// the caller's ownership, recording the realized tip for family
// reconciliation.
func deleteWikiPageOwned(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, wikiName WebPath) (err error) {
	wikiWorkingPool.CheckIn(fmt.Sprint(repo.ID))
	defer wikiWorkingPool.CheckOut(fmt.Sprint(repo.ID))

	if err = InitWiki(ctx, repo); err != nil {
		return fmt.Errorf("InitWiki: %w", err)
	}

	basePath, err := repo_module.CreateTemporaryPath("update-wiki")
	if err != nil {
		return err
	}
	defer func() {
		if err := repo_module.RemoveTemporaryPath(basePath); err != nil {
			log.Error("Merge: RemoveTemporaryPath: %s", err)
		}
	}()

	if err := git.Clone(ctx, repo.WikiPath(), basePath, git.CloneRepoOptions{
		Bare:   true,
		Shared: true,
		Branch: repo.GetWikiBranchName(),
	}); err != nil {
		log.Error("Failed to clone repository: %s (%v)", repo.FullName(), err)
		return fmt.Errorf("failed to clone repository: %s (%w)", repo.FullName(), err)
	}

	gitRepo, err := git.OpenRepository(ctx, basePath)
	if err != nil {
		log.Error("Unable to open temporary repository: %s (%v)", basePath, err)
		return fmt.Errorf("failed to open new temporary repository in: %s %w", basePath, err)
	}
	defer gitRepo.Close()

	if err := gitRepo.ReadTreeToIndex("HEAD"); err != nil {
		log.Error("Unable to read HEAD tree to index in: %s %v", basePath, err)
		return fmt.Errorf("unable to read HEAD tree to index in: %s %w", basePath, err)
	}

	found, wikiPath, err := prepareGitPath(gitRepo, repo.GetWikiBranchName(), wikiName)
	if err != nil {
		return err
	}
	if found {
		err := gitRepo.RemoveFilesFromIndex(wikiPath)
		if err != nil {
			return err
		}
	} else {
		return os.ErrNotExist
	}

	// FIXME: The wiki doesn't have lfs support at present - if this changes need to check attributes here

	tree, err := gitRepo.WriteTree()
	if err != nil {
		return err
	}
	message := fmt.Sprintf("Delete page %q", wikiName)
	commitTreeOpts := git.CommitTreeOpts{
		Message: message,
		Parents: []string{"HEAD"},
	}

	committer := doer.NewGitSig()

	sign, signingKey, signer, _ := asymkey_service.SignWikiCommit(ctx, repo, doer)
	if sign {
		commitTreeOpts.KeyID = signingKey
		if repo.GetTrustModel() == repo_model.CommitterTrustModel || repo.GetTrustModel() == repo_model.CollaboratorCommitterTrustModel {
			committer = signer
		}
	} else {
		commitTreeOpts.NoGPGSign = true
	}

	commitHash, err := gitRepo.CommitTree(doer.NewGitSig(), committer, tree, commitTreeOpts)
	if err != nil {
		return err
	}

	if err := pushWikiBranch(ctx, doer, repo, basePath, commitHash.String()); err != nil {
		if git.IsErrPushOutOfDate(err) || git.IsErrPushRejected(err) {
			return err
		}
		return fmt.Errorf("Push: %w", err)
	}

	return nativeoperation_service.RecordScopeRefResult(ctx, commitHash.String())
}

// DeleteWiki removes the actual and local copy of repository wiki.
func DeleteWiki(ctx context.Context, repo *repo_model.Repository) error {
	if err := repo_service.UpdateRepositoryUnits(ctx, repo, nil, []unit.Type{unit.TypeWiki}); err != nil {
		return err
	}

	system_model.RemoveAllWithNotice(ctx, "Delete repository wiki", repo.WikiPath())
	return nil
}

type SearchContentsResult struct {
	*git.GrepResult
	Title string
}

func SearchWikiContents(ctx context.Context, repo *repo_model.Repository, keyword string) ([]SearchContentsResult, error) {
	gitRepo, err := git.OpenRepository(ctx, repo.WikiPath())
	if err != nil {
		return nil, err
	}
	defer gitRepo.Close()

	grepRes, err := git.GrepSearch(ctx, gitRepo, keyword, git.GrepOptions{
		ContextLineNumber: 0,
		Mode:              git.FixedAnyGrepMode,
		RefName:           repo.GetWikiBranchName(),
		MaxResultLimit:    10,
		MatchesPerFile:    3,
	})
	if err != nil {
		return nil, err
	}

	res := make([]SearchContentsResult, 0, len(grepRes))
	for _, entry := range grepRes {
		wp, err := GitPathToWebPath(entry.Filename)
		if err != nil {
			return nil, err
		}
		_, title := WebPathToUserTitle(wp)

		res = append(res, SearchContentsResult{
			GrepResult: entry,
			Title:      title,
		})
	}

	return res, nil
}

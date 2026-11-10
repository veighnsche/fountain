// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	repo_module "forgejo.org/modules/repository"
)

// WithRefRename persists launch authority before the enclosing metadata
// transaction begins. Native branch -m remains responsible for config, reflogs
// and linked worktrees; its complete result is proved before clearing the stage.
func (s *Service) WithRefRename(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, target execcontext.RepositoryTarget, from, to, tip string, fn func(run func() error) error) error {
	execution := execcontext.FromContext(ctx)
	if execution == nil {
		return errors.New("native branch rename requires ownership")
	}
	reservation, document, err := scopeDocumentForOwner(ctx, execution.Owner)
	if err != nil {
		return err
	}
	secret, err := execcontext.ReadCapabilityFile(execution.CapabilityPath)
	if err != nil || !execcontext.VerifyProof(reservation.Verifier, secret) {
		return nativeoperation_model.ErrWrongOwner
	}
	if err := repo.LoadOwner(ctx); err != nil {
		return err
	}
	plan := execcontext.RefRenamePlan{RepositoryID: repo.ID, OwnerName: repo.OwnerName, RepositoryName: repo.Name, Target: target,
		FromRef: git.BranchPrefix + from, ToRef: git.BranchPrefix + to, TipOID: tip}
	if err := plan.Validate(); err != nil {
		return err
	}
	expectedPath := repo.RepoPath()
	if target == execcontext.TargetWiki {
		expectedPath = repo.WikiPath()
	}
	if filepath.Clean(gitRepo.Path) != filepath.Clean(expectedPath) || !scopeAllowsRename(reservation.Owner, document.Scope(), plan) {
		return nativeoperation_model.ErrWrongOwner
	}
	actual, absent, err := renameTip(ctx, gitRepo.Path, plan.FromRef)
	if err != nil {
		return err
	}
	if absent || actual != tip {
		return errors.New("native branch rename source moved")
	}
	_, absent, err = renameTip(ctx, gitRepo.Path, plan.ToRef)
	if err != nil {
		return err
	}
	if !absent {
		return errors.New("native branch rename target exists")
	}
	heads, err := renameHeads(ctx, gitRepo.Path)
	if err != nil {
		return err
	}
	encoded, err := document.WithNativeRename(&plan)
	if err != nil {
		return err
	}
	if err := nativeoperation_model.UpdateScopeExact(ctx, reservation.Owner, reservation.Generation, document.Original(), encoded); err != nil {
		return err
	}
	env := repo_module.RefWriteEnvironmentForTarget(repo, target)
	env = execcontext.AppendGitOriginEnv(env, execcontext.GitOriginRefRename)
	env = execcontext.AppendExecEnv(env, execution)
	entered := false
	var launchErr error
	effectErr := fn(func() error {
		if entered {
			launchErr = errors.New("native branch rename may launch only once")
			return launchErr
		}
		entered = true
		launchErr = gitRepo.RenameBranchWithEnv(from, to, env)
		return launchErr
	})
	if entered && (launchErr != nil || effectErr != nil) {
		return fmt.Errorf("%w: native branch rename did not complete: %w", execcontext.ErrIndeterminateEffect, errors.Join(launchErr, effectErr))
	}
	if entered {
		if err := verifyRenameResult(ctx, gitRepo.Path, plan, heads); err != nil {
			return fmt.Errorf("%w: %w", execcontext.ErrIndeterminateEffect, err)
		}
	} else if effectErr == nil {
		return fmt.Errorf("%w: native branch rename metadata callback completed without launching Git", execcontext.ErrIndeterminateEffect)
	}
	current, currentDocument, err := scopeDocumentForOwner(context.WithoutCancel(ctx), execution.Owner)
	if err != nil {
		return fmt.Errorf("%w: rename stage could not be retired: %w", execcontext.ErrIndeterminateEffect, err)
	}
	if currentDocument.Scope().NativeRename == nil || *currentDocument.Scope().NativeRename != plan {
		return fmt.Errorf("%w: rename stage changed", execcontext.ErrIndeterminateEffect)
	}
	cleared, err := currentDocument.WithNativeRename(nil)
	if err != nil {
		return fmt.Errorf("%w: %w", execcontext.ErrIndeterminateEffect, err)
	}
	if err := nativeoperation_model.UpdateScopeExact(context.WithoutCancel(ctx), execution.Owner, current.Generation, currentDocument.Original(), cleared); err != nil {
		return fmt.Errorf("%w: rename stage could not be retired: %w", execcontext.ErrIndeterminateEffect, err)
	}
	return effectErr
}

func scopeAllowsRename(owner string, scope Scope, plan execcontext.RefRenamePlan) bool {
	if scope.NativeRename != nil || scope.RepositoryID != plan.RepositoryID {
		return false
	}
	family, resource, ok := execcontext.SplitOrdinaryResource(owner)
	if !ok || scope.Family != family {
		return false
	}
	if family == FamilyRepoSettings {
		return plan.Target == execcontext.TargetWiki && resource == strconv.FormatInt(plan.RepositoryID, 10)+"/branches"
	}
	if family != FamilyRefWrite {
		return false
	}
	parts := strings.SplitN(resource, "/", 3)
	if len(parts) != 3 || parts[1] != RefWriteRename || len(scope.Refs) != 2 {
		return false
	}
	target, err := execcontext.RefWriteRepositoryTarget(scope.RepositoryTarget, resource)
	if err != nil || target != plan.Target {
		return false
	}
	zero := strings.Repeat("0", len(plan.TipOID))
	from, to := scope.Refs[0], scope.Refs[1]
	return from.Ref == plan.FromRef && from.OldOID == plan.TipOID && from.NewOID == zero &&
		to.Ref == plan.ToRef && (to.OldOID == "" || to.OldOID == zero) && to.NewOID == plan.TipOID
}

func renameTip(ctx context.Context, path, ref string) (string, bool, error) {
	out, _, err := git.NewCommand(ctx, "rev-parse", "--verify", "--quiet").AddDynamicArguments(ref).RunStdString(&git.RunOpts{Dir: path})
	if git.IsErrorExitCode(err, 1) {
		return "", true, nil
	}
	return strings.TrimSpace(out), false, err
}

func renameHeads(ctx context.Context, path string) (map[string]string, error) {
	command := git.NewCommand(ctx, "worktree", "list", "--porcelain")
	delimiter := "\x00"
	if git.CheckGitVersionAtLeast("2.36.0") == nil {
		command.AddArguments("-z")
	} else {
		delimiter = "\n"
	}
	out, _, err := command.RunStdString(&git.RunOpts{Dir: path})
	if err != nil {
		return nil, err
	}
	heads := make(map[string]string)
	for _, field := range strings.Split(out, delimiter) {
		worktree, ok := strings.CutPrefix(field, "worktree ")
		if !ok {
			if delimiter == "\n" && field != "" && field != "bare" && field != "detached" &&
				!strings.HasPrefix(field, "HEAD ") && !strings.HasPrefix(field, "branch ") &&
				field != "locked" && !strings.HasPrefix(field, "locked ") && field != "prunable" && !strings.HasPrefix(field, "prunable ") {
				return nil, errors.New("native branch rename worktree path is ambiguous")
			}
			continue
		}
		if _, duplicate := heads[worktree]; duplicate {
			return nil, errors.New("native branch rename worktree identity is ambiguous")
		}
		head, _, err := git.NewCommand(ctx, "symbolic-ref", "--quiet", "HEAD").RunStdString(&git.RunOpts{Dir: worktree})
		if git.IsErrorExitCode(err, 1) {
			head = ""
		} else if err != nil {
			return nil, err
		}
		heads[worktree] = strings.TrimSpace(head)
	}
	if len(heads) == 0 {
		return nil, errors.New("native branch rename found no worktree identity")
	}
	return heads, nil
}

func verifyRenameResult(ctx context.Context, path string, plan execcontext.RefRenamePlan, heads map[string]string) error {
	_, fromAbsent, err := renameTip(ctx, path, plan.FromRef)
	if err != nil {
		return err
	}
	to, toAbsent, err := renameTip(ctx, path, plan.ToRef)
	if err != nil {
		return err
	}
	if !fromAbsent || toAbsent || to != plan.TipOID {
		return errors.New("native branch rename refs did not reach the recorded result")
	}
	after, err := renameHeads(ctx, path)
	if err != nil {
		return err
	}
	if len(after) != len(heads) {
		return errors.New("native branch rename worktree identities changed")
	}
	for worktree, head := range heads {
		if head == plan.FromRef {
			head = plan.ToRef
		}
		if got, ok := after[worktree]; !ok || got != head {
			return errors.New("native branch rename symbolic HEAD result is unverified")
		}
	}
	return nil
}

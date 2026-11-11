// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Exercise the actual private hook envelope.
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	git_model "forgejo.org/models/git"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/util"
	nativeoperation_service "forgejo.org/services/nativeoperation"
	repo_service "forgejo.org/services/repository"
	wiki_service "forgejo.org/services/wiki"

	"github.com/stretchr/testify/require"
)

func init() {
	if os.Getenv("FOUNTAIN_RENAME_TEST_HOOK") != "1" {
		return
	}
	if err := runRenameTestHook(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runRenameTestHook() error {
	if len(os.Args) != 2 {
		return errors.New("missing rename hook phase")
	}
	phase := os.Args[1]
	if phase == "preparing" {
		_, err := io.Copy(io.Discard, os.Stdin)
		return err
	}
	origin, err := execcontext.GitOrigin(os.Getenv(execcontext.EnvGitOrigin), os.Getenv("SSH_ORIGINAL_COMMAND"))
	if err != nil {
		return err
	}
	proof, err := execcontext.ReadCapabilityFile(os.Getenv(execcontext.EnvExecFile))
	if err != nil {
		return errors.New("rename hook is missing its execution proof")
	}
	repoName := os.Getenv(repo_module.EnvRepoName)
	if os.Getenv(repo_module.EnvRepoIsWiki) == "true" && !strings.HasSuffix(repoName, ".wiki") {
		repoName += ".wiki"
	}
	request := nativeoperation_service.TransactionRequest{
		OwnerName: os.Getenv(repo_module.EnvRepoUsername), RepoName: repoName,
		Origin: origin, Phase: phase, Proof: proof,
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			return errors.New("invalid rename hook ref tuple")
		}
		request.Lines = append(request.Lines, nativeoperation_service.RefLine{Old: fields[0], New: fields[1], Ref: fields[2]})
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Post(os.Getenv("FOUNTAIN_RENAME_TEST_HOOK_URL"), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("rename hook admission returned HTTP %d", response.StatusCode)
	}
	return nil
}

type renameHookEvidence struct {
	mu       sync.Mutex
	requests []nativeoperation_service.TransactionRequest
	errors   []error
}

func installRenameTestHook(t *testing.T, repoPath string) *renameHookEvidence {
	t.Helper()
	evidence := new(renameHookEvidence)
	service := nativeoperation_service.NewService()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request nativeoperation_service.TransactionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid hook envelope", http.StatusBadRequest)
			return
		}
		decision, err := service.ClassifyTransaction(r.Context(), request)
		if err == nil && !decision.Allowed {
			err = errors.New("valid native rename hook was refused")
		}
		if err == nil {
			otherTarget := request
			if withoutWiki, ok := strings.CutSuffix(otherTarget.RepoName, ".wiki"); ok {
				otherTarget.RepoName = withoutWiki
			} else {
				otherTarget.RepoName += ".wiki"
			}
			other, otherErr := service.ClassifyTransaction(r.Context(), otherTarget)
			if otherErr != nil || other.Allowed {
				err = errors.New("native rename proof crossed the code/wiki target boundary")
			}
		}
		evidence.mu.Lock()
		request.Proof = ""
		evidence.requests = append(evidence.requests, request)
		if err != nil {
			evidence.errors = append(evidence.errors, err)
		}
		evidence.mu.Unlock()
		if err != nil {
			http.Error(w, "rename admission failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	executable, err := os.Executable()
	require.NoError(t, err)
	script := "#!/bin/sh\nFOUNTAIN_RENAME_TEST_HOOK=1 FOUNTAIN_RENAME_TEST_HOOK_URL=" + util.ShellEscape(server.URL) + " exec " + util.ShellEscape(executable) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "hooks", "reference-transaction"), []byte(script), 0o700))
	return evidence
}

func (e *renameHookEvidence) assertAdmitted(t *testing.T, target execcontext.RepositoryTarget) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	require.Empty(t, e.errors)
	require.NotEmpty(t, e.requests, "native Git must reach the real HTTP admission boundary")
	prepared := false
	for _, request := range e.requests {
		require.Equal(t, execcontext.GitOriginRefRename, request.Origin)
		require.Equal(t, target == execcontext.TargetWiki, strings.HasSuffix(request.RepoName, ".wiki"))
		prepared = prepared || request.Phase == nativeoperation_service.PhasePrepared
	}
	require.True(t, prepared, "a prepared ref transaction must be admitted")
}

func renameGit(ctx context.Context, t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, git.GitExecutable, args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Rename Fixture", "GIT_AUTHOR_EMAIL=rename@example.invalid", "GIT_COMMITTER_NAME=Rename Fixture", "GIT_COMMITTER_EMAIL=rename@example.invalid")
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "Git %v: %s", args, output)
	return strings.TrimSpace(string(output))
}

func prepareRenameFixture(t *testing.T) (context.Context, *repo_model.Repository) {
	t.Helper()
	unittest.PrepareTestEnv(t)
	t.Cleanup(test.MockVariableValue(&setting.RepoRootPath, t.TempDir()))
	t.Cleanup(test.MockVariableValue(&setting.AppDataPath, t.TempDir()))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	repository := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})
	require.NoError(t, repository.LoadOwner(ctx))
	return ctx, repository
}

func createRenameGitRepository(ctx context.Context, t *testing.T, path, branch string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	renameGit(ctx, t, filepath.Dir(path), "", "init", "--bare", "--initial-branch="+branch, path)
	renameGit(ctx, t, path, "", "config", "core.logAllRefUpdates", "true")
	tree := renameGit(ctx, t, path, "", "mktree")
	tip := renameGit(ctx, t, path, "fixture\n", "commit-tree", tree)
	renameGit(ctx, t, path, "", "update-ref", "refs/heads/"+branch, tip)
	renameGit(ctx, t, path, "", "config", "branch."+branch+".description", "native-rename-config")
	return tip
}

func TestRefRenamePreservesNativeGitAndSQLSemantics(t *testing.T) {
	ctx, repository := prepareRenameFixture(t)
	const from, to = "branch2", "native-renamed"
	tip := createRenameGitRepository(ctx, t, repository.RepoPath(), from)
	worktree := filepath.Join(t.TempDir(), "linked")
	renameGit(ctx, t, repository.RepoPath(), "", "worktree", "add", worktree, from)
	evidence := installRenameTestHook(t, repository.RepoPath())
	gitRepo, err := git.OpenRepository(ctx, repository.RepoPath())
	require.NoError(t, err)
	defer gitRepo.Close()
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repository.OwnerID})
	result, err := repo_service.RenameBranch(ctx, repository, doer, gitRepo, from, to)
	require.NoError(t, err, "the HTTP hook must not block on the enclosing SQL branch/PR updates")
	require.Empty(t, result)
	evidence.assertAdmitted(t, execcontext.TargetCode)
	require.False(t, gitRepo.IsBranchExist(from))
	require.Equal(t, tip, renameGit(ctx, t, repository.RepoPath(), "", "rev-parse", "refs/heads/"+to))
	require.Equal(t, "refs/heads/"+to, renameGit(ctx, t, repository.RepoPath(), "", "symbolic-ref", "HEAD"))
	require.Equal(t, "refs/heads/"+to, renameGit(ctx, t, worktree, "", "symbolic-ref", "HEAD"))
	require.Equal(t, "native-rename-config", renameGit(ctx, t, repository.RepoPath(), "", "config", "--get", "branch."+to+".description"))
	require.Contains(t, renameGit(ctx, t, repository.RepoPath(), "", "reflog", "show", "--format=%gs", "refs/heads/"+to), "renamed refs/heads/"+from+" to refs/heads/"+to)
	branch, err := git_model.GetBranch(ctx, repository.ID, to)
	require.NoError(t, err)
	require.Equal(t, to, branch.Name)
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Empty(t, held.Owner)
}

func TestWikiRefRenamePreservesCodeHEADAndTargetBoundary(t *testing.T) {
	ctx, repository := prepareRenameFixture(t)
	createRenameGitRepository(ctx, t, repository.RepoPath(), repository.DefaultBranch)
	from := repository.GetWikiBranchName()
	const to = "wiki-renamed"
	tip := createRenameGitRepository(ctx, t, repository.WikiPath(), from)
	evidence := installRenameTestHook(t, repository.WikiPath())
	require.NoError(t, wiki_service.NormalizeWikiBranch(ctx, repository, to))
	evidence.assertAdmitted(t, execcontext.TargetWiki)
	require.Equal(t, "refs/heads/"+repository.DefaultBranch, renameGit(ctx, t, repository.RepoPath(), "", "symbolic-ref", "HEAD"))
	require.Equal(t, "refs/heads/"+to, renameGit(ctx, t, repository.WikiPath(), "", "symbolic-ref", "HEAD"))
	require.Equal(t, tip, renameGit(ctx, t, repository.WikiPath(), "", "rev-parse", "refs/heads/"+to))
	stored := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: repository.ID})
	require.Equal(t, to, stored.WikiBranch)
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Empty(t, held.Owner)
}

func TestRefRenameEnteredMetadataFailureKeepsWitnessAndFence(t *testing.T) {
	ctx, repository := prepareRenameFixture(t)
	const from, to = "branch2", "failed-metadata"
	tip := createRenameGitRepository(ctx, t, repository.RepoPath(), from)
	evidence := installRenameTestHook(t, repository.RepoPath())
	gitRepo, err := git.OpenRepository(ctx, repository.RepoPath())
	require.NoError(t, err)
	defer gitRepo.Close()
	service := nativeoperation_service.NewService()
	injected := errors.New("metadata transaction failed after native rename")
	zero := strings.Repeat("0", len(tip))
	err = service.WithOrdinaryOwnership(ctx, nativeoperation_service.FamilyRefWrite,
		execcontext.RefWriteResource(repository.ID, nativeoperation_service.RefWriteRename, from+"->"+to),
		nativeoperation_service.Scope{
			RepositoryID: repository.ID, RepositoryTarget: execcontext.TargetCode,
			Refs: []nativeoperation_service.ScopedRef{{Ref: "refs/heads/" + from, OldOID: tip, NewOID: zero}, {Ref: "refs/heads/" + to, NewOID: tip}},
		},
		func(owned context.Context) error {
			return service.WithRefRename(owned, repository, gitRepo, execcontext.TargetCode, from, to, tip, func(run func() error) error {
				return git_model.RenameBranch(owned, repository, from, to, func(context.Context, bool) error {
					if err := run(); err != nil {
						return err
					}
					return injected
				})
			})
		})
	require.ErrorIs(t, err, injected)
	require.ErrorIs(t, err, execcontext.ErrIndeterminateEffect)
	evidence.assertAdmitted(t, execcontext.TargetCode)
	require.False(t, gitRepo.IsBranchExist(from))
	require.Equal(t, tip, renameGit(ctx, t, repository.RepoPath(), "", "rev-parse", "refs/heads/"+to))
	_, err = git_model.GetBranch(ctx, repository.ID, from)
	require.NoError(t, err, "the metadata transaction must roll back while Git's landed result remains fenced")
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, held.Owner)
	document, err := execcontext.DecodeScopeDocument(held.Scope)
	require.NoError(t, err)
	require.NotNil(t, document.Scope().NativeRename)
	entered := false
	err = service.WithOrdinaryOwnership(ctx, "test", "competing-write", nativeoperation_service.Scope{}, func(context.Context) error {
		entered = true
		return nil
	})
	require.ErrorIs(t, err, nativeoperation_model.ErrBusy)
	require.False(t, entered)
}

func TestRefRenameSwallowedLateGitFailureKeepsWitnessAndFence(t *testing.T) {
	ctx, repository := prepareRenameFixture(t)
	const from, to = "branch2", "failed-config"
	tip := createRenameGitRepository(ctx, t, repository.RepoPath(), from)
	evidence := installRenameTestHook(t, repository.RepoPath())
	gitRepo, err := git.OpenRepository(ctx, repository.RepoPath())
	require.NoError(t, err)
	defer gitRepo.Close()
	require.NoError(t, os.WriteFile(filepath.Join(repository.RepoPath(), "config.lock"), []byte("locked\n"), 0o600))
	service := nativeoperation_service.NewService()
	zero := strings.Repeat("0", len(tip))
	var launchErr error
	err = service.WithOrdinaryOwnership(ctx, nativeoperation_service.FamilyRefWrite,
		execcontext.RefWriteResource(repository.ID, nativeoperation_service.RefWriteRename, from+"->"+to),
		nativeoperation_service.Scope{
			RepositoryID: repository.ID, RepositoryTarget: execcontext.TargetCode,
			Refs: []nativeoperation_service.ScopedRef{{Ref: "refs/heads/" + from, OldOID: tip, NewOID: zero}, {Ref: "refs/heads/" + to, NewOID: tip}},
		},
		func(owned context.Context) error {
			return service.WithRefRename(owned, repository, gitRepo, execcontext.TargetCode, from, to, tip, func(run func() error) error {
				launchErr = run()
				return nil
			})
		})
	require.Error(t, launchErr)
	require.ErrorIs(t, err, execcontext.ErrIndeterminateEffect)
	evidence.assertAdmitted(t, execcontext.TargetCode)
	require.False(t, gitRepo.IsBranchExist(from))
	require.Equal(t, tip, renameGit(ctx, t, repository.RepoPath(), "", "rev-parse", "refs/heads/"+to))
	require.Equal(t, "refs/heads/"+to, renameGit(ctx, t, repository.RepoPath(), "", "symbolic-ref", "HEAD"))
	require.Equal(t, "native-rename-config", renameGit(ctx, t, repository.RepoPath(), "", "config", "--get", "branch."+from+".description"))
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, held.Owner, "successful ref/HEAD checks cannot erase a failed native configuration update")
	document, err := execcontext.DecodeScopeDocument(held.Scope)
	require.NoError(t, err)
	require.NotNil(t, document.Scope().NativeRename)
}

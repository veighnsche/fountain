// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	activities_model "forgejo.org/models/activities"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/queue"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	"forgejo.org/services/feed"
	nativeoperation_service "forgejo.org/services/nativeoperation"
	notify_service "forgejo.org/services/notify"
	pull_service "forgejo.org/services/pull"

	"github.com/stretchr/testify/require"
)

func TestPushHandlerRetainsBusyWork(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	_, err := nativeoperation_model.ClaimOrdinary(db.DefaultContext, "ord:branch-delete/1/branch2", `{"kind":"ordinary"}`, "v")
	require.NoError(t, err)

	opts := []*repo_module.PushUpdateOptions{{
		RefFullName:  git.RefNameFromBranch("master"),
		OldCommitID:  "65f1bf27bc3bf70f64657658635e66094edbcb4d",
		NewCommitID:  "985f0301dba5e7b34be866819cd15ad3d8f508ee",
		PusherID:     2,
		PusherName:   "user2",
		RepoUserName: "user2",
		RepoName:     "repo1",
	}}
	unhandled := handler(opts)
	require.Len(t, unhandled, 1)
	require.Equal(t, opts, unhandled[0])

	// The retained batch is not consumed: the owner still holds it.
	held, err := nativeoperation_model.ReadReservation(db.DefaultContext)
	require.NoError(t, err)
	require.Equal(t, "ord:branch-delete/1/branch2", held.Owner)
	if os.Getenv("SODA_FOUNTAIN_PUSH_ENGINE") != "1" {
		return
	}

	// Qualify the shared engine against real local Git, SQL and the native
	// feed notifier, extending the existing integration root rather than a mock.
	ctx := t.Context()
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, held.Owner))
	oldRoot := setting.RepoRootPath
	setting.RepoRootPath = t.TempDir()
	t.Cleanup(func() { setting.RepoRootPath = oldRoot })
	repo := &repo_model.Repository{OwnerID: 2, OwnerName: "user2", Name: "push-engine", LowerName: "push-engine", IsPrivate: true, DefaultBranch: "main", ObjectFormatName: "sha1"}
	_, err = db.GetEngine(ctx).Insert(repo)
	require.NoError(t, err)
	_, err = db.GetEngine(ctx).Insert(&repo_model.RepoUnit{RepoID: repo.ID, Type: unit.TypeCode}, &repo_model.RepoUnit{RepoID: repo.ID, Type: unit.TypeIssues})
	require.NoError(t, err)
	issue := &issues_model.Issue{RepoID: repo.ID, Index: 1, PosterID: 2, Title: "native push engine issue"}
	_, err = db.GetEngine(ctx).Insert(issue)
	require.NoError(t, err)

	work := t.TempDir()
	gitRun := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=User Two", "GIT_AUTHOR_EMAIL=user2@example.com", "GIT_COMMITTER_NAME=User Two", "GIT_COMMITTER_EMAIL=user2@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(repo.RepoPath()), 0o755))
	gitRun(work, "init", "--bare", repo.RepoPath())
	gitRun(work, "clone", repo.RepoPath(), work)
	gitRun(work, "checkout", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("base\n"), 0o600))
	gitRun(work, "add", "file.txt")
	gitRun(work, "commit", "-m", "native base")
	base := gitRun(work, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("changed\n"), 0o600))
	gitRun(work, "commit", "-am", "Fixes #1")
	tip := gitRun(work, "rev-parse", "HEAD")
	gitRun(work, "push", "origin", "main")
	gitRun(repo.RepoPath(), "symbolic-ref", "HEAD", "refs/heads/main")
	updates := []*repo_module.PushUpdateOptions{{RefFullName: git.RefNameFromBranch("main"), OldCommitID: base, NewCommitID: tip, PusherID: 2, PusherName: "user2", RepoUserName: repo.OwnerName, RepoName: repo.Name, TimeNano: time.Now().UnixNano()}}

	notifier := feed.NewNotifier()
	notify_service.RegisterNotifier(notifier)
	t.Cleanup(func() { notify_service.UnregisterNotifier(notifier) })
	require.NoError(t, pull_service.InitPullRequestTaskQueue())
	pull_service.StartPullRequestTasks()

	// No owner, no execution context, and a changed generation all refuse
	// before repository size, issue or notification effects.
	require.ErrorIs(t, ProcessPushUpdates(ctx, repo, updates), nativeoperation_model.ErrWrongOwner)
	scope, err := json.Marshal(nativeoperation_service.Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: nativeoperation_service.FamilyPushCompletion, RepositoryID: repo.ID, Refs: []nativeoperation_service.ScopedRef{{Ref: "refs/heads/main", OldOID: base, NewOID: tip}}, PusherID: 2})
	require.NoError(t, err)
	held, err = nativeoperation_model.ClaimOrdinary(ctx, "ord:push-engine/held", string(scope), "fixture-verifier")
	require.NoError(t, err)
	defer func() {
		current, err := nativeoperation_model.ReadReservation(context.WithoutCancel(ctx))
		require.NoError(t, err)
		if current.Owner == held.Owner {
			require.NoError(t, nativeoperation_model.ReleaseOwner(context.WithoutCancel(ctx), held.Owner))
		}
	}()
	owned := execcontext.NewContext(ctx, &execcontext.Execution{Owner: held.Owner, Generation: held.Generation})
	wrong := execcontext.NewContext(ctx, &execcontext.Execution{Owner: held.Owner, Generation: held.Generation + 1})
	require.ErrorIs(t, ProcessPushUpdates(ctx, repo, updates), nativeoperation_model.ErrWrongOwner)
	require.ErrorIs(t, ProcessPushUpdates(wrong, repo, updates), nativeoperation_model.ErrWrongOwner)
	badUpdate := *updates[0]
	badUpdate.RepoName = "different-repository"
	require.Error(t, ProcessPushUpdates(owned, repo, []*repo_module.PushUpdateOptions{&badUpdate}))
	var actionsBefore int64
	actionsBefore, err = db.GetEngine(ctx).Where("repo_id=?", repo.ID).Count(new(activities_model.Action))
	require.NoError(t, err)
	require.Zero(t, actionsBefore)

	require.NoError(t, ProcessPushUpdates(owned, repo, updates))
	retained, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, held, retained, "shared engine must retain ownership and revision")
	updated, err := repo_model.GetRepositoryByID(ctx, repo.ID)
	require.NoError(t, err)
	require.Positive(t, updated.Size)
	require.Positive(t, updated.UpdatedUnix)
	closed, err := issues_model.GetIssueByID(ctx, issue.ID)
	require.NoError(t, err)
	require.True(t, closed.IsClosed)
	refCount, err := db.GetEngine(ctx).Where("issue_id=? AND type=? AND commit_sha=?", issue.ID, issues_model.CommentTypeCommitRef, tip).Count(new(issues_model.Comment))
	require.NoError(t, err)
	require.EqualValues(t, 1, refCount)
	closeCount, err := db.GetEngine(ctx).Where("issue_id=? AND type=?", issue.ID, issues_model.CommentTypeClose).Count(new(issues_model.Comment))
	require.NoError(t, err)
	require.EqualValues(t, 1, closeCount)
	var acts []activities_model.Action
	require.NoError(t, db.GetEngine(ctx).Where("repo_id=? AND op_type=?", repo.ID, activities_model.ActionCommitRepo).Find(&acts))
	require.NotEmpty(t, acts)
	for _, act := range acts {
		require.EqualValues(t, 2, act.ActUserID)
		require.Equal(t, "refs/heads/main", act.RefName)
		require.True(t, act.IsPrivate)
		var commits repo_module.PushCommits
		require.NoError(t, json.Unmarshal([]byte(act.Content), &commits))
		require.Equal(t, tip, commits.HeadCommit.Sha1)
		require.EqualValues(t, 1, commits.Len)
		require.Len(t, commits.Commits, 1)
		require.Equal(t, tip, commits.Commits[0].Sha1)
		require.Contains(t, commits.CompareURL, base)
		require.Contains(t, commits.CompareURL, tip)
	}
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, held.Owner))
	require.NoError(t, queue.GetManager().FlushAll(ctx, 5*time.Second))

	// The ordinary worker still claims and releases around this same engine.
	// Native commit-reference/status work remains naturally idempotent; the
	// caller, not the engine, owns one-shot notification dispatch.
	idle, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	// Native repository lookup accepts case variants of owner/repository names.
	ordinaryUpdate := *updates[0]
	ordinaryUpdate.RepoUserName = strings.ToUpper(ordinaryUpdate.RepoUserName)
	ordinaryUpdate.RepoName = strings.ToUpper(ordinaryUpdate.RepoName)
	require.NoError(t, pushUpdates([]*repo_module.PushUpdateOptions{&ordinaryUpdate}))
	after, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Empty(t, after.Owner)
	require.Greater(t, after.Revision, idle.Revision)
	require.Greater(t, after.Generation, idle.Generation)
	require.NoError(t, queue.GetManager().FlushAll(ctx, 5*time.Second))
	refCount, err = db.GetEngine(ctx).Where("issue_id=? AND type=? AND commit_sha=?", issue.ID, issues_model.CommentTypeCommitRef, tip).Count(new(issues_model.Comment))
	require.NoError(t, err)
	require.EqualValues(t, 1, refCount)
	closeCount, err = db.GetEngine(ctx).Where("issue_id=? AND type=?", issue.ID, issues_model.CommentTypeClose).Count(new(issues_model.Comment))
	require.NoError(t, err)
	require.EqualValues(t, 1, closeCount)
	t.Log("PASS: actual Git/SQL/feed push engine; held-owner refusal/retention, issue reference/closure and ordinary claim/release")
}

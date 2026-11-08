// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"context"
	"errors"
	"fmt"

	execcontext "forgejo.org/modules/nativeoperation"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/log"
	"forgejo.org/modules/process"
	"forgejo.org/modules/queue"
)

// Store event data, never the completing push's execution capability.
type pullRequestTask struct {
	DoerID, RepositoryID         int64
	Branch, OldCommit, NewCommit string
	Sync                         bool
	TimeNano                     int64
}

var pullRequestTaskQueue *queue.WorkerPoolQueue[*pullRequestTask]

// AddTestPullRequestTask retains the PR synchronization event until a fresh
// owner can process it. The completing push never waits for this queue.
func AddTestPullRequestTask(_ context.Context, doer *user_model.User, repoID int64, branch string, isSync bool, oldCommitID, newCommitID string, timeNano int64) {
	err := pullRequestTaskQueue.Push(&pullRequestTask{
		DoerID: doer.ID, RepositoryID: repoID, Branch: branch, Sync: isSync,
		OldCommit: oldCommitID, NewCommit: newCommitID, TimeNano: timeNano,
	})
	if err != nil {
		log.Error("Queue PR synchronization for repository %d: %v", repoID, err)
	}
}

func pullRequestTaskHandler(tasks ...*pullRequestTask) []*pullRequestTask {
	var retry []*pullRequestTask
	for _, task := range tasks {
		err := runPullRequestTask(task)
		if errors.Is(err, nativeoperation_model.ErrBusy) || errors.Is(err, nativeoperation_model.ErrInhibited) {
			retry = append(retry, task)
		} else if err != nil {
			log.Error("PR synchronization for repository %d: %v", task.RepositoryID, err)
		}
	}
	return retry
}

func runPullRequestTask(task *pullRequestTask) error {
	description := fmt.Sprintf("AddTestPullRequestTask [head_repo_id: %d, head_branch: %s]: only pull requests created before nano time %d will be considered", task.RepositoryID, task.Branch, task.TimeNano)
	ctx, _, finished := process.GetManager().AddContext(graceful.GetManager().ShutdownContext(), description)
	defer finished()
	var doer *user_model.User
	var err error
	if task.DoerID == user_model.ActionsUserID {
		doer = user_model.NewActionsUser()
	} else {
		doer, err = user_model.GetUserByID(ctx, task.DoerID)
	}
	if err != nil {
		return err
	}
	return withCollabOwnershipWait(ctx, execcontext.CollabBatchResource("pr-sweep"), collabScope{RepositoryID: task.RepositoryID, HeadRef: git.BranchPrefix + task.Branch}, func(ctx context.Context) error {
		return testPullRequest(ctx, doer, task.RepositoryID, task.TimeNano, task.Branch, task.Sync, task.OldCommit, task.NewCommit)
	})
}

// InitPullRequestTaskQueue prepares storage before persisted push completion
// starts. Processing waits until the PR checker and Actions notifier are ready.
func InitPullRequestTaskQueue() error {
	pullRequestTaskQueue = queue.CreateSimpleQueue(graceful.GetManager().ShutdownContext(), "pr_sweep", pullRequestTaskHandler)
	if pullRequestTaskQueue == nil {
		return errors.New("unable to create pr_sweep queue")
	}
	return nil
}

// StartPullRequestTasks runs deferred synchronization after core initialization.
func StartPullRequestTasks() {
	go graceful.GetManager().RunWithCancel(pullRequestTaskQueue)
}

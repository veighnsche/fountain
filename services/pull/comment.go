// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"context"

	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/json"
	execcontext "forgejo.org/modules/nativeoperation"
)

// getCommitIDsFromRepo get commit IDs from repo in between oldCommitID and newCommitID
// isForcePush will be true if oldCommit isn't on the branch
// Commit on baseBranch will skip
func getCommitIDsFromRepo(ctx context.Context, repo *repo_model.Repository, oldCommitID, newCommitID, baseBranch string) (commitIDs []string, isForcePush bool, err error) {
	gitRepo, closer, err := gitrepo.RepositoryFromContextOrOpen(ctx, repo)
	if err != nil {
		return nil, false, err
	}
	defer closer.Close()

	oldCommit, err := gitRepo.GetCommit(oldCommitID)
	if err != nil {
		return nil, false, err
	}

	newCommit, err := gitRepo.GetCommit(newCommitID)
	if err != nil {
		return nil, false, err
	}

	isForcePush, err = newCommit.IsForcePush(oldCommitID)
	if err != nil {
		return nil, false, err
	}

	if isForcePush {
		commitIDs = make([]string, 2)
		commitIDs[0] = oldCommitID
		commitIDs[1] = newCommitID

		return commitIDs, isForcePush, err
	}

	// Find commits between new and old commit excluding base branch commits
	commits, err := gitRepo.CommitsBetweenNotBase(newCommit, oldCommit, baseBranch)
	if err != nil {
		return nil, false, err
	}

	commitIDs = make([]string, 0, len(commits))
	for i := len(commits) - 1; i >= 0; i-- {
		commitIDs = append(commitIDs, commits[i].ID.String())
	}

	return commitIDs, isForcePush, err
}

// CreatePushPullComment create push code to pull base comment
func CreatePushPullComment(ctx context.Context, pusher *user_model.User, pr *issues_model.PullRequest, oldCommitID, newCommitID string) (comment *issues_model.Comment, err error) {
	// One collaboration writer owns the push comment before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale. Push and receive flows normally carry the
	// enclosing execution; the claim only binds standalone callers.
	err = withCollabOwnership(ctx, execcontext.CommentCreateResource(pr.IssueID), pr.BaseRepoID, func(ctx context.Context) error {
		comment, err = doCreatePushPullComment(ctx, pusher, pr, oldCommitID, newCommitID)
		return err
	})
	return comment, err
}

func doCreatePushPullComment(ctx context.Context, pusher *user_model.User, pr *issues_model.PullRequest, oldCommitID, newCommitID string) (comment *issues_model.Comment, err error) {
	if pr.HasMerged || oldCommitID == "" || newCommitID == "" {
		return nil, nil
	}

	ops := &issues_model.CreateCommentOptions{
		Type: issues_model.CommentTypePullRequestPush,
		Doer: pusher,
		Repo: pr.BaseRepo,
	}

	var data issues_model.PushActionContent

	data.CommitIDs, data.IsForcePush, err = getCommitIDsFromRepo(ctx, pr.BaseRepo, oldCommitID, newCommitID, pr.BaseBranch)
	if err != nil {
		return nil, err
	}

	if err := pr.LoadIssue(ctx); err != nil {
		return nil, err
	}

	ops.Issue = pr.Issue

	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	ops.Content = string(dataJSON)

	comment, err = issues_model.CreateComment(ctx, ops)

	return comment, err
}

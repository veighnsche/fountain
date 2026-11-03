// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	sdk "forgejo.org/extension-sdk"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	repo_module "forgejo.org/modules/repository"
	pull_service "forgejo.org/services/pull"
)

// SyncHumanMergeBranch mirrors the committed merge target through the same
// native branch-catalog owner used by ordinary post-receive. Exact PR metadata
// must already match the receipt. A fresh native catalog keeps its usual bootstrap
// behavior. No ref write, push queue/notification, completion or owner release occurs.
func (s *Service) SyncHumanMergeBranch(ctx context.Context, installation string, q sdk.HumanMergeRequest) error {
	_, err := s.withHumanMergeReceipt(ctx, installation, q, func(tx context.Context, receipt sdk.HumanMergeReceipt, pr *issues_model.PullRequest, _ *user_model.User) (bool, error) {
		if !pr.HasMerged || !pr.Issue.IsClosed || pr.MergedCommitID != receipt.ResultOID || pr.MergerID != receipt.ActorID || pr.MergeBase != receipt.MergeBaseOID || pr.MergedUnix <= 0 {
			return false, pull_service.ErrMergeMetadata
		}
		repository, err := repo_model.GetRepositoryByID(tx, receipt.RepositoryID)
		if err != nil {
			return false, err
		}
		native, err := git.OpenRepository(tx, repository.RepoPath())
		if err != nil {
			return false, err
		}
		defer native.Close()
		if err := repo_module.SyncBranchesToDB(tx, repository.ID, receipt.ActorID, []string{receipt.BaseBranch}, native.GetBranchCommit); err != nil {
			return false, err
		}
		branch, err := git_model.GetBranch(tx, repository.ID, receipt.BaseBranch)
		if err != nil {
			return false, err
		}
		if branch.IsDeleted || branch.CommitID != receipt.ResultOID {
			return false, ErrHumanMergePushEvidence
		}
		return false, nil
	})
	return err
}

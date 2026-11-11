// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issue

import (
	"context"

	issues_model "forgejo.org/models/issues"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	notify_service "forgejo.org/services/notify"
)

// ChangeContent changes issue content, as the given user.
func ChangeContent(ctx context.Context, issue *issues_model.Issue, doer *user_model.User, content string, contentVersion int) (err error) {
	// One collaboration writer owns the content change before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale.
	return withCollabOwnership(ctx, execcontext.IssueResource(issue.ID, "content"), issue.RepoID, func(ctx context.Context) error {
		return doChangeContent(ctx, issue, doer, content, contentVersion)
	})
}

func doChangeContent(ctx context.Context, issue *issues_model.Issue, doer *user_model.User, content string, contentVersion int) (err error) {
	oldContent := issue.Content

	if err := issues_model.ChangeIssueContent(ctx, issue, doer, content, contentVersion); err != nil {
		return err
	}

	notify_service.IssueChangeContent(ctx, doer, issue, oldContent)

	return nil
}

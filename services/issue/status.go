// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issue

import (
	"context"

	issues_model "forgejo.org/models/issues"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	execcontext "forgejo.org/modules/nativeoperation"
	notify_service "forgejo.org/services/notify"
)

// ChangeStatus changes issue status to open or closed.
func ChangeStatus(ctx context.Context, issue *issues_model.Issue, doer *user_model.User, commitID string, closed bool) error {
	// One collaboration writer owns the status change before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale.
	return withCollabOwnership(ctx, execcontext.IssueResource(issue.ID, "status"), issue.RepoID, func(ctx context.Context) error {
		return doChangeStatus(ctx, issue, doer, commitID, closed)
	})
}

func doChangeStatus(ctx context.Context, issue *issues_model.Issue, doer *user_model.User, commitID string, closed bool) error {
	comment, err := issues_model.ChangeIssueStatus(ctx, issue, doer, closed)
	if err != nil {
		if issues_model.IsErrDependenciesLeft(err) && closed {
			if err := issues_model.FinishIssueStopwatchIfPossible(ctx, doer, issue); err != nil {
				log.Error("Unable to stop stopwatch for issue[%d]#%d: %v", issue.ID, issue.Index, err)
			}
		}
		return err
	}

	if closed {
		if err := issues_model.FinishIssueStopwatchIfPossible(ctx, doer, issue); err != nil {
			return err
		}
	}

	notify_service.IssueChangeStatus(ctx, doer, commitID, issue, comment, closed)

	return nil
}

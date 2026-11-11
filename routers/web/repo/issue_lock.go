// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	stdCtx "context"

	issues_model "forgejo.org/models/issues"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

// LockIssue locks an issue. This would limit commenting abilities to
// users with write access to the repo.
func LockIssue(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.IssueLockForm)
	issue := GetActionIssue(ctx)
	if ctx.Written() {
		return
	}

	if issue.IsLocked {
		ctx.JSONError(ctx.Tr("repo.issues.lock_duplicate"))
		return
	}

	if !form.HasValidReason() {
		ctx.JSONError(ctx.Tr("repo.issues.lock.unknown_reason"))
		return
	}

	// One collaboration writer owns the lock change before its effects,
	// advancing the native revision so old accepted-input observations
	// go stale.
	doer := ctx.Doer
	if err := nativeoperation_service.WithCollaborationOwnership(ctx, execcontext.IssueResource(issue.ID, "lock"), issue.RepoID, func(ctx stdCtx.Context) error {
		return issues_model.LockIssue(ctx, &issues_model.IssueLockOptions{
			Doer:   doer,
			Issue:  issue,
			Reason: form.Reason,
		})
	}); err != nil {
		ctx.ServerError("LockIssue", err)
		return
	}

	ctx.JSONRedirect(issue.Link())
}

// UnlockIssue unlocks a previously locked issue.
func UnlockIssue(ctx *context.Context) {
	issue := GetActionIssue(ctx)
	if ctx.Written() {
		return
	}

	if !issue.IsLocked {
		ctx.JSONError(ctx.Tr("repo.issues.unlock_error"))
		return
	}

	// One collaboration writer owns the lock change before its effects,
	// advancing the native revision so old accepted-input observations
	// go stale.
	doer := ctx.Doer
	if err := nativeoperation_service.WithCollaborationOwnership(ctx, execcontext.IssueResource(issue.ID, "lock"), issue.RepoID, func(ctx stdCtx.Context) error {
		return issues_model.UnlockIssue(ctx, &issues_model.IssueLockOptions{
			Doer:  doer,
			Issue: issue,
		})
	}); err != nil {
		ctx.ServerError("UnlockIssue", err)
		return
	}

	ctx.JSONRedirect(issue.Link())
}

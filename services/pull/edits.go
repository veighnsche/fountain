// Copyright 2022 The Gitea Authors.
// All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"context"
	"errors"

	execcontext "forgejo.org/modules/nativeoperation"

	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	unit_model "forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
)

var ErrUserHasNoPermissionForAction = errors.New("user not allowed to do this action")

// SetAllowEdits allow edits from maintainers to PRs
func SetAllowEdits(ctx context.Context, doer *user_model.User, pr *issues_model.PullRequest, allow bool) error {
	// One collaboration writer owns the maintainer-edits change before
	// its effects, advancing the native revision so old accepted-input
	// observations go stale.
	return withCollabOwnership(ctx, execcontext.PullResource(pr.ID, "edits"), pr.BaseRepoID, func(ctx context.Context) error {
		return doSetAllowEdits(ctx, doer, pr, allow)
	})
}

func doSetAllowEdits(ctx context.Context, doer *user_model.User, pr *issues_model.PullRequest, allow bool) error {
	if doer == nil || !pr.Issue.IsPoster(doer.ID) {
		return ErrUserHasNoPermissionForAction
	}

	if err := pr.LoadHeadRepo(ctx); err != nil {
		return err
	}

	permission, err := access_model.GetUserRepoPermission(ctx, pr.HeadRepo, doer)
	if err != nil {
		return err
	}

	if !permission.CanWrite(unit_model.TypeCode) {
		return ErrUserHasNoPermissionForAction
	}

	pr.AllowMaintainerEdit = allow
	return issues_model.UpdateAllowEdits(ctx, pr)
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"context"

	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
)

// NativeActionBlocker names a native rule, independently of a caller's wire profile.
type NativeActionBlocker string

const (
	NativeActionAllowed       NativeActionBlocker = ""
	NativeActionUnreadable    NativeActionBlocker = "subject_unreadable"
	NativeActionArchived      NativeActionBlocker = "repository_archived"
	NativeActionBlocked       NativeActionBlocker = "actor_blocked"
	NativeActionLocked        NativeActionBlocker = "discussion_locked"
	NativeActionPermission    NativeActionBlocker = "permission_required"
	NativeActionMerged        NativeActionBlocker = "pull_already_merged"
	NativeActionClosed        NativeActionBlocker = "discussion_already_closed"
	NativeActionNotClosed     NativeActionBlocker = "discussion_not_closed"
	NativeActionAlreadyLocked NativeActionBlocker = "discussion_already_locked"
	NativeActionNotLocked     NativeActionBlocker = "discussion_not_locked"
	NativeActionDependencies  NativeActionBlocker = "open_dependencies"
)

// DiscussionActionFacts are current model facts, not an admission or a cached
// permission grant. Writers must derive them again under their held owner.
type DiscussionActionFacts struct {
	Readable, Archived, Writer, AnyWriter bool
	Author, Poster, Blocked, SiteAdmin    bool
	Closed, Locked                        bool
}

func ReadDiscussionActionFacts(ctx context.Context, repo *repo_model.Repository, issue *Issue, actor *user_model.User, permission access_model.Permission) DiscussionActionFacts {
	kind := unit.TypeIssues
	if issue.IsPull {
		kind = unit.TypePullRequests
	}
	return DiscussionActionFacts{
		Readable:  !kind.UnitGlobalDisabled() && repo.UnitEnabled(ctx, kind) && permission.CanRead(kind),
		Archived:  repo.IsArchived,
		Writer:    permission.CanWriteIssuesOrPulls(issue.IsPull),
		AnyWriter: permission.CanAccessAny(perm.AccessModeWrite, unit.TypeIssues, unit.TypePullRequests),
		Author:    issue.IsPoster(actor.ID), Poster: issue.PosterID == actor.ID,
		Blocked:   user_model.IsBlockedMultiple(ctx, []int64{repo.OwnerID, issue.PosterID}, actor.ID),
		SiteAdmin: actor.IsAdmin, Closed: issue.IsClosed, Locked: issue.IsLocked,
	}
}

func (f DiscussionActionFacts) mutable() NativeActionBlocker {
	if !f.Readable {
		return NativeActionUnreadable
	}
	if f.Archived {
		return NativeActionArchived
	}
	return NativeActionAllowed
}

func (f DiscussionActionFacts) Comment() NativeActionBlocker {
	if b := f.mutable(); b != NativeActionAllowed {
		return b
	}
	if f.Locked && !f.Writer && !f.SiteAdmin {
		return NativeActionLocked
	}
	if f.Blocked {
		return NativeActionBlocked
	}
	return NativeActionAllowed
}

func (f DiscussionActionFacts) TitleEdit() NativeActionBlocker {
	if b := f.mutable(); b != NativeActionAllowed {
		return b
	}
	if f.Blocked {
		return NativeActionBlocked
	}
	if !f.Author && !f.Writer {
		return NativeActionPermission
	}
	return NativeActionAllowed
}

func (f DiscussionActionFacts) BodyEdit() NativeActionBlocker {
	if b := f.mutable(); b != NativeActionAllowed {
		return b
	}
	if !f.Poster && !f.Writer {
		return NativeActionPermission
	}
	return NativeActionAllowed
}

func (f DiscussionActionFacts) StatePermission() NativeActionBlocker {
	return f.TitleEdit()
}

func (f DiscussionActionFacts) StateActions(hasMerged bool) (close, reopen NativeActionBlocker) {
	close, reopen = f.StatePermission(), f.StatePermission()
	if close == NativeActionAllowed {
		close, reopen = NativeActionAllowed, NativeActionNotClosed
		if f.Closed {
			close, reopen = NativeActionClosed, NativeActionAllowed
		}
	}
	if hasMerged {
		close, reopen = NativeActionMerged, NativeActionMerged
	}
	if f.Blocked {
		close, reopen = NativeActionBlocked, NativeActionBlocked
	}
	if f.Archived {
		close, reopen = NativeActionArchived, NativeActionArchived
	}
	if !f.Readable {
		close, reopen = NativeActionUnreadable, NativeActionUnreadable
	}
	return close, reopen
}

func (f DiscussionActionFacts) LockPermission() NativeActionBlocker {
	if b := f.mutable(); b != NativeActionAllowed {
		return b
	}
	if !f.AnyWriter {
		return NativeActionPermission
	}
	return NativeActionAllowed
}

func (f DiscussionActionFacts) LockActions() (lock, unlock NativeActionBlocker) {
	lock, unlock = f.LockPermission(), f.LockPermission()
	if lock != NativeActionAllowed {
		return lock, unlock
	}
	if f.Locked {
		return NativeActionAlreadyLocked, NativeActionAllowed
	}
	return NativeActionAllowed, NativeActionNotLocked
}

// CheckNativeCloseDependencies is deliberately separate from the other facts:
// only an otherwise admitted close requires this query, and read errors remain
// availability errors rather than a denial or a stale-state classification.
func CheckNativeCloseDependencies(ctx context.Context, repo *repo_model.Repository, issue *Issue) (NativeActionBlocker, error) {
	if !repo.IsDependenciesEnabled(ctx) {
		return NativeActionAllowed, nil
	}
	clear, err := IssueNoDependenciesLeft(ctx, issue)
	if err != nil {
		return NativeActionAllowed, err
	}
	if !clear {
		return NativeActionDependencies, nil
	}
	return NativeActionAllowed, nil
}

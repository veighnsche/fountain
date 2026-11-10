// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues_test

import (
	"testing"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"

	"github.com/stretchr/testify/require"
)

func TestDiscussionActionFactsPreserveImportedPosterAndNativeLockRules(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: issue.RepoID})
	actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: issue.PosterID})
	actor.IsAdmin = false
	permission := access_model.Permission{UnitsMode: map[unit.Type]perm.AccessMode{unit.TypeIssues: perm.AccessModeRead}}
	issue.OriginalAuthorID = 123
	issue.IsLocked = true
	facts := issues_model.ReadDiscussionActionFacts(db.DefaultContext, repo, issue, actor, permission)
	require.True(t, facts.Readable)
	require.Equal(t, issues_model.NativeActionPermission, facts.TitleEdit())
	require.Equal(t, issues_model.NativeActionAllowed, facts.BodyEdit())
	require.Equal(t, issues_model.NativeActionLocked, facts.Comment())
	actor.IsAdmin = true
	facts = issues_model.ReadDiscussionActionFacts(db.DefaultContext, repo, issue, actor, permission)
	require.Equal(t, issues_model.NativeActionAllowed, facts.Comment())
	require.Equal(t, issues_model.NativeActionPermission, facts.LockPermission())
	permission.UnitsMode[unit.TypePullRequests] = perm.AccessModeWrite
	facts = issues_model.ReadDiscussionActionFacts(db.DefaultContext, repo, issue, actor, permission)
	require.Equal(t, issues_model.NativeActionAllowed, facts.LockPermission())
	require.Equal(t, issues_model.NativeActionPermission, facts.TitleEdit())
}

func TestDiscussionActionFactsBlockedAuthorKeepsBodyButCannotCommentTitleOrState(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 1})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: issue.RepoID})
	actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: issue.PosterID})
	repo.OwnerID = 4 // Fixture owner 4 blocks actor 1.
	permission := access_model.Permission{UnitsMode: map[unit.Type]perm.AccessMode{unit.TypeIssues: perm.AccessModeRead}}
	facts := issues_model.ReadDiscussionActionFacts(db.DefaultContext, repo, issue, actor, permission)
	require.True(t, facts.Blocked)
	require.Equal(t, issues_model.NativeActionBlocked, facts.Comment())
	require.Equal(t, issues_model.NativeActionBlocked, facts.TitleEdit())
	require.Equal(t, issues_model.NativeActionAllowed, facts.BodyEdit())
	facts.Locked = true
	facts.SiteAdmin = false
	require.Equal(t, issues_model.NativeActionLocked, facts.Comment())
	closeAction, reopenAction := facts.StateActions(true)
	require.Equal(t, issues_model.NativeActionBlocked, closeAction)
	require.Equal(t, issues_model.NativeActionBlocked, reopenAction)
	facts.Archived = true
	closeAction, reopenAction = facts.StateActions(true)
	require.Equal(t, issues_model.NativeActionArchived, closeAction)
	require.Equal(t, issues_model.NativeActionArchived, reopenAction)
	require.Equal(t, issues_model.NativeActionArchived, facts.BodyEdit())
}

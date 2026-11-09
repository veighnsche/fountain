// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package extensionauth stores native administrator enrollment of background
// actor bindings: which installation may use which native token, actor,
// repository and operation kind. It performs no factory policy.
package extensionauth

import (
	"context"
	"errors"
	"strings"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/timeutil"

	"github.com/google/uuid"
)

// Operation kinds mirror the SDK background kinds. Authorization for one kind
// never grants another.
const (
	KindIssueGraph      = "issue.graph.mutate"
	KindRefPublish      = "git.ref.publish"
	KindPRCreate        = "pull_request.create"
	KindReviewSubmit    = "pull_request.review.submit"
	KindThreadResolve   = "pull_request.review.thread.resolve"
	KindMerge           = "pull_request.merge"
	KindLifecycleIssue  = "issue.lifecycle.apply"
	KindLifecycleReview = "pull_request.review.request"
	KindLifecycleNote   = "issue.comment.publish"
	KindExecutionLaunch = "execution.launch"
)

// ActorBinding is one administrator approval binding an installation UUID to
// a native token, actor, repository and operation kind. It holds IDs only,
// never secrets.
type ActorBinding struct {
	ID             int64              `xorm:"pk autoincr"`
	InstallationID string             `xorm:"VARCHAR(36) NOT NULL index unique(binding)"`
	TokenID        int64              `xorm:"NOT NULL index unique(binding)"`
	ActorID        int64              `xorm:"NOT NULL index unique(binding)"`
	RepositoryID   int64              `xorm:"NOT NULL index unique(binding)"`
	Kind           string             `xorm:"VARCHAR(64) NOT NULL index unique(binding)"`
	CreatedUnix    timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix    timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func init() {
	db.RegisterModel(new(ActorBinding))
}

// ValidKind reports whether kind is a supported background operation kind.
func ValidKind(kind string) bool {
	switch kind {
	case KindIssueGraph, KindRefPublish, KindPRCreate, KindReviewSubmit, KindThreadResolve, KindMerge, KindLifecycleIssue, KindLifecycleReview, KindLifecycleNote, KindExecutionLaunch:
		return true
	default:
		return false
	}
}

// EnrollBinding records an administrator approval. It verifies the token row
// exists and belongs to the actor, and that the actor and repository exist.
// Enrollment is idempotent for an identical binding. Current token secrets,
// scopes, account state and permissions are rechecked at submission, not here.
func EnrollBinding(ctx context.Context, installationID string, tokenID, actorID, repositoryID int64, kind string) (*ActorBinding, error) {
	if _, err := uuid.Parse(installationID); err != nil {
		return nil, errors.New("invalid installation id")
	}
	if tokenID <= 0 || actorID <= 0 || repositoryID <= 0 || !ValidKind(kind) {
		return nil, errors.New("invalid actor binding")
	}
	token := new(auth_model.AccessToken)
	has, err := db.GetEngine(ctx).ID(tokenID).NoAutoCondition().Get(token)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, errors.New("access token does not exist")
	}
	if token.UID != actorID {
		return nil, errors.New("access token does not belong to the actor")
	}
	if _, err := user_model.GetUserByID(ctx, actorID); err != nil {
		return nil, errors.New("actor does not exist")
	}
	if _, err := repo_model.GetRepositoryByID(ctx, repositoryID); err != nil {
		return nil, errors.New("repository does not exist")
	}
	existing := new(ActorBinding)
	has, err = db.GetEngine(ctx).Where("installation_id=? AND token_id=? AND actor_id=? AND repository_id=? AND kind=?",
		installationID, tokenID, actorID, repositoryID, kind).NoAutoCondition().Get(existing)
	if err != nil {
		return nil, err
	}
	if has {
		return existing, nil
	}
	binding := &ActorBinding{
		InstallationID: installationID,
		TokenID:        tokenID,
		ActorID:        actorID,
		RepositoryID:   repositoryID,
		Kind:           strings.Clone(kind),
	}
	if err := db.Insert(ctx, binding); err != nil {
		return nil, err
	}
	return binding, nil
}

// ListBindings returns an installation's enrolled bindings ordered by ID.
func ListBindings(ctx context.Context, installationID string) ([]*ActorBinding, error) {
	if _, err := uuid.Parse(installationID); err != nil {
		return nil, errors.New("invalid installation id")
	}
	var bindings []*ActorBinding
	err := db.GetEngine(ctx).Where("installation_id=?", installationID).OrderBy("id").Find(&bindings)
	return bindings, err
}

// RevokeBinding deletes one enrolled binding. It reports whether a row
// existed. Outstanding operations of later milestones order their
// invalidation through the cancellation path; this only ends new admission.
func RevokeBinding(ctx context.Context, installationID string, tokenID, actorID, repositoryID int64, kind string) (bool, error) {
	if _, err := uuid.Parse(installationID); err != nil {
		return false, errors.New("invalid installation id")
	}
	if tokenID <= 0 || actorID <= 0 || repositoryID <= 0 || !ValidKind(kind) {
		return false, errors.New("invalid actor binding")
	}
	deleted, err := db.GetEngine(ctx).Where("installation_id=? AND token_id=? AND actor_id=? AND repository_id=? AND kind=?",
		installationID, tokenID, actorID, repositoryID, kind).Delete(new(ActorBinding))
	if err != nil {
		return false, err
	}
	return deleted > 0, nil
}

// HasAnyBinding reports whether any enrolled binding covers an installation,
// token, actor and repository across all operation kinds. Snapshot reads ride
// enrolled background authority without granting any mutation kind: the read
// reveals only what the token's actor can already observe, and kind-specific
// mutation admission still requires its own binding at submission.
func HasAnyBinding(ctx context.Context, installationID string, tokenID, actorID, repositoryID int64) (bool, error) {
	count, err := db.GetEngine(ctx).Where("installation_id=? AND token_id=? AND actor_id=? AND repository_id=?",
		installationID, tokenID, actorID, repositoryID).Count(new(ActorBinding))
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// FindBinding returns the binding matching a verified submission, if any.
//
//nolint:nilnil // The optional lookup reports absence without an error.
func FindBinding(ctx context.Context, installationID string, tokenID, actorID, repositoryID int64, kind string) (*ActorBinding, error) {
	binding := new(ActorBinding)
	has, err := db.GetEngine(ctx).Where("installation_id=? AND token_id=? AND actor_id=? AND repository_id=? AND kind=?",
		installationID, tokenID, actorID, repositoryID, kind).NoAutoCondition().Get(binding)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, nil
	}
	return binding, nil
}

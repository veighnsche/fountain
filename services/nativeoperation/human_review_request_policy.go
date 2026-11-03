// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/organization"
	user_model "forgejo.org/models/user"
	issue_service "forgejo.org/services/issue"
)

var ErrHumanReviewRequestMissing = errors.New("selected review request subject is not visible")

// HumanReviewRequestSelection retains native subjects only inside the host.
// Preparation and conditional SQL use the same defining native policy.
type HumanReviewRequestSelection struct {
	User         *user_model.User
	Team         *organization.Team
	Requests     sdk.HumanReviewRequestEvidence
	AddAction    sdk.RepositoryActionCapability
	RemoveAction sdk.RepositoryActionCapability
}

func ReadHumanReviewRequestSelection(ctx context.Context, actor *user_model.User, issue *issues_model.Issue, kind string, id int64) (*HumanReviewRequestSelection, error) {
	if actor == nil || issue == nil || !issue.IsPull || id <= 0 || kind != "user" && kind != "team" {
		return nil, ErrHumanReviewRequestMissing
	}
	if err := issue.LoadRepo(ctx); err != nil {
		return nil, err
	}
	if err := issue.LoadPullRequest(ctx); err != nil {
		return nil, err
	}
	s := &HumanReviewRequestSelection{AddAction: sdk.RepositoryActionCapability{State: "allowed", Reason: "review_request_add"}, RemoveAction: sdk.RepositoryActionCapability{State: "allowed", Reason: "review_request_remove"}}
	var valid func(bool) error
	var latest *issues_model.Review
	var err error
	if kind == "user" {
		s.User, err = user_model.GetUserByID(ctx, id)
		if user_model.IsErrUserNotExist(err) {
			return nil, ErrHumanReviewRequestMissing
		}
		if err != nil {
			return nil, err
		}
		valid = func(add bool) error { return issue_service.IsValidReviewRequest(ctx, s.User, actor, add, issue) }
		latest, err = issues_model.GetReviewByIssueIDAndUserID(ctx, issue.ID, id)
		if err != nil && !issues_model.IsErrReviewNotExist(err) {
			return nil, err
		}
	} else {
		s.Team, err = organization.GetTeamByID(ctx, id)
		if organization.IsErrTeamNotExist(err) {
			return nil, ErrHumanReviewRequestMissing
		}
		if err != nil {
			return nil, err
		}
		if err := issue.Repo.LoadOwner(ctx); err != nil {
			return nil, err
		}
		if !issue.Repo.Owner.IsOrganization() || s.Team.OrgID != issue.Repo.OwnerID {
			return nil, ErrHumanReviewRequestMissing
		}
		valid = func(add bool) error { return issue_service.IsValidTeamReviewRequest(ctx, s.Team, actor, add, issue) }
	}
	for _, action := range []struct {
		add  bool
		fact *sdk.RepositoryActionCapability
	}{{true, &s.AddAction}, {false, &s.RemoveAction}} {
		if err := valid(action.add); err != nil {
			if !issues_model.IsErrNotValidReviewRequest(err) {
				return nil, err
			}
			*action.fact = sdk.RepositoryActionCapability{State: "denied", Reason: "review_request_policy"}
		}
	}
	if kind == "user" && latest != nil && latest.Type != issues_model.ReviewTypeRequest && (issue.IsClosed || issue.PullRequest.HasMerged) {
		s.AddAction = sdk.RepositoryActionCapability{State: "denied", Reason: "pull_closed"}
	}
	if issue.Repo.IsArchived {
		s.AddAction = sdk.RepositoryActionCapability{State: "denied", Reason: "repository_archived"}
		s.RemoveAction = s.AddAction
	}
	s.Requests, err = ReadHumanReviewRequestEvidence(ctx, issue.ID, kind, id)
	if err != nil {
		return nil, err
	}
	return s, nil
}

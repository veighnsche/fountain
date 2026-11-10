// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package org

import (
	"context"
	"fmt"

	org_model "forgejo.org/models/organization"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/mailer"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

// CreateTeamInvite make a persistent invite in db and mail it
func CreateTeamInvite(ctx context.Context, inviter *user_model.User, team *org_model.Team, uname string) error {
	// One authority writer owns the invite before its effects. The mailed
	// notice is not a native authority effect and stays outside the claim.
	var invite *org_model.TeamInvite
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("team/%d/invite", team.ID), 0, nativeoperation_service.AuthorityTarget{Operation: "team/invite", ID: team.ID}, func(ctx context.Context) error {
		created, err := org_model.CreateTeamInvite(ctx, inviter, team, uname)
		if err != nil {
			return err
		}
		invite = created
		return nil
	}); err != nil {
		return err
	}

	return mailer.MailTeamInvite(ctx, inviter, team, invite)
}

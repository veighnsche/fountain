// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package user

import (
	"context"
	"fmt"

	"forgejo.org/models"
	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	password_module "forgejo.org/modules/auth/password"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/structs"
	"forgejo.org/services/mailer"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

type UpdateOptions struct {
	KeepEmailPrivate             optional.Option[bool]
	FullName                     optional.Option[string]
	Website                      optional.Option[string]
	Location                     optional.Option[string]
	Description                  optional.Option[string]
	Pronouns                     optional.Option[string]
	AllowGitHook                 optional.Option[bool]
	AllowImportLocal             optional.Option[bool]
	MaxRepoCreation              optional.Option[int]
	IsRestricted                 optional.Option[bool]
	Visibility                   optional.Option[structs.VisibleType]
	KeepActivityPrivate          optional.Option[bool]
	Language                     optional.Option[string]
	Theme                        optional.Option[string]
	DiffViewStyle                optional.Option[string]
	AllowCreateOrganization      optional.Option[bool]
	IsActive                     optional.Option[bool]
	IsAdmin                      optional.Option[bool]
	EmailNotificationsPreference optional.Option[string]
	SetLastLogin                 bool
	RepoAdminChangeTeamAccess    optional.Option[bool]
	EnableRepoUnitHints          optional.Option[bool]
	KeepPronounsPrivate          optional.Option[bool]
}

type userUpdatePlan struct {
	columns []string
	steps   []func(context.Context, *user_model.User) error
}

func (p *userUpdatePlan) add(column string, step func(context.Context, *user_model.User) error) {
	p.columns = append(p.columns, column)
	p.steps = append(p.steps, step)
}

func planUserField[T any](plan *userUpdatePlan, column string, option optional.Option[T], field func(*user_model.User) *T) {
	if has, value := option.Get(); has {
		plan.add(column, func(_ context.Context, user *user_model.User) error {
			*field(user) = value
			return nil
		})
	}
}

func planUserUpdate(opts *UpdateOptions) userUpdatePlan {
	plan := userUpdatePlan{}
	planUserField(&plan, "keep_email_private", opts.KeepEmailPrivate, func(u *user_model.User) *bool { return &u.KeepEmailPrivate })
	planUserField(&plan, "full_name", opts.FullName, func(u *user_model.User) *string { return &u.FullName })
	planUserField(&plan, "pronouns", opts.Pronouns, func(u *user_model.User) *string { return &u.Pronouns })
	planUserField(&plan, "website", opts.Website, func(u *user_model.User) *string { return &u.Website })
	planUserField(&plan, "location", opts.Location, func(u *user_model.User) *string { return &u.Location })
	planUserField(&plan, "description", opts.Description, func(u *user_model.User) *string { return &u.Description })
	planUserField(&plan, "language", opts.Language, func(u *user_model.User) *string { return &u.Language })
	planUserField(&plan, "theme", opts.Theme, func(u *user_model.User) *string { return &u.Theme })
	planUserField(&plan, "diff_view_style", opts.DiffViewStyle, func(u *user_model.User) *string { return &u.DiffViewStyle })
	planUserField(&plan, "enable_repo_unit_hints", opts.EnableRepoUnitHints, func(u *user_model.User) *bool { return &u.EnableRepoUnitHints })
	planUserField(&plan, "keep_pronouns_private", opts.KeepPronounsPrivate, func(u *user_model.User) *bool { return &u.KeepPronounsPrivate })
	planUserField(&plan, "allow_git_hook", opts.AllowGitHook, func(u *user_model.User) *bool { return &u.AllowGitHook })
	planUserField(&plan, "allow_import_local", opts.AllowImportLocal, func(u *user_model.User) *bool { return &u.AllowImportLocal })
	planUserField(&plan, "max_repo_creation", opts.MaxRepoCreation, func(u *user_model.User) *int { return &u.MaxRepoCreation })
	planUserField(&plan, "is_active", opts.IsActive, func(u *user_model.User) *bool { return &u.IsActive })
	planUserField(&plan, "is_restricted", opts.IsRestricted, func(u *user_model.User) *bool { return &u.IsRestricted })
	if has, value := opts.IsAdmin.Get(); has {
		plan.add("is_admin", func(ctx context.Context, u *user_model.User) error {
			if !value && user_model.IsLastAdminUser(ctx, u) {
				return models.ErrDeleteLastAdminUser{UID: u.ID}
			}
			u.IsAdmin = value
			return nil
		})
	}
	if has, value := opts.Visibility.Get(); has {
		plan.add("visibility", func(_ context.Context, u *user_model.User) error {
			if !u.IsOrganization() && !setting.Service.AllowedUserVisibilityModesSlice.IsAllowedVisibility(value) {
				return fmt.Errorf("visibility mode not allowed: %s", value.String())
			}
			u.Visibility = value
			return nil
		})
	}
	planUserField(&plan, "keep_activity_private", opts.KeepActivityPrivate, func(u *user_model.User) *bool { return &u.KeepActivityPrivate })
	planUserField(&plan, "allow_create_organization", opts.AllowCreateOrganization, func(u *user_model.User) *bool { return &u.AllowCreateOrganization })
	planUserField(&plan, "repo_admin_change_team_access", opts.RepoAdminChangeTeamAccess, func(u *user_model.User) *bool { return &u.RepoAdminChangeTeamAccess })
	planUserField(&plan, "email_notifications_preference", opts.EmailNotificationsPreference, func(u *user_model.User) *string { return &u.EmailNotificationsPreference })
	if opts.SetLastLogin {
		plan.add("last_login_unix", func(_ context.Context, u *user_model.User) error {
			u.SetLastLogin()
			return nil
		})
	}
	return plan
}

func UpdateUser(ctx context.Context, u *user_model.User, opts *UpdateOptions) error {
	plan := planUserUpdate(opts)
	if len(plan.columns) == 0 {
		return nil
	}
	apply := func(ctx context.Context) error {
		for _, step := range plan.steps {
			if err := step(ctx, u); err != nil {
				return err
			}
		}
		return user_model.UpdateUserCols(ctx, u, plan.columns...)
	}
	if !user_model.UserColumnsRequireAuthority(plan.columns...) {
		return apply(ctx)
	}
	return nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("user/%d/update", u.ID), 0, nativeoperation_service.AuthorityTarget{Operation: "user/update", ID: u.ID}, apply)
}

type UpdateAuthOptions struct {
	LoginSource        optional.Option[int64]
	LoginName          optional.Option[string]
	Password           optional.Option[string]
	MustChangePassword optional.Option[bool]
	ProhibitLogin      optional.Option[bool]
}

func UpdateAuth(ctx context.Context, u *user_model.User, opts *UpdateAuthOptions) error {
	// One authority writer owns the credential change before its effects.
	// The password-change notice below is not a native authority effect
	// and stays outside the claim.
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("user/%d/auth", u.ID), 0, nativeoperation_service.AuthorityTarget{Operation: "user/auth", ID: u.ID}, func(ctx context.Context) error {
		return doUpdateAuth(ctx, u, opts)
	}); err != nil {
		return err
	}

	if opts.Password.Has() {
		return mailer.SendPasswordChange(u)
	}

	return nil
}

func doUpdateAuth(ctx context.Context, u *user_model.User, opts *UpdateAuthOptions) error {
	if has, value := opts.LoginSource.Get(); has {
		source, err := auth_model.GetSourceByID(ctx, value)
		if err != nil {
			return err
		}

		u.LoginType = source.Type
		u.LoginSource = source.ID
	}
	if has, value := opts.LoginName.Get(); has {
		u.LoginName = value
	}

	if has, value := opts.Password.Get(); has && (u.IsLocal() || u.IsOAuth2()) {
		password := value

		if len(password) < setting.MinPasswordLength {
			return password_module.ErrMinLength
		}
		if !password_module.IsComplexEnough(password) {
			return password_module.ErrComplexity
		}
		if err := password_module.IsPwned(ctx, password); err != nil {
			return err
		}

		if err := u.SetPassword(password); err != nil {
			return err
		}
	}

	if has, value := opts.MustChangePassword.Get(); has {
		u.MustChangePassword = value
	}
	if has, value := opts.ProhibitLogin.Get(); has {
		u.ProhibitLogin = value
	}

	return user_model.UpdateUserCols(ctx, u, "login_type", "login_source", "login_name", "passwd", "passwd_hash_algo", "salt", "must_change_password", "prohibit_login")
}

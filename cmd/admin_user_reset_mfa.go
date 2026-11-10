// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"fmt"

	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	nativeoperation_service "forgejo.org/services/nativeoperation"

	"github.com/urfave/cli/v3"
)

func microcmdUserResetMFA() *cli.Command {
	return &cli.Command{
		Name:   "reset-mfa",
		Usage:  "Remove all two-factor authentication configurations for a user",
		Before: noDanglingArgs,
		Action: runResetMFA,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "username",
				Aliases: []string{"u"},
				Value:   "",
				Usage:   "The user to update",
			},
		},
	}
}

func runResetMFA(ctx context.Context, c *cli.Command) error {
	if err := argsSet(c, "username"); err != nil {
		return err
	}

	ctx, cancel := installSignals(ctx)
	defer cancel()

	if err := initDB(ctx); err != nil {
		return err
	}

	user, err := user_model.GetUserByName(ctx, c.String("username"))
	if err != nil {
		return err
	}

	// One authority writer owns the credential reset before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("user/%d/credentials/reset", user.ID), 0, nativeoperation_service.AuthorityTarget{Operation: "user/credentials/reset", ID: user.ID}, func(ctx context.Context) error {
		webAuthnList, err := auth_model.GetWebAuthnCredentialsByUID(ctx, user.ID)
		if err != nil {
			return err
		}

		for _, credential := range webAuthnList {
			if _, err := auth_model.DeleteCredential(ctx, credential.ID, user.ID); err != nil {
				return err
			}
		}

		tfaModes, err := auth_model.GetTwoFactorByUID(ctx, user.ID)
		if err == nil && tfaModes != nil {
			if err := auth_model.DeleteTwoFactorByID(ctx, tfaModes.ID, user.ID); err != nil {
				return err
			}
		} else {
			if _, is := err.(auth_model.ErrTwoFactorNotEnrolled); !is {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("%s's two-factor authentication settings have been removed!\n", user.Name)
	return nil
}

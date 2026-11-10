// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"

	user_model "forgejo.org/models/user"
	nativeoperation_service "forgejo.org/services/nativeoperation"

	"github.com/urfave/cli/v3"
)

func microcmdUserMustChangePassword() *cli.Command {
	return &cli.Command{
		Name:   "must-change-password",
		Usage:  "Set the must change password flag for the provided users or all users",
		Action: runMustChangePassword,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "all",
				Aliases: []string{"A"},
				Usage:   "All users must change password, except those explicitly excluded with --exclude",
			},
			&cli.StringSliceFlag{
				Name:    "exclude",
				Aliases: []string{"e"},
				Usage:   "Do not change the must-change-password flag for these users",
			},
			&cli.BoolFlag{
				Name:  "unset",
				Usage: "Instead of setting the must-change-password flag, unset it",
			},
		},
	}
}

func runMustChangePassword(ctx context.Context, c *cli.Command) error {
	ctx, cancel := installSignals(ctx)
	defer cancel()

	if c.NArg() == 0 && !c.IsSet("all") {
		return errors.New("either usernames or --all must be provided")
	}

	mustChangePassword := !c.Bool("unset")
	all := c.Bool("all")
	exclude := c.StringSlice("exclude")

	if err := initDB(ctx); err != nil {
		return err
	}

	// One authority writer owns the credential-policy change before its
	// effects, advancing the native revision so old permission
	// observations go stale.
	var n int64
	if err := nativeoperation_service.WithAuthorityOwnershipFor(ctx, "users/must-change-password", 0, nativeoperation_service.AuthorityTarget{}, func(ctx context.Context) error {
		updated, err := user_model.SetMustChangePassword(ctx, all, mustChangePassword, c.Args().Slice(), exclude)
		if err != nil {
			return err
		}
		n = updated
		return nil
	}); err != nil {
		return err
	}

	fmt.Printf("Updated %d users setting MustChangePassword to %t\n", n, mustChangePassword)
	return nil
}

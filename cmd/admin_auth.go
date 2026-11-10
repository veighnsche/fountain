// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	auth_service "forgejo.org/services/auth"
	nativeoperation_service "forgejo.org/services/nativeoperation"

	"github.com/urfave/cli/v3"
)

type (
	authService struct {
		initDB            func(ctx context.Context) error
		createAuthSource  func(context.Context, *auth_model.Source) error
		updateAuthSource  func(context.Context, *auth_model.Source) error
		getAuthSourceByID func(ctx context.Context, id int64) (*auth_model.Source, error)
	}
)

func microcmdAuthDelete() *cli.Command {
	return &cli.Command{
		Name:   "delete",
		Usage:  "Delete specific auth source",
		Flags:  []cli.Flag{idFlag()},
		Before: noDanglingArgs,
		Action: runDeleteAuth,
	}
}

func microcmdAuthList() *cli.Command {
	return &cli.Command{
		Name:   "list",
		Usage:  "List auth sources",
		Before: noDanglingArgs,
		Action: runListAuth,
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:  "min-width",
				Usage: "Minimal cell width including any padding for the formatted table",
				Value: 0,
			},
			&cli.IntFlag{
				Name:  "tab-width",
				Usage: "width of tab characters in formatted table (equivalent number of spaces)",
				Value: 8,
			},
			&cli.IntFlag{
				Name:  "padding",
				Usage: "padding added to a cell before computing its width",
				Value: 1,
			},
			&cli.StringFlag{
				Name:  "pad-char",
				Usage: `ASCII char used for padding if padchar == '\\t', the Writer will assume that the width of a '\\t' in the formatted output is tabwidth, and cells are left-aligned independent of align_left (for correct-looking results, tabwidth must correspond to the tab width in the viewer displaying the result)`,
				Value: "\t",
			},
			&cli.BoolFlag{
				Name:  "vertical-bars",
				Usage: "Set to true to print vertical bars between columns",
			},
		},
	}
}

// newAuthService creates a service with default functions. The default
// create/update functions claim one authority writer before their effects,
// advancing the native revision so old permission observations go stale.
func newAuthService() *authService {
	return &authService{
		initDB: initDB,
		createAuthSource: func(ctx context.Context, source *auth_model.Source) error {
			return nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("auth-source/%s", source.Name), 0, nativeoperation_service.AuthorityTarget{}, func(ctx context.Context) error {
				return auth_model.CreateSource(ctx, source)
			})
		},
		updateAuthSource: func(ctx context.Context, source *auth_model.Source) error {
			return nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("auth-source/%d", source.ID), 0, nativeoperation_service.AuthorityTarget{Operation: "auth-source", ID: source.ID}, func(ctx context.Context) error {
				return auth_model.UpdateSource(ctx, source)
			})
		},
		getAuthSourceByID: auth_model.GetSourceByID,
	}
}

func runListAuth(ctx context.Context, c *cli.Command) error {
	ctx, cancel := installSignals(ctx)
	defer cancel()

	if err := initDB(ctx); err != nil {
		return err
	}

	authSources, err := db.Find[auth_model.Source](ctx, auth_model.FindSourcesOptions{})
	if err != nil {
		return err
	}

	flags := tabwriter.AlignRight
	if c.Bool("vertical-bars") {
		flags |= tabwriter.Debug
	}

	padChar := byte('\t')
	if len(c.String("pad-char")) > 0 {
		padChar = c.String("pad-char")[0]
	}

	// loop through each source and print
	w := tabwriter.NewWriter(os.Stdout, c.Int("min-width"), c.Int("tab-width"), c.Int("padding"), padChar, flags)
	fmt.Fprint(w, "ID\tName\tType\tEnabled\n")
	for _, source := range authSources {
		fmt.Fprintf(w, "%d\t%s\t%s\t%t\n", source.ID, source.Name, source.Type.String(), source.IsActive)
	}
	w.Flush()

	return nil
}

func runDeleteAuth(ctx context.Context, c *cli.Command) error {
	if !c.IsSet("id") {
		return errors.New("--id flag is missing")
	}

	ctx, cancel := installSignals(ctx)
	defer cancel()

	if err := initDB(ctx); err != nil {
		return err
	}

	source, err := auth_model.GetSourceByID(ctx, c.Int64("id"))
	if err != nil {
		return err
	}

	// One authority writer owns the auth-source deletion before its
	// effects, advancing the native revision so old permission
	// observations go stale.
	return nativeoperation_service.WithAuthorityOwnershipFor(ctx, fmt.Sprintf("auth-source/%d", source.ID), 0, nativeoperation_service.AuthorityTarget{Operation: "auth-source", ID: source.ID}, func(ctx context.Context) error {
		return auth_service.DeleteSource(ctx, source)
	})
}

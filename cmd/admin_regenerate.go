// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"

	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/modules/graceful"
	nativeoperation_service "forgejo.org/services/nativeoperation"
	repo_service "forgejo.org/services/repository"

	"github.com/urfave/cli/v3"
)

var (
	microcmdRegenHooks = &cli.Command{
		Name:   "hooks",
		Usage:  "Regenerate git-hooks",
		Before: noDanglingArgs,
		Action: runRegenerateHooks,
	}

	microcmdRegenKeys = &cli.Command{
		Name:   "keys",
		Usage:  "Regenerate authorized_keys file",
		Before: noDanglingArgs,
		Action: runRegenerateKeys,
	}
)

func runRegenerateHooks(ctx context.Context, c *cli.Command) error {
	ctx, cancel := installSignals(ctx)
	defer cancel()

	if err := initDB(ctx); err != nil {
		return err
	}
	// One authority writer owns the regeneration before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	return nativeoperation_service.WithAuthorityOwnershipFor(ctx, "admin/regenerate-hooks", 0, nativeoperation_service.AuthorityTarget{}, func(ctx context.Context) error {
		return repo_service.SyncRepositoryHooks(graceful.GetManager().ShutdownContext())
	})
}

func runRegenerateKeys(ctx context.Context, c *cli.Command) error {
	ctx, cancel := installSignals(ctx)
	defer cancel()

	if err := initDB(ctx); err != nil {
		return err
	}
	// One authority writer owns the regeneration before its effects,
	// advancing the native revision so old permission observations go
	// stale.
	return nativeoperation_service.WithAuthorityOwnershipFor(ctx, "admin/regenerate-keys", 0, nativeoperation_service.AuthorityTarget{}, asymkey_model.RewriteAllPublicKeys)
}

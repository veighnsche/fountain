// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"fmt"
	"path/filepath"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"
)

func withAuthorityOwnership(ctx context.Context, repoID, userID int64, fn func(context.Context) error) error {
	if execcontext.FromContext(ctx) != nil {
		return fn(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	resource := fmt.Sprintf("repo/%d/collaborator/%d", repoID, userID)
	scope, err := execcontext.EncodeScope(execcontext.Scope{
		Kind: execcontext.OwnerOrdinary, RepositoryID: repoID,
		Family: execcontext.FamilyAuthority, AuthorityOp: "repo/collaborator", AuthorityID: repoID, AuthorityID2: userID,
	})
	if err != nil {
		return err
	}
	return execcontext.RunOrdinary(ctx, nativeoperation_model.OrdinaryStore{}, filepath.Join(setting.AppDataPath, "nativeop-exec"), execcontext.FamilyAuthority, resource, scope, fn)
}

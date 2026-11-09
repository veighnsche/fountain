// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	access_model "forgejo.org/models/perm/access"
	"forgejo.org/models/unit"
	"forgejo.org/services/context"
	repository_service "forgejo.org/services/repository"
)

// ReadDesktopSource shares the native source owner with the host callback. The
// authenticated enrollment fixes account/repository; selectors cannot grant
// code read, dispatch, publication or permission to act as another writer.
func ReadDesktopSource(ctx *context.APIContext) {
	current := func() bool {
		grant, _, ok := currentDesktopGrant(ctx)
		if !ok {
			return false
		}
		permission, err := access_model.GetUserRepoPermission(ctx, ctx.Repo().Repository, ctx.Doer())
		if grant.Revoked || err != nil || !permission.CanRead(unit.TypeCode) || !ctx.Doer().IsAccessAllowed(ctx) {
			ctx.Error(http.StatusForbidden, "desktop source", errors.New("current code-read enrollment required"))
			return false
		}
		return true
	}
	if !current() {
		return
	}
	var selection struct {
		Commit string   `json:"source_commit"`
		Paths  []string `json:"source_paths"`
		Actors []string `json:"source_actors"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Resp, ctx.Req.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&selection) != nil || decoder.Decode(&trailing) != io.EOF {
		ctx.Error(http.StatusBadRequest, "desktop source", errors.New("bounded exact source selection required"))
		return
	}
	request := extension.CallbackRequest{RepositoryID: strconv.FormatInt(ctx.Repo().Repository.ID, 10), SourceCommit: selection.Commit, SourcePaths: selection.Paths, SourceActors: selection.Actors}
	source, status := repository_service.ReadRepositorySource(ctx, request)
	if status != http.StatusOK {
		ctx.Error(status, "desktop source", errors.New("native source unavailable"))
		return
	}
	if !current() {
		return
	}
	ctx.JSON(http.StatusOK, source)
}

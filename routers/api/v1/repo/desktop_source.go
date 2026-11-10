// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/context"
	native_service "forgejo.org/services/nativeoperation"
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

// ReadDesktopSnapshot uses the ordinary OAuth/enrollment identity and the
// existing native read owner. It never impersonates an extension callback or
// acquires an operation reservation, execution claim or publication grant.
func ReadDesktopSnapshot(ctx *context.APIContext) {
	readDesktopSnapshot(ctx, false)
}

func ReadDesktopIssueSnapshot(ctx *context.APIContext) {
	readDesktopSnapshot(ctx, true)
}

func readDesktopSnapshot(ctx *context.APIContext, issuesOnly bool) {
	current := func() bool {
		grant, _, ok := currentDesktopGrant(ctx)
		if !ok {
			return false
		}
		actor, actorErr := user_model.GetUserByID(ctx, ctx.Doer().ID)
		repository, repoErr := repo_model.GetRepositoryByID(ctx, ctx.Repo().Repository.ID)
		if grant.Revoked || actorErr != nil || repoErr != nil || !actor.IsAccessAllowed(ctx) {
			ctx.Error(http.StatusForbidden, "desktop snapshot", errors.New("current read enrollment required"))
			return false
		}
		permission, err := access_model.GetUserRepoPermission(ctx, repository, actor)
		kind := unit.TypeCode
		if issuesOnly {
			kind = unit.TypeIssues
		}
		if err != nil || !permission.CanRead(kind) || issuesOnly != (grant.Kind == "issues") {
			ctx.Error(http.StatusForbidden, "desktop snapshot", errors.New("current repository read required"))
			return false
		}
		return true
	}
	if !current() {
		return
	}
	var request extension.SnapshotRequest
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Resp, ctx.Req.Body, 64<<10))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&request) != nil || decoder.Decode(&trailing) != io.EOF || extension.ValidateSnapshotRequest(request) != nil ||
		request.RepositoryID != strconv.FormatInt(ctx.Repo().Repository.ID, 10) || request.ActorID != strconv.FormatInt(ctx.Doer().ID, 10) {
		ctx.Error(http.StatusBadRequest, "desktop snapshot", errors.New("bounded exact repository/account selectors required"))
		return
	}
	if issuesOnly {
		for _, family := range request.Families {
			if family != extension.SnapshotFamilyIssue && family != extension.SnapshotFamilyComments && family != extension.SnapshotFamilyDependencies && family != extension.SnapshotFamilyRepositoryIssues {
				ctx.Error(http.StatusForbidden, "desktop snapshot", errors.New("issue-only enrollment refuses code, PR and execution facts"))
				return
			}
		if request.PullNumber != "" {
			ctx.Error(http.StatusForbidden, "desktop snapshot", errors.New("issue-only enrollment refuses PR selectors"))
			return
		}
		if request.IssueIndex != "" {
			index, _ := strconv.ParseInt(request.IssueIndex,10,64)
			issue, err := issues_model.GetIssueByIndex(ctx,ctx.Repo().Repository.ID,index)
			if err != nil || issue.IsPull {
				ctx.Error(http.StatusForbidden,"desktop snapshot",errors.New("plain issue selection required"))
				return
			}
		} else if len(request.CommentIDs) != 0 {
			ctx.Error(http.StatusForbidden,"desktop snapshot",errors.New("issue comments require their exact plain issue"))
			return
		}
	}
	hasScope, scope := ctx.Authentication().Scope().Get()
	publicOnly, scopeErr := scope.PublicOnly()
	decision := extensionauth.SnapshotReadDecision{SubmissionDecision: extensionauth.SubmissionDecision{ActorID: ctx.Doer().ID}, IssuesOnly: issuesOnly, RepositoryID: ctx.Repo().Repository.ID, Scope: scope, PublicOnly: publicOnly, RepositoryIDs: []int64{ctx.Repo().Repository.ID}}
	if !hasScope || scopeErr != nil || !decision.CanReadFamilies(request.Families) {
		ctx.Error(http.StatusForbidden, "desktop snapshot", errors.New("explicit native read scopes required"))
		return
	}
	service := native_service.Default()
	before, err := service.Status(ctx)
	if err != nil || !before.Idle || before.Inhibited {
		ctx.Error(http.StatusConflict, "desktop snapshot", errors.New("native read domain unavailable"))
		return
	}
	snapshot, err := service.ReadSnapshot(ctx, decision, request)
	if err == nil && current() {
		var after extension.NativeSnapshot
		after, err = service.ReadSnapshot(ctx, decision, request)
		if err == nil && !reflect.DeepEqual(snapshot, after) {
			ctx.Error(http.StatusConflict, "desktop snapshot", errors.New("native snapshot changed"))
			return
		}
	} else if err == nil {
		return
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, native_service.ErrSnapshotNotFound) {
			status = http.StatusNotFound
		}
		if errors.Is(err, native_service.ErrSnapshotInvalid) {
			status = http.StatusBadRequest
		}
		ctx.Error(status, "desktop snapshot", errors.New("native snapshot unavailable"))
		return
	}
	after, err := service.Status(ctx)
	if err != nil || before.Revision != after.Revision || !after.Idle || after.Inhibited {
		ctx.Error(http.StatusConflict, "desktop snapshot", errors.New("native snapshot changed"))
		return
	}
	if !current() {
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"revision": after.Revision, "snapshot": snapshot})
}

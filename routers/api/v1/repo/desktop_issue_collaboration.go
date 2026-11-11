// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	stdcontext "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	extension "forgejo.org/extension-sdk"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/desktopgrant"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/services/context"
	native_service "forgejo.org/services/nativeoperation"
)

// The original OAuth authorization and profile proof own this namespace. The
// issue-only grant never creates an extension session or controller authority.
func desktopIssueAdmission(ctx *context.APIContext, write bool) (string, func(stdcontext.Context) error, bool) {
	g, app, ok := currentDesktopGrant(ctx)
	if !ok {
		return "", nil, false
	}
	actorID, repositoryID, oauthID, applicationID, grantID := ctx.Doer().ID, ctx.Repo().Repository.ID, g.OAuthGrantID, app.ID, g.ID
	admit := func(tx stdcontext.Context) error {
		if tx.Err() != nil || ctx.Req.Context().Err() != nil {
			return native_service.ErrAuthorityLost
		}
		current, err := desktopgrant.Get(tx, grantID)
		if err != nil || current.Kind != "issues" || current.ActorID != actorID || current.RepositoryID != repositoryID || current.OAuthGrantID != oauthID || current.ProofSHA256 == "" || current.ProofSHA256 != g.ProofSHA256 || current.RuntimeID != g.RuntimeID || write && current.Revoked {
			return native_service.ErrAuthorityLost
		}
		oauth, err := auth_model.GetOAuth2GrantByID(tx, oauthID)
		if err != nil || oauth == nil || oauth.UserID != actorID || oauth.ApplicationID != applicationID {
			return native_service.ErrAuthorityLost
		}
		scope := auth_model.AccessTokenScope(strings.ReplaceAll(oauth.Scope, " ", ","))
		required := auth_model.AccessTokenScopeReadIssue
		if write {
			required = auth_model.AccessTokenScopeWriteIssue
		}
		allowed, err := scope.HasScope(required)
		if err != nil || !allowed {
			return native_service.ErrAuthorityLost
		}
		actor, err := user_model.GetUserByID(tx, actorID)
		if err != nil || !actor.IsAccessAllowed(tx) {
			return native_service.ErrAuthorityLost
		}
		repository, err := repo_model.GetRepositoryByID(tx, repositoryID)
		if err != nil || !repository.UnitEnabled(tx, unit.TypeIssues) || unit.TypeIssues.UnitGlobalDisabled() {
			return native_service.ErrAuthorityLost
		}
		permission, err := access_model.GetUserRepoPermission(tx, repository, actor)
		if err != nil || !permission.CanRead(unit.TypeIssues) {
			return native_service.ErrAuthorityLost
		}
		return nil
	}
	if err := admit(ctx); err != nil {
		desktopIssueError(ctx, err)
		return "", nil, false
	}
	return desktopgrant.DesktopInstallation(grantID), admit, true
}

func desktopIssueDecode(ctx *context.APIContext, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Resp, ctx.Req.Body, 2<<20))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(target) != nil || decoder.Decode(&trailing) != io.EOF {
		ctx.Error(http.StatusBadRequest, "desktop issue", errors.New("bounded issue command required"))
		return false
	}
	return true
}

func desktopIssueError(ctx *context.APIContext, err error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, native_service.ErrAuthorityLost):
		status = http.StatusForbidden
	case errors.Is(err, native_service.ErrIntentConflict):
		status = http.StatusConflict
	case errors.Is(err, native_service.ErrInvalidIntent):
		status = http.StatusBadRequest
	}
	ctx.Error(status, "desktop issue", errors.New("issue operation unavailable or changed"))
}

// Preparation brackets current facts with the existing native writer revision;
// the primary operation still independently checks the exact snapshot and actor.
func PrepareDesktopIssueCommand(ctx *context.APIContext) {
	installation, admit, ok := desktopIssueAdmission(ctx, false)
	if !ok {
		return
	}
	// Withdrawn enrollments retain journal lookup, not new preparation facts.
	// Recheck at each read boundary as well as before returning the proposal.
	admitted := admit
	admit = func(readContext stdcontext.Context) error {
		if err := admitted(readContext); err != nil {
			return err
		}
		grant, err := desktopgrant.Get(readContext, ctx.Params(":grant"))
		if err != nil || grant.Revoked {
			return native_service.ErrAuthorityLost
		}
		return nil
	}
	if err := admit(ctx); err != nil {
		desktopIssueError(ctx, err)
		return
	}
	var q struct {
		Kind               string  `json:"kind"`
		Number             int64   `json:"number"`
		Command            string  `json:"command_id,omitempty"`
		NotAfter           int64   `json:"not_after,omitempty"`
		Title              *string `json:"title,omitempty"`
		Body               *string `json:"body,omitempty"`
		Close              bool    `json:"close,omitempty"`
		Lock               bool    `json:"lock,omitempty"`
		Reason             string  `json:"reason,omitempty"`
		OriginalTitle      *string `json:"original_title,omitempty"`
		OriginalBodyDigest *string `json:"original_body_sha256,omitempty"`
	}
	if !desktopIssueDecode(ctx, &q) {
		return
	}
	if q.Number < 0 || q.Kind == "create" && q.Number != 0 || q.Kind != "create" && q.Kind != "capabilities" && q.Number == 0 ||
		q.Kind != "create" && q.Kind != "comment" && q.Kind != "edit" && q.Kind != "state" && q.Kind != "lock" && q.Kind != "capabilities" {
		desktopIssueError(ctx, native_service.ErrInvalidIntent)
		return
	}
	service := native_service.Default()
	read := func(readContext stdcontext.Context, revision int64) (any, error) {
		if err := admit(readContext); err != nil {
			return nil, err
		}
		actor, err := user_model.GetUserByID(readContext, ctx.Doer().ID)
		if err != nil {
			return nil, err
		}
		repository, err := repo_model.GetRepositoryByID(readContext, ctx.Repo().Repository.ID)
		if err != nil {
			return nil, err
		}
		permission, err := access_model.GetUserRepoPermission(readContext, repository, actor)
		if err != nil {
			return nil, err
		}
		fact := func(allowed bool) extension.RepositoryActionCapability {
			if allowed && !repository.IsArchived {
				return extension.RepositoryActionCapability{State: "allowed", Reason: "current_issue_permission"}
			}
			return extension.RepositoryActionCapability{State: "denied", Reason: "issue_action_unavailable"}
		}
		repoID, actorID := strconv.FormatInt(repository.ID, 10), strconv.FormatInt(actor.ID, 10)
		capabilities := map[string]any{
			"repository_id": repoID, "actor_id": actorID, "archived": repository.IsArchived,
			"can_create_issues": !repository.IsArchived && !user_model.IsBlocked(readContext, repository.OwnerID, actor.ID), "writer": permission.CanWriteIssuesOrPulls(false),
		}
		if q.Kind == "capabilities" && q.Number == 0 {
			return capabilities, nil
		}
		if q.Kind == "create" {
			return extension.RepositoryIssueCreateSnapshot{RepositoryID: repoID, ActorID: actorID, NativeRevision: revision, Action: fact(!user_model.IsBlocked(readContext, repository.OwnerID, actor.ID))}, nil
		}
		issue, err := issues_model.GetIssueByIndex(readContext, repository.ID, q.Number)
		if err != nil || issue.IsPull {
			return nil, native_service.ErrAuthorityLost
		}
		issueID, index := strconv.FormatInt(issue.ID, 10), strconv.FormatInt(issue.Index, 10)
		facts := issues_model.ReadDiscussionActionFacts(readContext, repository, issue, actor, permission)
		closeAction, reopenAction := facts.StateActions(false)
		if (q.Kind == "state" || q.Kind == "capabilities") && closeAction == issues_model.NativeActionAllowed {
			closeAction, err = issues_model.CheckNativeCloseDependencies(readContext, repository, issue)
			if err != nil {
				return nil, err
			}
		}
		switch q.Kind {
		case "capabilities":
			capabilities["issue_id"], capabilities["index"], capabilities["is_closed"], capabilities["is_locked"] = issueID, index, issue.IsClosed, issue.IsLocked
			capabilities["can_update"] = facts.TitleEdit() == issues_model.NativeActionAllowed && facts.BodyEdit() == issues_model.NativeActionAllowed
			capabilities["can_close"], capabilities["can_reopen"] = closeAction == issues_model.NativeActionAllowed, reopenAction == issues_model.NativeActionAllowed
			return capabilities, nil
		case "comment":
			return extension.RepositoryCommentSnapshot{RepositoryID: repoID, ActorID: actorID, Subject: "issue", SubjectID: issueID, IssueID: issueID, Index: index, NativeRevision: revision, Action: fact(facts.Comment() == issues_model.NativeActionAllowed)}, nil
		case "edit":
			digest := sha256.Sum256([]byte(issue.Content))
			return extension.RepositoryEditSnapshot{RepositoryID: repoID, ActorID: actorID, Subject: "issue", SubjectID: issueID, IssueID: issueID, Index: index, Title: issue.Title, BodyDigest: hex.EncodeToString(digest[:]), ContentVersion: int64(issue.ContentVersion), NativeRevision: revision, TitleAction: fact(facts.TitleEdit() == issues_model.NativeActionAllowed), BodyAction: fact(facts.BodyEdit() == issues_model.NativeActionAllowed)}, nil
		case "state":
			return extension.RepositoryStateSnapshot{RepositoryID: repoID, ActorID: actorID, Subject: "issue", SubjectID: issueID, IssueID: issueID, Index: index, IsClosed: issue.IsClosed, NativeRevision: revision, CloseAction: fact(closeAction == issues_model.NativeActionAllowed), ReopenAction: fact(reopenAction == issues_model.NativeActionAllowed)}, nil
		default:
			return extension.RepositoryLockSnapshot{RepositoryID: repoID, ActorID: actorID, Subject: "issue", SubjectID: issueID, IssueID: issueID, Index: index, IsLocked: issue.IsLocked, NativeRevision: revision, LockAction: fact(facts.LockPermission() == issues_model.NativeActionAllowed && !facts.Locked), UnlockAction: fact(facts.LockPermission() == issues_model.NativeActionAllowed && facts.Locked)}, nil
		}
	}
	first, _, err := native_service.ObserveStable(ctx, service.ReadNativeRevision, read,
		func(first, second any) bool { return reflect.DeepEqual(first, second) }, nil)
	if err != nil {
		var observation *native_service.ObservationError
		if errors.As(err, &observation) && observation.Stage == native_service.ObservationFirstRead {
			desktopIssueError(ctx, observation.Err)
		} else {
			desktopIssueError(ctx, native_service.ErrIntentConflict)
		}
		return
	}
	if admit(ctx) != nil {
		desktopIssueError(ctx, native_service.ErrIntentConflict)
		return
	}
	if q.Command == "" {
		ctx.JSON(http.StatusOK, first)
		return
	}
	command := desktopIssueCommand{Kind: q.Kind}
	switch p := first.(type) {
	case extension.RepositoryIssueCreateSnapshot:
		if q.Title == nil || q.Body == nil {
			desktopIssueError(ctx, native_service.ErrInvalidIntent)
			return
		}
		command.Create = &extension.HumanIssueCreateRequest{OperationID: q.Command, Expected: p, NotAfter: q.NotAfter, Title: *q.Title, Body: *q.Body}
	case extension.RepositoryCommentSnapshot:
		if q.Body == nil {
			desktopIssueError(ctx, native_service.ErrInvalidIntent)
			return
		}
		command.Comment = &extension.HumanCommentRequest{OperationID: q.Command, Expected: p, NotAfter: q.NotAfter, Body: *q.Body}
	case extension.RepositoryEditSnapshot:
		if q.OriginalTitle == nil || q.OriginalBodyDigest == nil || *q.OriginalTitle != p.Title || *q.OriginalBodyDigest != p.BodyDigest {
			desktopIssueError(ctx, native_service.ErrIntentConflict)
			return
		}
		command.Edit = &extension.HumanEditRequest{OperationID: q.Command, Expected: p, NotAfter: q.NotAfter, Title: q.Title, Body: q.Body}
	case extension.RepositoryStateSnapshot:
		command.State = &extension.HumanStateRequest{OperationID: q.Command, Expected: p, NotAfter: q.NotAfter, Close: q.Close}
	case extension.RepositoryLockSnapshot:
		command.Lock = &extension.HumanLockRequest{OperationID: q.Command, Expected: p, NotAfter: q.NotAfter, Lock: q.Lock, Reason: q.Reason}
	}
	_, digest, err := desktopIssueCanonical(command)
	if err != nil {
		desktopIssueError(ctx, native_service.ErrInvalidIntent)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"request": command, "intent_digest": digest, "installation_id": installation})
}

type desktopIssueCommand struct {
	Kind      string                             `json:"kind"`
	Operation string                             `json:"operation,omitempty"`
	Create    *extension.HumanIssueCreateRequest `json:"create,omitempty"`
	Comment   *extension.HumanCommentRequest     `json:"comment,omitempty"`
	Edit      *extension.HumanEditRequest        `json:"edit,omitempty"`
	State     *extension.HumanStateRequest       `json:"state,omitempty"`
	Lock      *extension.HumanLockRequest        `json:"lock,omitempty"`
}

func desktopIssueCanonical(q desktopIssueCommand) (string, string, error) {
	switch {
	case q.Kind == "create" && q.Create != nil:
		return extension.CanonicalHumanIssueCreateIntent(*q.Create)
	case q.Kind == "comment" && q.Comment != nil:
		return extension.CanonicalHumanCommentIntent(*q.Comment)
	case q.Kind == "edit" && q.Edit != nil:
		return extension.CanonicalHumanEditIntent(*q.Edit)
	case q.Kind == "state" && q.State != nil:
		return extension.CanonicalHumanStateIntent(*q.State)
	case q.Kind == "lock" && q.Lock != nil:
		return extension.CanonicalHumanLockIntent(*q.Lock)
	}
	return "", "", native_service.ErrInvalidIntent
}

func SubmitDesktopIssueCommand(ctx *context.APIContext) {
	var q desktopIssueCommand
	if !desktopIssueDecode(ctx, &q) {
		return
	}
	installation, admit, ok := desktopIssueAdmission(ctx, q.Kind != "cancel")
	if !ok {
		return
	}
	count := 0
	for _, present := range []bool{q.Create != nil, q.Comment != nil, q.Edit != nil, q.State != nil, q.Lock != nil} {
		if present {
			count++
		}
	}
	if q.Kind == "cancel" && count != 0 || q.Kind != "cancel" && (count != 1 || q.Operation != "") {
		desktopIssueError(ctx, native_service.ErrInvalidIntent)
		return
	}
	repository, actor := strconv.FormatInt(ctx.Repo().Repository.ID, 10), strconv.FormatInt(ctx.Doer().ID, 10)
	match := func(repo, user, subject string) bool {
		return repo == repository && user == actor && subject == "issue"
	}
	service := native_service.Default()
	var record extension.OperationRecord
	var err error
	switch {
	case q.Kind == "create" && q.Create != nil && match(q.Create.Expected.RepositoryID, q.Create.Expected.ActorID, "issue"):
		record, err = service.SubmitHumanIssueCreate(ctx, installation, *q.Create, admit)
	case q.Kind == "comment" && q.Comment != nil && match(q.Comment.Expected.RepositoryID, q.Comment.Expected.ActorID, q.Comment.Expected.Subject):
		record, err = service.SubmitHumanComment(ctx, installation, *q.Comment, admit)
	case q.Kind == "edit" && q.Edit != nil && match(q.Edit.Expected.RepositoryID, q.Edit.Expected.ActorID, q.Edit.Expected.Subject):
		record, err = service.SubmitHumanEdit(ctx, installation, *q.Edit, admit)
	case q.Kind == "state" && q.State != nil && match(q.State.Expected.RepositoryID, q.State.Expected.ActorID, q.State.Expected.Subject):
		record, err = service.SubmitHumanState(ctx, installation, *q.State, admit)
	case q.Kind == "lock" && q.Lock != nil && match(q.Lock.Expected.RepositoryID, q.Lock.Expected.ActorID, q.Lock.Expected.Subject):
		record, err = service.SubmitHumanLock(ctx, installation, *q.Lock, admit)
	case q.Kind == "cancel" && q.Operation != "" && len(q.Operation) <= 128:
		prior, lookupErr := service.Get(ctx, installation, q.Operation)
		if lookupErr != nil {
			desktopIssueError(ctx, lookupErr)
			return
		}
		if !desktopIssueRecord(prior, repository, actor) {
			desktopIssueError(ctx, native_service.ErrAuthorityLost)
			return
		}
		record, err = service.Cancel(ctx, installation, q.Operation)
	default:
		err = native_service.ErrInvalidIntent
	}
	if err != nil && !errors.Is(err, native_service.ErrCancelledBeforeSubmit) {
		desktopIssueError(ctx, err)
		return
	}
	if err = admit(ctx); err != nil {
		desktopIssueError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, record)
}

func desktopIssueRecord(lookup extension.OperationLookup, repository, actor string) bool {
	r := lookup.Record
	if r == nil || r.Kind == "" {
		return true
	}
	if r.RepositoryID != repository || r.ActorID != actor {
		return false
	}
	switch r.Kind {
	case extension.OperationKindHumanIssueCreate, extension.OperationKindHumanComment, extension.OperationKindHumanEdit, extension.OperationKindHumanState, extension.OperationKindHumanLock:
		return true
	}
	return false
}

func GetDesktopIssueCommand(ctx *context.APIContext) {
	installation, admit, ok := desktopIssueAdmission(ctx, false)
	if !ok {
		return
	}
	id := ctx.Params(":operation")
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\\x00\r\n") {
		desktopIssueError(ctx, native_service.ErrInvalidIntent)
		return
	}
	lookup, err := native_service.Default().Get(ctx, installation, id)
	if err != nil {
		desktopIssueError(ctx, err)
		return
	}
	if !desktopIssueRecord(lookup, strconv.FormatInt(ctx.Repo().Repository.ID, 10), strconv.FormatInt(ctx.Doer().ID, 10)) || admit(ctx) != nil {
		desktopIssueError(ctx, native_service.ErrAuthorityLost)
		return
	}
	ctx.JSON(http.StatusOK, lookup)
}

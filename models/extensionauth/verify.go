// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package extensionauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"slices"

	sdk "forgejo.org/extension-sdk"
	auth_model "forgejo.org/models/auth"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"

	"github.com/google/uuid"
)

// Bounded refusal codes for background submission. They identify the failed
// check without exposing secrets or native internals.
const (
	RefusalInvalidIntent      = "invalid_intent"
	RefusalUnknownKind        = "unknown_kind"
	RefusalInvalidCredential  = "invalid_credential"
	RefusalActorMismatch      = "actor_mismatch"
	RefusalBindingMissing     = "binding_missing"
	RefusalInsufficientScope  = "insufficient_scope"
	RefusalRepositoryExcluded = "repository_excluded"
	RefusalAccountUnavailable = "account_unavailable"
	RefusalPermissionLost     = "permission_lost"
)

// RefusalError denies a background submission with a bounded code and the
// HTTP status the dispatcher must return.
type RefusalError struct {
	Code   string
	Status int
}

func (err *RefusalError) Error() string { return "background submission refused: " + err.Code }

// SubmissionRequest carries the caller-selected submission facts plus the
// host-derived installation identity. TokenSecret is discarded after native
// verification and never recorded or logged.
type SubmissionRequest struct {
	InstallationID    string
	TokenSecret       string
	ActorID           int64
	RepositoryID      int64
	Kind              string
	PackageID         string
	ControllerGrantID string
	RequireController bool
}

// SubmissionDecision carries the verified submission provenance. The
// credential fingerprint detects regeneration of the same token row; it is
// neither a Bearer [REDACTED] nor public receipt data.
type SubmissionDecision struct {
	TokenID               int64
	ActorID               int64
	CredentialFingerprint string
	PackageID             string
	ControllerGrantID     string
}

func refuse(code string, status int) *RefusalError { return &RefusalError{Code: code, Status: status} }

// CredentialFingerprint binds a native credential generation from its
// authoritative hash/salt. Regenerating the token row changes it.
func CredentialFingerprint(tokenHash, tokenSalt string) string {
	sum := sha256.Sum256([]byte("extension-background-credential\x00" + tokenHash + "\x00" + tokenSalt))
	return hex.EncodeToString(sum[:])
}

// VerifySubmission authenticates a background submission against the enrolled
// binding and current native authority. Effective authority is the
// intersection of the binding, the native token scope/resource restrictions
// and the current native account/repository permissions. Manifest capability
// is enforced by the admission layer, not here.
func VerifySubmission(ctx context.Context, request SubmissionRequest) (SubmissionDecision, error) {
	if request.RequireController && request.ControllerGrantID == "" {
		return SubmissionDecision{}, refuse(RefusalBindingMissing, http.StatusForbidden)
	}
	if _, err := uuid.Parse(request.InstallationID); err != nil {
		return SubmissionDecision{}, refuse(RefusalInvalidIntent, http.StatusBadRequest)
	}
	if request.TokenSecret == "" || request.ActorID <= 0 || request.RepositoryID <= 0 {
		return SubmissionDecision{}, refuse(RefusalInvalidIntent, http.StatusBadRequest)
	}
	if !ValidKind(request.Kind) {
		return SubmissionDecision{}, refuse(RefusalUnknownKind, http.StatusBadRequest)
	}
	token, err := auth_model.GetAccessTokenBySHA(ctx, request.TokenSecret)
	if err != nil {
		if ctx.Err() != nil {
			return SubmissionDecision{}, ctx.Err()
		}
		if auth_model.IsErrAccessTokenNotExist(err) || auth_model.IsErrAccessTokenEmpty(err) {
			return SubmissionDecision{}, refuse(RefusalInvalidCredential, http.StatusUnauthorized)
		}
		return SubmissionDecision{}, err
	}
	// Always compare against the current authoritative hash/salt, including
	// after a cached-ID lookup, before capturing the fingerprint.
	expected := auth_model.HashToken(request.TokenSecret, token.TokenSalt)
	if subtle.ConstantTimeCompare([]byte(token.TokenHash), []byte(expected)) != 1 {
		return SubmissionDecision{}, refuse(RefusalInvalidCredential, http.StatusUnauthorized)
	}
	if token.UID != request.ActorID {
		return SubmissionDecision{}, refuse(RefusalActorMismatch, http.StatusForbidden)
	}
	binding, err := FindBinding(ctx, request.InstallationID, token.ID, request.ActorID, request.RepositoryID, request.Kind)
	if err != nil {
		return SubmissionDecision{}, err
	}
	if binding == nil {
		return SubmissionDecision{}, refuse(RefusalBindingMissing, http.StatusForbidden)
	}
	// All supported background kinds mutate native collaboration state, so the
	// token must currently grant repository write. Kind-specific native
	// checks stay at each kind's own enforcement boundary.
	granted, err := token.Scope.HasScope(auth_model.AccessTokenScopeWriteRepository)
	if err != nil || !granted {
		return SubmissionDecision{}, refuse(RefusalInsufficientScope, http.StatusForbidden)
	}
	if !token.ResourceAllRepos {
		resources, err := auth_model.GetRepositoriesAccessibleWithToken(ctx, token.ID)
		if err != nil {
			return SubmissionDecision{}, err
		}
		allowed := false
		for _, resource := range resources {
			if resource.RepoID == request.RepositoryID {
				allowed = true
				break
			}
		}
		if !allowed {
			return SubmissionDecision{}, refuse(RefusalRepositoryExcluded, http.StatusForbidden)
		}
	}
	user, err := user_model.GetUserByID(ctx, request.ActorID)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			return SubmissionDecision{}, refuse(RefusalAccountUnavailable, http.StatusForbidden)
		}
		return SubmissionDecision{}, err
	}
	if !user.IsActive || user.ProhibitLogin {
		return SubmissionDecision{}, refuse(RefusalAccountUnavailable, http.StatusForbidden)
	}
	repository, err := repo_model.GetRepositoryByID(ctx, request.RepositoryID)
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return SubmissionDecision{}, refuse(RefusalInvalidIntent, http.StatusBadRequest)
		}
		return SubmissionDecision{}, err
	}
	publicOnly, err := token.Scope.PublicOnly()
	if err != nil {
		return SubmissionDecision{}, refuse(RefusalInsufficientScope, http.StatusForbidden)
	}
	if publicOnly && repository.IsPrivate {
		return SubmissionDecision{}, refuse(RefusalRepositoryExcluded, http.StatusForbidden)
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repository, user)
	if err != nil {
		return SubmissionDecision{}, err
	}
	if !permission.CanWrite(unit.TypeCode) {
		return SubmissionDecision{}, refuse(RefusalPermissionLost, http.StatusForbidden)
	}
	decision := SubmissionDecision{
		TokenID:               token.ID,
		ActorID:               request.ActorID,
		CredentialFingerprint: CredentialFingerprint(token.TokenHash, token.TokenSalt),
		PackageID:             request.PackageID, ControllerGrantID: request.ControllerGrantID,
	}
	if request.ControllerGrantID != "" {
		if _, err = CheckControllerUse(ctx, ControllerUse{GrantID: request.ControllerGrantID, InstallationID: request.InstallationID, PackageID: request.PackageID,
			ActorID: request.ActorID, RepositoryID: request.RepositoryID, TokenID: decision.TokenID, CredentialFingerprint: decision.CredentialFingerprint, Kind: request.Kind}); err != nil {
			return SubmissionDecision{}, refuse(RefusalBindingMissing, http.StatusForbidden)
		}
	}
	return decision, nil
}

// SnapshotReadRequest carries the caller-selected snapshot-read facts plus
// the host-derived installation identity. TokenSecret is discarded after
// native verification and never recorded or logged.
type SnapshotReadRequest struct {
	InstallationID string
	TokenSecret    string
	ActorID        int64
	RepositoryID   int64
	Families       []string
}

// SnapshotReadDecision retains the native credential restrictions for every
// record joined by the loader, including targets in another repository.
// It contains no submitted token secret and is never returned on the wire.
type SnapshotReadDecision struct {
	SubmissionDecision
	RepositoryID     int64
	Scope            auth_model.AccessTokenScope
	PublicOnly       bool
	ResourceAllRepos bool
	RepositoryIDs    []int64
}

// AllowsRepository intersects the credential's public/resource restrictions.
// Current account and unit permissions must also be checked by the loader.
func (d SnapshotReadDecision) AllowsRepository(id int64, private bool) bool {
	if d.PublicOnly && private {
		return false
	}
	if d.ResourceAllRepos {
		return true
	}
	return slices.Contains(d.RepositoryIDs, id)
}

// CanReadFamilies requires every native API scope selected by the read.
func (d SnapshotReadDecision) CanReadFamilies(families []string) bool {
	if len(families) == 0 {
		return false
	}
	for _, family := range families {
		var scope auth_model.AccessTokenScope
		switch family {
		case sdk.SnapshotFamilyIssue, sdk.SnapshotFamilyComments, sdk.SnapshotFamilyDependencies, sdk.SnapshotFamilyRepositoryIssues:
			scope = auth_model.AccessTokenScopeReadIssue
		case sdk.SnapshotFamilyRepositoryWriters, sdk.SnapshotFamilyPull, sdk.SnapshotFamilyReviews, sdk.SnapshotFamilyReviewThreads, sdk.SnapshotFamilyMergeEligibility, sdk.SnapshotFamilyChecks, sdk.SnapshotFamilyRefs, sdk.SnapshotFamilyAncestry:
			scope = auth_model.AccessTokenScopeReadRepository
		default:
			return false
		}
		granted, err := d.Scope.HasScope(scope)
		if err != nil || !granted {
			return false
		}
	}
	return true
}

// VerifySnapshotRead authenticates a background snapshot read against an
// enrolled binding and current native read authority. Effective authority is
// the intersection of the binding, the native token scope/resource
// restrictions and the current native account/repository read permissions.
// Unlike submission, reads require only read scope and read access: a
// read-only actor observes nothing it cannot already see, and the read
// grants no mutation kind.
func VerifySnapshotRead(ctx context.Context, request SnapshotReadRequest) (SnapshotReadDecision, error) {
	if _, err := uuid.Parse(request.InstallationID); err != nil {
		return SnapshotReadDecision{}, refuse(RefusalInvalidIntent, http.StatusBadRequest)
	}
	if request.TokenSecret == "" || request.ActorID <= 0 || request.RepositoryID <= 0 {
		return SnapshotReadDecision{}, refuse(RefusalInvalidIntent, http.StatusBadRequest)
	}
	token, err := auth_model.GetAccessTokenBySHA(ctx, request.TokenSecret)
	if err != nil {
		if ctx.Err() != nil {
			return SnapshotReadDecision{}, ctx.Err()
		}
		if auth_model.IsErrAccessTokenNotExist(err) || auth_model.IsErrAccessTokenEmpty(err) {
			return SnapshotReadDecision{}, refuse(RefusalInvalidCredential, http.StatusUnauthorized)
		}
		return SnapshotReadDecision{}, err
	}
	// Always compare against the current authoritative hash/salt, including
	// after a cached-ID lookup, before capturing the fingerprint.
	expected := auth_model.HashToken(request.TokenSecret, token.TokenSalt)
	if subtle.ConstantTimeCompare([]byte(token.TokenHash), []byte(expected)) != 1 {
		return SnapshotReadDecision{}, refuse(RefusalInvalidCredential, http.StatusUnauthorized)
	}
	if token.UID != request.ActorID {
		return SnapshotReadDecision{}, refuse(RefusalActorMismatch, http.StatusForbidden)
	}
	bound, err := HasAnyBinding(ctx, request.InstallationID, token.ID, request.ActorID, request.RepositoryID)
	if err != nil {
		return SnapshotReadDecision{}, err
	}
	if !bound {
		return SnapshotReadDecision{}, refuse(RefusalBindingMissing, http.StatusForbidden)
	}
	decision := SnapshotReadDecision{
		SubmissionDecision: SubmissionDecision{
			TokenID: token.ID, ActorID: request.ActorID,
			CredentialFingerprint: CredentialFingerprint(token.TokenHash, token.TokenSalt),
		},
		RepositoryID: request.RepositoryID, Scope: token.Scope, ResourceAllRepos: token.ResourceAllRepos,
	}
	if !decision.CanReadFamilies(request.Families) {
		return SnapshotReadDecision{}, refuse(RefusalInsufficientScope, http.StatusForbidden)
	}
	decision.PublicOnly, err = token.Scope.PublicOnly()
	if err != nil {
		return SnapshotReadDecision{}, refuse(RefusalInsufficientScope, http.StatusForbidden)
	}
	if !token.ResourceAllRepos {
		resources, err := auth_model.GetRepositoriesAccessibleWithToken(ctx, token.ID)
		if err != nil {
			return SnapshotReadDecision{}, err
		}
		allowed := false
		for _, resource := range resources {
			decision.RepositoryIDs = append(decision.RepositoryIDs, resource.RepoID)
			if resource.RepoID == request.RepositoryID {
				allowed = true
			}
		}
		if !allowed {
			return SnapshotReadDecision{}, refuse(RefusalRepositoryExcluded, http.StatusForbidden)
		}
	}
	user, err := user_model.GetUserByID(ctx, request.ActorID)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			return SnapshotReadDecision{}, refuse(RefusalAccountUnavailable, http.StatusForbidden)
		}
		return SnapshotReadDecision{}, err
	}
	if !user.IsActive || user.ProhibitLogin {
		return SnapshotReadDecision{}, refuse(RefusalAccountUnavailable, http.StatusForbidden)
	}
	repositoryRecord, err := repo_model.GetRepositoryByID(ctx, request.RepositoryID)
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return SnapshotReadDecision{}, refuse(RefusalInvalidIntent, http.StatusBadRequest)
		}
		return SnapshotReadDecision{}, err
	}
	if !decision.AllowsRepository(repositoryRecord.ID, repositoryRecord.IsPrivate) {
		return SnapshotReadDecision{}, refuse(RefusalRepositoryExcluded, http.StatusForbidden)
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repositoryRecord, user)
	if err != nil {
		return SnapshotReadDecision{}, err
	}
	if !permission.CanReadAny(unit.TypeCode, unit.TypeIssues, unit.TypePullRequests) {
		return SnapshotReadDecision{}, refuse(RefusalPermissionLost, http.StatusForbidden)
	}
	return decision, nil
}

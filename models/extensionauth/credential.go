// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package extensionauth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"slices"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/desktopgrant"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
)

type credentialProfile uint8

const (
	mutationCredential credentialProfile = iota
	readCredential
	controllerCredential
)

func (profile credentialProfile) refuse(code string, status int) error {
	if profile == controllerCredential {
		return desktopgrant.ErrController
	}
	return refuse(code, status)
}

// nativePAT contains facts read from the current token row, never preparation
// authority. Every caller constructs it inside its own admission boundary.
type nativePAT struct {
	token       *auth_model.AccessToken
	fingerprint string
}

func identifyPAT(token *auth_model.AccessToken, actorID int64, profile credentialProfile) (nativePAT, error) {
	if token.UID != actorID {
		return nativePAT{}, profile.refuse(RefusalActorMismatch, http.StatusForbidden)
	}
	return nativePAT{token: token, fingerprint: CredentialFingerprint(token.TokenHash, token.TokenSalt)}, nil
}

func authenticatePAT(ctx context.Context, secret string, actorID int64) (nativePAT, error) {
	token, err := auth_model.GetAccessTokenBySHA(ctx, secret)
	if err != nil {
		if ctx.Err() != nil {
			return nativePAT{}, ctx.Err()
		}
		if auth_model.IsErrAccessTokenNotExist(err) || auth_model.IsErrAccessTokenEmpty(err) {
			return nativePAT{}, refuse(RefusalInvalidCredential, http.StatusUnauthorized)
		}
		return nativePAT{}, err
	}
	// A cached token ID is not proof of the current secret generation.
	expected := auth_model.HashToken(secret, token.TokenSalt)
	if subtle.ConstantTimeCompare([]byte(token.TokenHash), []byte(expected)) != 1 {
		return nativePAT{}, refuse(RefusalInvalidCredential, http.StatusUnauthorized)
	}
	return identifyPAT(token, actorID, mutationCredential)
}

func currentControllerPAT(ctx context.Context, tokenID, actorID int64, fingerprint string) (nativePAT, error) {
	token := new(auth_model.AccessToken)
	found, err := db.GetEngine(ctx).ID(tokenID).NoAutoCondition().Get(token)
	if err != nil {
		return nativePAT{}, err
	}
	if !found {
		return nativePAT{}, desktopgrant.ErrController
	}
	credential, err := identifyPAT(token, actorID, controllerCredential)
	if err != nil {
		return nativePAT{}, err
	}
	if credential.fingerprint != fingerprint {
		return nativePAT{}, desktopgrant.ErrController
	}
	return credential, nil
}

func (credential nativePAT) decision() SubmissionDecision {
	return SubmissionDecision{TokenID: credential.token.ID, ActorID: credential.token.UID, CredentialFingerprint: credential.fingerprint}
}

func (credential nativePAT) requireScope(profile credentialProfile, families []string) error {
	var granted bool
	var err error
	if profile == readCredential {
		granted = (SnapshotReadDecision{Scope: credential.token.Scope}).CanReadFamilies(families)
	} else {
		granted, err = credential.token.Scope.HasScope(auth_model.AccessTokenScopeWriteRepository)
	}
	if err != nil || !granted {
		return profile.refuse(RefusalInsufficientScope, http.StatusForbidden)
	}
	return nil
}

func (credential nativePAT) publicOnly(profile credentialProfile) (bool, error) {
	publicOnly, err := credential.token.Scope.PublicOnly()
	if err != nil {
		return false, profile.refuse(RefusalInsufficientScope, http.StatusForbidden)
	}
	return publicOnly, nil
}

func (credential nativePAT) repositoryResources(ctx context.Context, repositoryID int64, profile credentialProfile) ([]int64, error) {
	if credential.token.ResourceAllRepos {
		return nil, nil
	}
	resources, err := auth_model.GetRepositoriesAccessibleWithToken(ctx, credential.token.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(resources))
	for _, resource := range resources {
		ids = append(ids, resource.RepoID)
	}
	if !slices.Contains(ids, repositoryID) {
		return nil, profile.refuse(RefusalRepositoryExcluded, http.StatusForbidden)
	}
	return ids, nil
}

func checkNativeAuthority(ctx context.Context, actorID, repositoryID int64, profile credentialProfile, restrict func(*repo_model.Repository) error) error {
	actor, err := user_model.GetUserByID(ctx, actorID)
	if err != nil {
		if profile != controllerCredential && user_model.IsErrUserNotExist(err) {
			return profile.refuse(RefusalAccountUnavailable, http.StatusForbidden)
		}
		return err
	}
	if !actor.IsActive || actor.ProhibitLogin {
		return profile.refuse(RefusalAccountUnavailable, http.StatusForbidden)
	}
	repository, err := repo_model.GetRepositoryByID(ctx, repositoryID)
	if err != nil {
		if profile != controllerCredential && repo_model.IsErrRepoNotExist(err) {
			return profile.refuse(RefusalInvalidIntent, http.StatusBadRequest)
		}
		return err
	}
	if restrict != nil {
		if err := restrict(repository); err != nil {
			return err
		}
	}
	if profile == controllerCredential && (repository.IsArchived || repository.IsMirror || user_model.IsBlocked(ctx, repository.OwnerID, actor.ID)) {
		return desktopgrant.ErrController
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repository, actor)
	if err != nil {
		return err
	}
	allowed := permission.CanWrite(unit.TypeCode)
	if profile == readCredential {
		allowed = permission.CanReadAny(unit.TypeCode, unit.TypeIssues, unit.TypePullRequests)
	}
	if !allowed {
		return profile.refuse(RefusalPermissionLost, http.StatusForbidden)
	}
	return nil
}

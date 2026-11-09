// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package extensionauth

import (
	"context"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/desktopgrant"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
)

// ControllerUse is verified provenance, supplied by an authenticated host
// adapter or loaded from the original native operation. It carries no secret.
type ControllerUse struct {
	GrantID               string
	InstallationID        string
	PackageID             string
	ActorID               int64
	RepositoryID          int64
	TokenID               int64
	CredentialFingerprint string
	Kind                  string
	NotAfter              int64
}

// CheckControllerUse holds the repository controller through an enclosing
// native commit-admission transaction. A current login, PAT binding or read
// enrollment alone cannot substitute for this separate finite grant.
func CheckControllerUse(ctx context.Context, use ControllerUse) (*desktopgrant.OperationGrant, error) {
	g, err := desktopgrant.GetOperationGrant(ctx, use.GrantID)
	if err != nil {
		return nil, err
	}
	if g.RepositoryID != use.RepositoryID || g.ActorID != use.ActorID || g.InstallationID != use.InstallationID ||
		g.Revoked || g.NotAfter <= time.Now().Unix() || !g.Allows(use.Kind) || use.NotAfter > g.NotAfter {
		return nil, desktopgrant.ErrController
	}
	err = desktopgrant.WithPlacement(ctx, g.RepositoryID, func(ctx context.Context, p *desktopgrant.Placement) error {
		// Reread under the same gate used by revocation and runtime selection.
		var err error
		g, err = desktopgrant.GetOperationGrant(ctx, use.GrantID)
		if err != nil {
			return err
		}
		if g.Revoked || g.NotAfter <= time.Now().Unix() || p.State != desktopgrant.ControllerActive || p.Generation != g.Generation || p.RuntimeID != g.RuntimeID {
			return desktopgrant.ErrController
		}
		switch g.CredentialKind {
		case desktopgrant.CredentialOAuth:
			if !setting.FactoryControlConfigured() {
				return desktopgrant.ErrController
			}
			if use.TokenID != 0 || use.PackageID != "" || use.CredentialFingerprint != g.Fingerprint() || p.OwnerKind != desktopgrant.ControllerDesktop || p.OwnerID != g.EnrollmentID || g.ProofSHA256 == "" {
				return desktopgrant.ErrController
			}
			enrollment, err := desktopgrant.Get(ctx, g.EnrollmentID)
			if err != nil || enrollment.Revoked || enrollment.ActorID != g.ActorID || enrollment.RepositoryID != g.RepositoryID || enrollment.RuntimeID != g.RuntimeID || enrollment.ProofSHA256 != g.ProofSHA256 || enrollment.OAuthGrantID != g.OAuthGrantID {
				return desktopgrant.ErrController
			}
			authorization, err := auth_model.GetOAuth2GrantByID(ctx, g.OAuthGrantID)
			if err != nil || authorization == nil || authorization.UserID != g.ActorID || authorization.ApplicationID != g.ApplicationID {
				return desktopgrant.ErrController
			}
			app, err := auth_model.GetOAuth2ApplicationByID(ctx, g.ApplicationID)
			if err != nil || app == nil || app.ConfidentialClient || setting.OAuth2.FactoryDesktopControlClientID == "" || app.ClientID != setting.OAuth2.FactoryDesktopControlClientID {
				return desktopgrant.ErrController
			}
			allowed, err := auth_model.AccessTokenScope(authorization.Scope).HasScope(auth_model.AccessTokenScopeWriteRepository)
			if err != nil || !allowed {
				return desktopgrant.ErrController
			}
		case desktopgrant.CredentialPAT:
			if use.TokenID <= 0 || use.TokenID != g.TokenID || use.CredentialFingerprint != g.TokenFingerprint || use.PackageID != g.PackageID ||
				g.PackageID == "" || g.PackageID != setting.OAuth2.FactoryAppliancePackageID || p.OwnerKind != desktopgrant.ControllerAppliance || p.OwnerID != g.InstallationID {
				return desktopgrant.ErrController
			}
			token := new(auth_model.AccessToken)
			found, err := db.GetEngine(ctx).ID(g.TokenID).NoAutoCondition().Get(token)
			if err != nil {
				return err
			}
			if !found || token.UID != g.ActorID || CredentialFingerprint(token.TokenHash, token.TokenSalt) != g.TokenFingerprint {
				return desktopgrant.ErrController
			}
			allowed, err := token.Scope.HasScope(auth_model.AccessTokenScopeWriteRepository)
			if err != nil || !allowed {
				return desktopgrant.ErrController
			}
			if use.Kind == desktopgrant.ExecutionLaunch {
				bound, err := HasAnyBinding(ctx, g.InstallationID, g.TokenID, g.ActorID, g.RepositoryID)
				if err != nil || !bound {
					return desktopgrant.ErrController
				}
			} else {
				binding, err := FindBinding(ctx, g.InstallationID, g.TokenID, g.ActorID, g.RepositoryID, use.Kind)
				if err != nil || binding == nil {
					return desktopgrant.ErrController
				}
			}
			publicOnly, err := token.Scope.PublicOnly()
			if err != nil {
				return desktopgrant.ErrController
			}
			repository, err := repo_model.GetRepositoryByID(ctx, g.RepositoryID)
			if err != nil || publicOnly && repository.IsPrivate {
				return desktopgrant.ErrController
			}
			if !token.ResourceAllRepos {
				resources, err := auth_model.GetRepositoriesAccessibleWithToken(ctx, g.TokenID)
				if err != nil {
					return err
				}
				included := false
				for _, resource := range resources {
					if resource.RepoID == g.RepositoryID {
						included = true
						break
					}
				}
				if !included {
					return desktopgrant.ErrController
				}
			}
		default:
			return desktopgrant.ErrController
		}
		actor, err := user_model.GetUserByID(ctx, g.ActorID)
		if err != nil {
			return err
		}
		if !actor.IsActive || actor.ProhibitLogin {
			return desktopgrant.ErrController
		}
		repository, err := repo_model.GetRepositoryByID(ctx, g.RepositoryID)
		if err != nil {
			return err
		}
		if repository.IsArchived || repository.IsMirror || user_model.IsBlocked(ctx, repository.OwnerID, actor.ID) {
			return desktopgrant.ErrController
		}
		permission, err := access_model.GetUserRepoPermission(ctx, repository, actor)
		if err != nil {
			return err
		}
		if !permission.CanWrite(unit.TypeCode) {
			return desktopgrant.ErrController
		}
		return nil
	})
	return g, err
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package extensionauth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/desktopgrant"
	"forgejo.org/models/extensionauth"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"

	"github.com/stretchr/testify/require"
)

func preparePATController(t *testing.T, actorID int64) (*auth_model.AccessToken, extensionauth.ControllerUse) {
	t.Helper()
	priorPackage := setting.OAuth2.FactoryAppliancePackageID
	setting.OAuth2.FactoryAppliancePackageID = "profile-appliance"
	t.Cleanup(func() { setting.OAuth2.FactoryAppliancePackageID = priorPackage })
	token := createWriterToken(t, actorID)
	enroll(t, token, testRepository, extensionauth.KindMerge)
	grant := &desktopgrant.OperationGrant{
		ID: strings.Repeat("1", 32), CredentialKind: desktopgrant.CredentialPAT,
		TokenID: token.ID, TokenFingerprint: extensionauth.CredentialFingerprint(token.TokenHash, token.TokenSalt),
		InstallationID: testInstallation, PackageID: setting.OAuth2.FactoryAppliancePackageID,
		ActorID: actorID, RepositoryID: testRepository, RuntimeID: desktopgrant.ApplianceRuntime(testInstallation),
		Generation: 1, Operations: extensionauth.KindMerge, NotAfter: time.Now().Unix() + 300,
	}
	require.NoError(t, desktopgrant.WithPlacement(t.Context(), testRepository, func(ctx context.Context, placement *desktopgrant.Placement) error {
		placement.RuntimeID, placement.Generation = grant.RuntimeID, grant.Generation
		placement.OwnerKind, placement.OwnerID = desktopgrant.ControllerAppliance, testInstallation
		placement.State, placement.SelectedBy = desktopgrant.ControllerActive, testActor
		if err := desktopgrant.SavePlacement(ctx, placement); err != nil {
			return err
		}
		_, err := desktopgrant.EnrollOperationGrant(ctx, grant)
		return err
	}))
	return token, extensionauth.ControllerUse{
		GrantID: grant.ID, InstallationID: testInstallation, PackageID: grant.PackageID,
		ActorID: actorID, RepositoryID: testRepository, TokenID: token.ID,
		CredentialFingerprint: grant.TokenFingerprint, Kind: extensionauth.KindMerge,
	}
}

func profileSubmission(token *auth_model.AccessToken) extensionauth.SubmissionRequest {
	return extensionauth.SubmissionRequest{
		InstallationID: testInstallation, TokenSecret: token.Token, ActorID: token.UID,
		RepositoryID: testRepository, Kind: extensionauth.KindMerge,
	}
}

func TestPATAdmissionProfilesKeepDistinctAuthority(t *testing.T) {
	for _, tc := range []struct {
		name           string
		actorID        int64
		change         func(*testing.T, *auth_model.AccessToken)
		readRefusal    string
		writeRefusal   string
		controllerFail bool
	}{
		{name: "current writer", actorID: testActor},
		{
			name: "read-only actor", actorID: 4,
			writeRefusal: extensionauth.RefusalPermissionLost, controllerFail: true,
		},
		{
			name: "scope narrowed after grant", actorID: testActor,
			change: func(t *testing.T, token *auth_model.AccessToken) {
				token.Scope = auth_model.AccessTokenScopeReadRepository
				_, err := db.GetEngine(t.Context()).ID(token.ID).Cols("scope").Update(token)
				require.NoError(t, err)
			},
			writeRefusal: extensionauth.RefusalInsufficientScope, controllerFail: true,
		},
		{
			name: "resource narrowed after grant", actorID: testActor,
			change: func(t *testing.T, token *auth_model.AccessToken) {
				token.ResourceAllRepos = false
				_, err := db.GetEngine(t.Context()).ID(token.ID).Cols("resource_all_repos").Update(token)
				require.NoError(t, err)
			},
			readRefusal: extensionauth.RefusalRepositoryExcluded, writeRefusal: extensionauth.RefusalRepositoryExcluded, controllerFail: true,
		},
		{
			name: "account prohibited after grant", actorID: testActor,
			change: func(t *testing.T, token *auth_model.AccessToken) {
				_, err := db.GetEngine(t.Context()).ID(token.UID).Cols("prohibit_login").Update(&user_model.User{ProhibitLogin: true})
				require.NoError(t, err)
			},
			readRefusal: extensionauth.RefusalAccountUnavailable, writeRefusal: extensionauth.RefusalAccountUnavailable, controllerFail: true,
		},
		{
			name: "archived repository", actorID: testActor,
			change: func(t *testing.T, _ *auth_model.AccessToken) {
				_, err := db.GetEngine(t.Context()).ID(testRepository).Cols("is_archived").Update(&repo_model.Repository{IsArchived: true})
				require.NoError(t, err)
			},
			controllerFail: true,
		},
		{
			name: "mirror repository", actorID: testActor,
			change: func(t *testing.T, _ *auth_model.AccessToken) {
				_, err := db.GetEngine(t.Context()).ID(testRepository).Cols("is_mirror").Update(&repo_model.Repository{IsMirror: true})
				require.NoError(t, err)
			},
			controllerFail: true,
		},
		{
			name: "binding withdrawn after grant", actorID: testActor,
			change: func(t *testing.T, token *auth_model.AccessToken) {
				_, err := extensionauth.RevokeBinding(t.Context(), testInstallation, token.ID, token.UID, testRepository, extensionauth.KindMerge)
				require.NoError(t, err)
			},
			readRefusal: extensionauth.RefusalBindingMissing, writeRefusal: extensionauth.RefusalBindingMissing, controllerFail: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, unittest.PrepareTestDatabase())
			token, use := preparePATController(t, tc.actorID)
			if tc.change != nil {
				tc.change(t, token)
			}
			_, readErr := extensionauth.VerifySnapshotRead(t.Context(), snapshotReadRequest(token, testRepository))
			_, writeErr := extensionauth.VerifySubmission(t.Context(), profileSubmission(token))
			for _, check := range []struct {
				err  error
				code string
			}{{readErr, tc.readRefusal}, {writeErr, tc.writeRefusal}} {
				if check.code == "" {
					require.NoError(t, check.err)
				} else {
					code, status := refusalCode(t, check.err)
					require.Equal(t, check.code, code)
					require.Equal(t, 403, status)
				}
			}
			_, controllerErr := extensionauth.CheckControllerUse(t.Context(), use)
			if tc.controllerFail {
				require.ErrorIs(t, controllerErr, desktopgrant.ErrController)
			} else {
				require.NoError(t, controllerErr)
			}
		})
	}
}

func TestPATRegenerationInvalidatesAllAdmissionProfiles(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	token, use := preparePATController(t, testActor)
	read, err := extensionauth.VerifySnapshotRead(t.Context(), snapshotReadRequest(token, testRepository))
	require.NoError(t, err)
	write, err := extensionauth.VerifySubmission(t.Context(), profileSubmission(token))
	require.NoError(t, err)
	require.Equal(t, read.CredentialFingerprint, write.CredentialFingerprint)
	require.Equal(t, use.CredentialFingerprint, write.CredentialFingerprint)
	_, err = extensionauth.CheckControllerUse(t.Context(), use)
	require.NoError(t, err)
	regenerated, err := auth_model.RegenerateAccessTokenByID(t.Context(), token.ID, token.UID)
	require.NoError(t, err)
	_, err = extensionauth.VerifySnapshotRead(t.Context(), snapshotReadRequest(token, testRepository))
	code, status := refusalCode(t, err)
	require.Equal(t, extensionauth.RefusalInvalidCredential, code)
	require.Equal(t, 401, status)
	_, err = extensionauth.VerifySubmission(t.Context(), profileSubmission(token))
	code, status = refusalCode(t, err)
	require.Equal(t, extensionauth.RefusalInvalidCredential, code)
	require.Equal(t, 401, status)
	_, err = extensionauth.CheckControllerUse(t.Context(), use)
	require.ErrorIs(t, err, desktopgrant.ErrController)
	read, err = extensionauth.VerifySnapshotRead(t.Context(), snapshotReadRequest(regenerated, testRepository))
	require.NoError(t, err)
	write, err = extensionauth.VerifySubmission(t.Context(), profileSubmission(regenerated))
	require.NoError(t, err)
	require.Equal(t, read.CredentialFingerprint, write.CredentialFingerprint)
	require.NotEqual(t, use.CredentialFingerprint, write.CredentialFingerprint)
}

func TestControllerPolicyPreservesExactConfiguredPackageFence(t *testing.T) {
	priorPackage := setting.OAuth2.FactoryAppliancePackageID
	t.Cleanup(func() { setting.OAuth2.FactoryAppliancePackageID = priorPackage })
	for _, tc := range []struct {
		configured string
		packageID  string
		fenced     bool
	}{
		{"", "", false},
		{"", "appliance", false},
		{"appliance", "", false},
		{"appliance", "unrelated", false},
		{"appliance", "appliance", true},
	} {
		setting.OAuth2.FactoryAppliancePackageID = tc.configured
		policy := extensionauth.ControllerPolicyForPackage(tc.packageID)
		require.Equal(t, tc.fenced, policy.RequiresController())
		if tc.fenced {
			require.ErrorIs(t, policy.CheckLegacyIntake(), desktopgrant.ErrController)
		} else {
			require.NoError(t, policy.CheckLegacyIntake())
		}
	}
}

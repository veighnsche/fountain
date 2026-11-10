// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/desktopgrant"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/services/auth"
	"forgejo.org/services/context"
	"forgejo.org/services/contexttest"

	"github.com/stretchr/testify/require"
)

type desktopRouteAuthentication struct {
	*auth.BaseAuthenticationResult
	grantID int64
}

func (identity desktopRouteAuthentication) OAuth2GrantID() int64 { return identity.grantID }
func (identity desktopRouteAuthentication) User() *user_model.User {
	return nil
}

func TestDesktopGrantRouteBindsPersistedConsent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		route     context.DesktopGrantRoute
		proof     string
		status    int
		grantKind string
	}{
		{"legacy repository proofless", context.DesktopRepositoryGrantRoute, "", http.StatusCreated, ""},
		{"issue proofless refused", context.DesktopIssueGrantRoute, "", http.StatusForbidden, ""},
		{"issue bound proof", context.DesktopIssueGrantRoute, strings.Repeat("a", 64), http.StatusCreated, "issues"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, unittest.PrepareTestDatabase())
			application := &auth_model.OAuth2Application{UID: 2, Name: "route consent", ClientID: "route-consent-client", ConfidentialClient: false}
			require.NoError(t, db.Insert(t.Context(), application))
			authorization := &auth_model.OAuth2Grant{UserID: 2, ApplicationID: application.ID, Scope: "read:repository,write:issue"}
			require.NoError(t, db.Insert(t.Context(), authorization))
			ctx, response := contexttest.MockAPIContext(t, "POST /api/v1/repos/user2/repo1/factory/desktop/grants")
			ctx.SetDoer(unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2}))
			ctx.SetAuthentication(desktopRouteAuthentication{BaseAuthenticationResult: &auth.BaseAuthenticationResult{}, grantID: authorization.ID})
			contexttest.LoadRepo(t, ctx, 1)
			ctx.Req.Header.Set("Authorization", "Bearer already-authenticated")
			proofHash := ""
			if tc.proof != "" {
				digest := sha256.Sum256([]byte(tc.proof))
				proofHash = hex.EncodeToString(digest[:])
				ctx.Req.Header.Set("X-Factory-Desktop-Proof", tc.proof)
			}
			id := strings.Repeat("1", 32)
			ctx.Req.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"command_id":%q,"runtime_id":%q,"proof_sha256":%q}`, id, strings.Repeat("2", 32), proofHash)))
			// An untyped template value has no influence on enrolled consent.
			ctx.Data["factory_desktop_issue_grant"] = true
			DesktopGrantHandler(tc.route, EnrollDesktopGrant)(ctx)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			grant, err := desktopgrant.Get(t.Context(), id)
			if tc.status == http.StatusCreated {
				require.NoError(t, err)
				require.Equal(t, tc.grantKind, grant.Kind)
				require.Equal(t, proofHash, grant.ProofSHA256)
			} else {
				require.ErrorIs(t, err, desktopgrant.ErrNotFound)
			}
		})
	}
}

func TestDesktopDiscoveryPreservesExplicitProtocolConsent(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	priorOAuth := setting.OAuth2
	t.Cleanup(func() { setting.OAuth2 = priorOAuth })
	setting.OAuth2.Enabled, setting.OAuth2.EnableAdditionalGrantScopes = true, true
	setting.OAuth2.FactoryDesktopClientID, setting.OAuth2.FactoryDesktopControlClientID = "route-consent-client", "route-control-client"
	setting.OAuth2.FactoryAppliancePackageID = "route-appliance"
	application := &auth_model.OAuth2Application{UID: 2, Name: "discovery consent", ClientID: "route-consent-client", RedirectURIs: []string{"http://127.0.0.1/"}, ConfidentialClient: false}
	require.NoError(t, db.Insert(t.Context(), application))
	controlApplication := &auth_model.OAuth2Application{UID: 2, Name: "control discovery consent", ClientID: "route-control-client", RedirectURIs: []string{"http://127.0.0.1/"}, ConfidentialClient: false}
	require.NoError(t, db.Insert(t.Context(), controlApplication))
	for _, tc := range []struct {
		name    string
		handler func(*context.APIContext)
		headers []string
		status  int
		scope   string
	}{
		{"legacy read", DesktopOAuthConfig, nil, http.StatusOK, "read:repository"},
		{"protocol 2 read", DesktopOAuthConfig, []string{"2"}, http.StatusOK, "read:repository read:issue"},
		{"legacy control", DesktopControlOAuthConfig, nil, http.StatusOK, "write:repository"},
		{"protocol 2 control", DesktopControlOAuthConfig, []string{"2"}, http.StatusOK, "write:repository write:issue"},
		{"protocol 2 issues", DesktopIssuesOAuthConfig, []string{"2"}, http.StatusOK, "write:issue"},
		{"unknown protocol", DesktopOAuthConfig, []string{"3"}, http.StatusBadRequest, ""},
		{"duplicate protocol", DesktopOAuthConfig, []string{"2", "2"}, http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, response := contexttest.MockAPIContext(t, "GET /api/v1/factory/desktop/oauth")
			for _, header := range tc.headers {
				ctx.Req.Header.Add("X-Factory-Desktop-Protocol", header)
			}
			tc.handler(ctx)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			if tc.status == http.StatusOK {
				var result map[string]string
				contexttest.DecodeJSON(t, response, &result)
				require.Equal(t, tc.scope, result["scope"])
			}
		})
	}
}

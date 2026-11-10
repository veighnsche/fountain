// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package navigationpolicy

import "testing"

func TestAuthPathSegmentBoundaries(t *testing.T) {
	for _, path := range []string{
		"/user/login", "/forge/user/logout/next", "/user/sign_up", "/user/forgot_password", "/user/forget_password",
		"/user/reset_password", "/user/recover_account", "/user/activate", "/user/activate_email", "/user/two_factor",
		"/user/webauthn", "/user/oauth2", "/user/openid", "/user/link_account", "/user/link_account_signin", "/user/link_account_signup",
		"/user/settings/change_password", "/forge/user/settings/security/two_factor", "/user/settings/applications", "/user/settings/keys",
		"/install", "/forge/login/oauth/callback", "/login/openid", "/oauth2/authorize", "/openid",
	} {
		if !IsAuthPath(path) {
			t.Errorf("auth destination admitted as ordinary navigation: %s", path)
		}
	}
	for _, path := range []string{"/user/login-extra", "/user/settings/security-extra", "/users/login", "/repo/installations", "/login/oauth-extra", "/team/repo/issues", "/user/profile"} {
		if IsAuthPath(path) {
			t.Errorf("ordinary destination classified as auth: %s", path)
		}
	}
}

func TestCredentialQueryNamesAndOpaqueLimit(t *testing.T) {
	for _, key := range []string{"token", "PASSWORD", "secret", "client_secret", "AUTHORIZATION", "auth", "code", "credential", "session", "api_key", "private_key", "access_token", "_TOKEN"} {
		if !IsCredentialQueryKey(key) {
			t.Errorf("credential-bearing key admitted: %s", key)
		}
	}
	for _, key := range []string{"monkey", "tokens", "session_id", "token_name", "access_token_label"} {
		if IsCredentialQueryKey(key) {
			t.Errorf("ordinary query key classified as a credential: %s", key)
		}
	}
	if MaxOpaqueRouteSegmentBytes() != 255 {
		t.Fatal("existing opaque route byte limit changed")
	}
}

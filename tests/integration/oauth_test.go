// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	extension "forgejo.org/extension-sdk"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/test"
	"forgejo.org/routers/web/auth"
	app_context "forgejo.org/services/context"
	"forgejo.org/tests"

	"github.com/markbates/goth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	go_oauth2 "golang.org/x/oauth2"
)

func TestAuthorizeNoClientID(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequest(t, "GET", "/login/oauth/authorize")
	ctx := loginUser(t, "user2")
	resp := ctx.MakeRequest(t, req, http.StatusBadRequest)
	assert.Contains(t, resp.Body.String(), "Client ID not registered")
}

func TestAuthorizeUnregisteredRedirect(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequest(t, "GET", "/login/oauth/authorize?client_id=da7da3ba-9a13-4167-856f-3899de0b0138&redirect_uri=UNREGISTERED&response_type=code&state=thestate")
	ctx := loginUser(t, "user1")
	resp := ctx.MakeRequest(t, req, http.StatusBadRequest)
	assert.Contains(t, resp.Body.String(), "Unregistered Redirect URI")
}

func TestAuthorizeUnsupportedResponseType(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequest(t, "GET", "/login/oauth/authorize?client_id=da7da3ba-9a13-4167-856f-3899de0b0138&redirect_uri=a&response_type=UNEXPECTED&state=thestate")
	ctx := loginUser(t, "user1")
	resp := ctx.MakeRequest(t, req, http.StatusSeeOther)
	u, err := resp.Result().Location()
	require.NoError(t, err)
	assert.Equal(t, "unsupported_response_type", u.Query().Get("error"))
	assert.Equal(t, "Only code response type is supported.", u.Query().Get("error_description"))
}

func TestAuthorizeUnsupportedCodeChallengeMethod(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequest(t, "GET", "/login/oauth/authorize?client_id=da7da3ba-9a13-4167-856f-3899de0b0138&redirect_uri=a&response_type=code&state=thestate&code_challenge_method=UNEXPECTED")
	ctx := loginUser(t, "user1")
	resp := ctx.MakeRequest(t, req, http.StatusSeeOther)
	u, err := resp.Result().Location()
	require.NoError(t, err)
	assert.Equal(t, "invalid_request", u.Query().Get("error"))
	assert.Equal(t, "unsupported code challenge method", u.Query().Get("error_description"))
}

func TestAuthorizeLoginRedirect(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequest(t, "GET", "/login/oauth/authorize")
	assert.Contains(t, MakeRequest(t, req, http.StatusSeeOther).Body.String(), "/user/login")
}

func TestAuthorizeShow(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequest(t, "GET", "/login/oauth/authorize?client_id=da7da3ba-9a13-4167-856f-3899de0b0138&redirect_uri=a&response_type=code&state=thestate")
	ctx := loginUser(t, "user4")
	resp := ctx.MakeRequest(t, req, http.StatusOK)

	htmlDoc := NewHTMLParser(t, resp.Body)
	htmlDoc.AssertElement(t, "#authorize-app", true)
}

func TestOAuth_AuthorizeConfidentialTwice(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// da7da3ba-9a13-4167-856f-3899de0b0138 a confidential client in models/fixtures/oauth2_application.yml

	// request authorization for the first time shows the grant page ...
	authorizeURL := "/login/oauth/authorize?client_id=da7da3ba-9a13-4167-856f-3899de0b0138&redirect_uri=a&response_type=code&state=thestate"
	req := NewRequest(t, "GET", authorizeURL)
	ctx := loginUser(t, "user4")
	resp := ctx.MakeRequest(t, req, http.StatusOK)

	htmlDoc := NewHTMLParser(t, resp.Body)
	htmlDoc.AssertElement(t, "#authorize-app", true)

	// ... and the user grants the authorization
	req = NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
		"client_id":    "da7da3ba-9a13-4167-856f-3899de0b0138",
		"redirect_uri": "a",
		"state":        "thestate",
		"granted":      "true",
	})
	resp = ctx.MakeRequest(t, req, http.StatusSeeOther)
	assert.Contains(t, test.RedirectURL(resp), "code=")

	// request authorization the second time and the grant page is not shown again, redirection happens immediately
	req = NewRequest(t, "GET", authorizeURL)
	resp = ctx.MakeRequest(t, req, http.StatusSeeOther)
	assert.Contains(t, test.RedirectURL(resp), "code=")
}

func TestOAuth_AuthorizePublicTwice(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// ce5a1322-42a7-11ed-b878-0242ac120002 is a public client in models/fixtures/oauth2_application.yml
	authorizeURL := "/login/oauth/authorize?client_id=ce5a1322-42a7-11ed-b878-0242ac120002&redirect_uri=b&response_type=code&code_challenge_method=plain&code_challenge=CODE&state=thestate"
	ctx := loginUser(t, "user4")
	// a public client must be authorized every time
	for _, name := range []string{"First", "Second"} {
		t.Run(name, func(t *testing.T) {
			req := NewRequest(t, "GET", authorizeURL)
			resp := ctx.MakeRequest(t, req, http.StatusOK)

			htmlDoc := NewHTMLParser(t, resp.Body)
			htmlDoc.AssertElement(t, "#authorize-app", true)

			req = NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
				"client_id":    "ce5a1322-42a7-11ed-b878-0242ac120002",
				"redirect_uri": "b",
				"state":        "thestate",
				"granted":      "true",
			})
			resp = ctx.MakeRequest(t, req, http.StatusSeeOther)
			assert.Contains(t, test.RedirectURL(resp), "code=")
		})
	}
}

func TestAuthorizeRedirectWithExistingGrant(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequest(t, "GET", "/login/oauth/authorize?client_id=da7da3ba-9a13-4167-856f-3899de0b0138&redirect_uri=https%3A%2F%2Fexample.com%2Fxyzzy&response_type=code&state=thestate")
	ctx := loginUser(t, "user1")
	resp := ctx.MakeRequest(t, req, http.StatusSeeOther)
	u, err := resp.Result().Location()
	require.NoError(t, err)
	assert.Equal(t, "thestate", u.Query().Get("state"))
	assert.Greaterf(t, len(u.Query().Get("code")), 30, "authorization code '%s' should be longer then 30", u.Query().Get("code"))
	u.RawQuery = ""
	assert.Equal(t, "https://example.com/xyzzy", u.String())
}

func TestAuthorizePKCERequiredForPublicClient(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequest(t, "GET", "/login/oauth/authorize?client_id=ce5a1322-42a7-11ed-b878-0242ac120002&redirect_uri=http%3A%2F%2F127.0.0.1&response_type=code&state=thestate")
	ctx := loginUser(t, "user1")
	resp := ctx.MakeRequest(t, req, http.StatusSeeOther)
	u, err := resp.Result().Location()
	require.NoError(t, err)
	assert.Equal(t, "invalid_request", u.Query().Get("error"))
	assert.Equal(t, "PKCE is required for public clients", u.Query().Get("error_description"))
}

func TestAccessTokenExchange(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp := MakeRequest(t, req, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(response)

	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), parsed))
	assert.Greater(t, len(parsed.AccessToken), 10)
	assert.Greater(t, len(parsed.RefreshToken), 10)
}

func TestAccessTokenExchangeRedirectURIMismatch(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// The auth code fixture has redirect_uri="a", but we send a different
	// URI that is registered with the app ("https://example.com/xyzzy").
	// Per RFC 6749 §4.1.3, this must be rejected.
	req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "https://example.com/xyzzy",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp := MakeRequest(t, req, http.StatusBadRequest)

	var parsedError auth.AccessTokenErrorResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unauthorized_client", string(parsedError.ErrorCode))
	assert.Equal(t, "redirect_uri does not match the authorization request", parsedError.ErrorDescription)

	// Using the correct redirect_uri ("a") should succeed
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	MakeRequest(t, req, http.StatusOK)
}

func TestAccessTokenExchangeWithPublicClient(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "ce5a1322-42a7-11ed-b878-0242ac120002",
		"redirect_uri":  "http://127.0.0.1/",
		"code":          "authcodepublic",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp := MakeRequest(t, req, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(response)

	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), parsed))
	assert.Greater(t, len(parsed.AccessToken), 10)
	assert.Greater(t, len(parsed.RefreshToken), 10)
}

func TestAccessTokenExchangeJSON(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequestWithJSON(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp := MakeRequest(t, req, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(response)

	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), parsed))
	assert.Greater(t, len(parsed.AccessToken), 10)
	assert.Greater(t, len(parsed.RefreshToken), 10)
}

func TestAccessTokenExchangeWithoutPKCE(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "authcode",
	})
	resp := MakeRequest(t, req, http.StatusBadRequest)
	var parsedError auth.AccessTokenErrorResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unauthorized_client", string(parsedError.ErrorCode))
	assert.Equal(t, "failed PKCE code challenge", parsedError.ErrorDescription)
}

func TestAccessTokenExchangeWithInvalidCredentials(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	// invalid client id
	req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "???",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp := MakeRequest(t, req, http.StatusBadRequest)
	var parsedError auth.AccessTokenErrorResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "invalid_client", string(parsedError.ErrorCode))
	assert.Equal(t, "cannot load client with client id: '???'", parsedError.ErrorDescription)

	// invalid client secret
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "???",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unauthorized_client", string(parsedError.ErrorCode))
	assert.Equal(t, "invalid client secret", parsedError.ErrorDescription)

	// invalid redirect uri
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "???",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unauthorized_client", string(parsedError.ErrorCode))
	assert.Equal(t, "unexpected redirect URI", parsedError.ErrorDescription)

	// invalid authorization code
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "???",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unauthorized_client", string(parsedError.ErrorCode))
	assert.Equal(t, "client is not authorized", parsedError.ErrorDescription)

	// invalid grant_type
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "???",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unsupported_grant_type", string(parsedError.ErrorCode))
	assert.Equal(t, "Only refresh_token or authorization_code grant type is supported", parsedError.ErrorDescription)
}

func TestAccessTokenExchangeWithBasicAuth(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	req.Header.Add("Authorization", "Basic ZGE3ZGEzYmEtOWExMy00MTY3LTg1NmYtMzg5OWRlMGIwMTM4OjRNSzhOYTZSNTVzbWRDWTBXdUNDdW1aNmhqUlBuR1k1c2FXVlJISGpKaUE9")
	resp := MakeRequest(t, req, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(response)

	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), parsed))
	assert.Greater(t, len(parsed.AccessToken), 10)
	assert.Greater(t, len(parsed.RefreshToken), 10)

	// use wrong client_secret
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	req.Header.Add("Authorization", "Basic ZGE3ZGEzYmEtOWExMy00MTY3LTg1NmYtMzg5OWRlMGIwMTM4OmJsYWJsYQ==")
	resp = MakeRequest(t, req, http.StatusBadRequest)
	var parsedError auth.AccessTokenErrorResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unauthorized_client", string(parsedError.ErrorCode))
	assert.Equal(t, "invalid client secret", parsedError.ErrorDescription)

	// missing header
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "invalid_client", string(parsedError.ErrorCode))
	assert.Equal(t, "cannot load client with client id: ''", parsedError.ErrorDescription)

	// client_id inconsistent with Authorization header
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":   "authorization_code",
		"redirect_uri": "a",
		"code":         "authcode",
		"client_id":    "inconsistent",
	})
	req.Header.Add("Authorization", "Basic ZGE3ZGEzYmEtOWExMy00MTY3LTg1NmYtMzg5OWRlMGIwMTM4OjRNSzhOYTZSNTVzbWRDWTBXdUNDdW1aNmhqUlBuR1k1c2FXVlJISGpKaUE9")
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "invalid_request", string(parsedError.ErrorCode))
	assert.Equal(t, "client_id in request body inconsistent with Authorization header", parsedError.ErrorDescription)

	// client_secret inconsistent with Authorization header
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"redirect_uri":  "a",
		"code":          "authcode",
		"client_secret": "inconsistent",
	})
	req.Header.Add("Authorization", "Basic ZGE3ZGEzYmEtOWExMy00MTY3LTg1NmYtMzg5OWRlMGIwMTM4OjRNSzhOYTZSNTVzbWRDWTBXdUNDdW1aNmhqUlBuR1k1c2FXVlJISGpKaUE9")
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "invalid_request", string(parsedError.ErrorCode))
	assert.Equal(t, "client_secret in request body inconsistent with Authorization header", parsedError.ErrorDescription)
}

func TestRefreshTokenInvalidation(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp := MakeRequest(t, req, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(response)

	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), parsed))

	// test without invalidation
	setting.OAuth2.InvalidateRefreshTokens = false

	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type": "refresh_token",
		"client_id":  "da7da3ba-9a13-4167-856f-3899de0b0138",
		// omit secret
		"redirect_uri":  "a",
		"refresh_token": parsed.RefreshToken,
	})
	resp = MakeRequest(t, req, http.StatusBadRequest)
	var parsedError auth.AccessTokenErrorResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "invalid_client", string(parsedError.ErrorCode))
	assert.Equal(t, "invalid empty client secret", parsedError.ErrorDescription)

	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"refresh_token": "UNEXPECTED",
	})
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unauthorized_client", string(parsedError.ErrorCode))
	assert.Equal(t, "unable to parse refresh token", parsedError.ErrorDescription)

	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"refresh_token": parsed.AccessToken,
	})
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unauthorized_client", string(parsedError.ErrorCode))
	assert.Equal(t, "token is not a refresh token", parsedError.ErrorDescription)

	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"refresh_token": parsed.RefreshToken,
	})

	bs, err := io.ReadAll(req.Body)
	require.NoError(t, err)

	req.Body = io.NopCloser(bytes.NewReader(bs))
	MakeRequest(t, req, http.StatusOK)

	req.Body = io.NopCloser(bytes.NewReader(bs))
	MakeRequest(t, req, http.StatusOK)

	// test with invalidation
	setting.OAuth2.InvalidateRefreshTokens = true
	req.Body = io.NopCloser(bytes.NewReader(bs))
	MakeRequest(t, req, http.StatusOK)

	// repeat request should fail
	req.Body = io.NopCloser(bytes.NewReader(bs))
	resp = MakeRequest(t, req, http.StatusBadRequest)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "unauthorized_client", string(parsedError.ErrorCode))
	assert.Equal(t, "token was already used", parsedError.ErrorDescription)
}

func TestRefreshTokenCrossClientUsage(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// Step 1: Obtain a refresh token via app 1 (confidential client)
	req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp := MakeRequest(t, req, http.StatusOK)

	type tokenResponse struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(tokenResponse)
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), parsed))
	assert.NotEmpty(t, parsed.RefreshToken)

	// Step 2: Try to use the refresh token with app 2 (different client): must fail
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     "ce5a1322-42a7-11ed-b878-0242ac120002",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "b",
		"refresh_token": parsed.RefreshToken,
	})
	resp = MakeRequest(t, req, http.StatusBadRequest)

	var parsedError auth.AccessTokenErrorResponse
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &parsedError))
	assert.Equal(t, "invalid_grant", string(parsedError.ErrorCode))
	assert.Equal(t, "refresh token was not issued to this client", parsedError.ErrorDescription)

	// Step 3: Using the refresh token with the correct app 1 should still work
	req = NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"refresh_token": parsed.RefreshToken,
	})
	MakeRequest(t, req, http.StatusOK)
}

func TestSignInOAuthCallbackSignIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	//
	// OAuth2 authentication source GitLab
	//
	gitlabName := "gitlab"
	gitlab := addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))

	//
	// Create a user as if it had been previously been created by the GitLab
	// authentication source.
	//
	userGitLabUserID := "5678"
	userGitLab := &user_model.User{
		Name:        "gitlabuser",
		Email:       "gitlabuser@example.com",
		Passwd:      "gitlabuserpassword",
		Type:        user_model.UserTypeIndividual,
		LoginType:   auth_model.OAuth2,
		LoginSource: gitlab.ID,
		LoginName:   userGitLabUserID,
	}
	defer createUser(t.Context(), t, userGitLab)()

	//
	// A request for user information sent to Goth will return a
	// goth.User exactly matching the user created above.
	//
	defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
		return goth.User{
			Provider: gitlabName,
			UserID:   userGitLabUserID,
			Email:    userGitLab.Email,
		}, nil
	})()
	req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
	resp := MakeRequest(t, req, http.StatusSeeOther)
	assert.Equal(t, "/", test.RedirectURL(resp))
	userAfterLogin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: userGitLab.ID})
	assert.Greater(t, userAfterLogin.LastLoginUnix, userGitLab.LastLoginUnix)
}

func TestSignInOAuthCallbackWithoutPKCEWhenUnsupported(t *testing.T) {
	// https://codeberg.org/forgejo/forgejo/issues/4033
	defer tests.PrepareTestEnv(t)()

	// Setup authentication source
	gitlabName := "gitlab"
	gitlab := addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))
	// Create a user as if it had been previously been created by the authentication source.
	userGitLabUserID := "5678"
	userGitLab := &user_model.User{
		Name:        "gitlabuser",
		Email:       "gitlabuser@example.com",
		Passwd:      "gitlabuserpassword",
		Type:        user_model.UserTypeIndividual,
		LoginType:   auth_model.OAuth2,
		LoginSource: gitlab.ID,
		LoginName:   userGitLabUserID,
	}
	defer createUser(t.Context(), t, userGitLab)()

	// initial redirection (to generate the code_challenge)
	session := emptyTestSession(t)
	req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s", gitlabName))
	resp := session.MakeRequest(t, req, http.StatusTemporaryRedirect)
	dest, err := url.Parse(resp.Header().Get("Location"))
	require.NoError(t, err)
	assert.Empty(t, dest.Query().Get("code_challenge_method"))
	assert.Empty(t, dest.Query().Get("code_challenge"))

	// callback (to check the initial code_challenge)
	defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
		assert.Empty(t, req.URL.Query().Get("code_verifier"))
		return goth.User{
			Provider: gitlabName,
			UserID:   userGitLabUserID,
			Email:    userGitLab.Email,
		}, nil
	})()
	req = NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
	resp = session.MakeRequest(t, req, http.StatusSeeOther)
	assert.Equal(t, "/", test.RedirectURL(resp))
	unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: userGitLab.ID})
}

func TestSignInOAuthCallbackPKCE(t *testing.T) {
	onApplicationRun(t, func(t *testing.T, u *url.URL) {
		// Setup authentication source
		sourceName := "oidc"
		authSource := addAuthSource(t, authSourcePayloadOpenIDConnect(sourceName, u.String()))
		// Create a user as if it had been previously been created by the authentication source.
		userID := "5678"
		user := &user_model.User{
			Name:        "oidc.user",
			Email:       "oidc.user@example.com",
			Passwd:      "oidc.userpassword",
			Type:        user_model.UserTypeIndividual,
			LoginType:   auth_model.OAuth2,
			LoginSource: authSource.ID,
			LoginName:   userID,
		}
		defer createUser(t.Context(), t, user)()

		// initial redirection (to generate the code_challenge)
		session := emptyTestSession(t)
		req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s", sourceName))
		resp := session.MakeRequest(t, req, http.StatusTemporaryRedirect)
		dest, err := url.Parse(resp.Header().Get("Location"))
		require.NoError(t, err)
		assert.Equal(t, "S256", dest.Query().Get("code_challenge_method"))
		codeChallenge := dest.Query().Get("code_challenge")
		assert.NotEmpty(t, codeChallenge)

		// callback (to check the initial code_challenge)
		defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
			codeVerifier := req.URL.Query().Get("code_verifier")
			assert.NotEmpty(t, codeVerifier)
			assert.Greater(t, len(codeVerifier), 40, codeVerifier)

			sha2 := sha256.New()
			io.WriteString(sha2, codeVerifier)
			assert.Equal(t, codeChallenge, base64.RawURLEncoding.EncodeToString(sha2.Sum(nil)))

			return goth.User{
				Provider: sourceName,
				UserID:   userID,
				Email:    user.Email,
			}, nil
		})()
		req = NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", sourceName))
		resp = session.MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/", test.RedirectURL(resp))
		unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: user.ID})
	})
}

func TestWellKnownOpenIDConfiguration(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	t.Run("Issuer does not end with a slash", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()

		req := NewRequest(t, "GET", "/.well-known/openid-configuration")
		resp := MakeRequest(t, req, http.StatusOK)
		type response struct {
			Issuer string `json:"issuer"`
		}
		parsed := new(response)

		DecodeJSON(t, resp, parsed)
		assert.Equal(t, strings.TrimSuffix(setting.AppURL, "/"), parsed.Issuer)
	})

	t.Run("Not found if OAuth2 is not enabled", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		defer test.MockVariableValue(&setting.OAuth2.Enabled, false)()

		MakeRequest(t, NewRequest(t, "GET", "/.well-known/openid-configuration"), http.StatusNotFound)
	})
}

func TestSignInOAuthCallbackRedirectToEscaping(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	//
	// OAuth2 authentication source GitLab
	//
	gitlabName := "gitlab"
	gitlab := addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))

	//
	// Create a user as if it had been previously created by the GitLab
	// authentication source.
	//
	userGitLabUserID := "5678"
	userGitLab := &user_model.User{
		Name:        "gitlabuser",
		Email:       "gitlabuser@example.com",
		Passwd:      "gitlabuserpassword",
		Type:        user_model.UserTypeIndividual,
		LoginType:   auth_model.OAuth2,
		LoginSource: gitlab.ID,
		LoginName:   userGitLabUserID,
	}
	defer createUser(t.Context(), t, userGitLab)()

	//
	// A request for user information sent to Goth will return a
	// goth.User exactly matching the user created above.
	//
	defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
		return goth.User{
			Provider: gitlabName,
			UserID:   userGitLabUserID,
			Email:    userGitLab.Email,
		}, nil
	})()
	req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
	req.AddCookie(&http.Cookie{
		Name:  "redirect_to",
		Value: "/login/oauth/authorize?redirect_uri=https%3A%2F%2Ftranslate.example.org",
		Path:  "/",
	})
	resp := MakeRequest(t, req, http.StatusSeeOther)

	hasNewSessionCookie := false
	sessionCookieName := setting.SessionConfig.CookieName
	for _, c := range resp.Result().Cookies() {
		if c.Name == sessionCookieName {
			hasNewSessionCookie = true
			break
		}
		t.Log("Got cookie", c.Name)
	}

	assert.True(t, hasNewSessionCookie, "Session cookie %q is missing", sessionCookieName)
	assert.Equal(t, "/login/oauth/authorize?redirect_uri=https://translate.example.org", test.RedirectURL(resp))
}

func setupMockOIDCServer() *httptest.Server {
	var mockServer *httptest.Server
	mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"issuer": "` + strings.TrimSuffix(mockServer.URL, "/") + `",
				"authorization_endpoint": "` + mockServer.URL + `/authorize",
				"token_endpoint": "` + mockServer.URL + `/token",
				"userinfo_endpoint": "` + mockServer.URL + `/userinfo"
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return mockServer
}

func TestSignInOauthCallbackSyncSSHKeys(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	mockServer := setupMockOIDCServer()
	defer mockServer.Close()

	sourceName := "oidc"
	authPayload := authSourcePayloadOpenIDConnect(sourceName, mockServer.URL+"/")
	authPayload["oauth2_attribute_ssh_public_key"] = "sshpubkey"
	authSource := addAuthSource(t, authPayload)

	userID := "5678"
	user := &user_model.User{
		Name:        "oidc.user",
		Email:       "oidc.user@example.com",
		Passwd:      "oidc.userpassword",
		Type:        user_model.UserTypeIndividual,
		LoginType:   auth_model.OAuth2,
		LoginSource: authSource.ID,
		LoginName:   userID,
		IsActive:    true,
	}
	defer createUser(t.Context(), t, user)()

	for _, tt := range []struct {
		name          string
		rawData       map[string]any
		parsedKeySets []string
	}{
		{
			name: "Add keys",
			rawData: map[string]any{
				"sshpubkey": []any{
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINDRDoephkaFELacrNNe2fqAwedhRB1MKOpLEHlPuczO nocomment",
				},
			},
			parsedKeySets: []string{
				"SHA256:X/mW7JUQ8J8yhrKBbZ/pJni8qx7zPA1DTFsi8ftpDwg",
			},
		},
		{
			name: "Update keys",
			rawData: map[string]any{
				"sshpubkey": []any{
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMLLMOLFMouSJmzOASKKv178d+7op4utSxcugF9tVVch nocomment",
					"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGyDh9sg1IGQGa0U363wcGXrDlGBhZI3UHvS7we/0d+T nocomment",
				},
			},
			parsedKeySets: []string{
				"SHA256:gsyG4JNmY5XoLBK5lSzuwD3EXcaDBiDKBkqDkpQTH6Q",
				"SHA256:bbEKB1Qpumgk6QrgiN6t/kIvtUZvIQ8rqQBz8yYPzYw",
			},
		},
		{
			name: "Remove keys",
			rawData: map[string]any{
				"sshpubkey": []any{},
			},
			parsedKeySets: []string{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
				return goth.User{
					Provider: sourceName,
					UserID:   userID,
					Email:    user.Email,
					RawData:  tt.rawData,
				}, nil
			})()

			session := emptyTestSession(t)

			req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", sourceName))
			resp := session.MakeRequest(t, req, http.StatusSeeOther)
			assert.Equal(t, "/", test.RedirectURL(resp))

			req = NewRequest(t, "GET", "/user/settings/keys")
			resp = session.MakeRequest(t, req, http.StatusOK)

			htmlDoc := NewHTMLParser(t, resp.Body)
			divs := htmlDoc.doc.Find("#keys-ssh .flex-item .flex-item-body:not(:last-child)")

			syncedKeys := make([]string, divs.Length())
			for i := 0; i < divs.Length(); i++ {
				syncedKeys[i] = strings.TrimSpace(divs.Eq(i).Text())
			}

			assert.ElementsMatch(t, tt.parsedKeySets, syncedKeys, "Unequal number of keys")
		})
	}
}

func TestSignUpViaOAuthWithMissingFields(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	// enable auto-creation of accounts via OAuth2
	enableAutoRegistration := setting.OAuth2Client.EnableAutoRegistration
	setting.OAuth2Client.EnableAutoRegistration = true
	defer func() {
		setting.OAuth2Client.EnableAutoRegistration = enableAutoRegistration
	}()

	// OAuth2 authentication source GitLab
	gitlabName := "gitlab"
	addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))
	userGitLabUserID := "5678"

	// The Goth User returned by the oauth2 integration is missing
	// an email address, so we won't be able to automatically create a local account for it.
	defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
		return goth.User{
			Provider: gitlabName,
			UserID:   userGitLabUserID,
		}, nil
	})()
	req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
	resp := MakeRequest(t, req, http.StatusSeeOther)
	assert.Equal(t, "/user/link_account", test.RedirectURL(resp))
}

func TestOAuth_GrantApplicationOAuth(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/login/oauth/authorize?client_id=da7da3ba-9a13-4167-856f-3899de0b0138&redirect_uri=a&response_type=code&state=thestate")
	ctx := loginUser(t, "user4")
	resp := ctx.MakeRequest(t, req, http.StatusOK)

	htmlDoc := NewHTMLParser(t, resp.Body)
	htmlDoc.AssertElement(t, "#authorize-app", true)

	req = NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
		"client_id":    "da7da3ba-9a13-4167-856f-3899de0b0138",
		"redirect_uri": "a",
		"state":        "thestate",
		"granted":      "false",
	})
	resp = ctx.MakeRequest(t, req, http.StatusSeeOther)
	assert.Contains(t, test.RedirectURL(resp), "error=access_denied&error_description=the+request+is+denied")
}

func TestOAuthIntrospection(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "da7da3ba-9a13-4167-856f-3899de0b0138",
		"client_secret": "4MK8Na6R55smdCY0WuCCumZ6hjRPnGY5saWVRHHjJiA=",
		"redirect_uri":  "a",
		"code":          "authcode",
		"code_verifier": "N1Zo9-8Rfwhkt68r1r29ty8YwIraXR8eh_1Qwxg7yQXsonBt",
	})
	resp := MakeRequest(t, req, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(response)

	DecodeJSON(t, resp, parsed)
	assert.Greater(t, len(parsed.AccessToken), 10)
	assert.Greater(t, len(parsed.RefreshToken), 10)

	type introspectResponse struct {
		Active   bool   `json:"active"`
		Scope    string `json:"scope,omitempty"`
		Username string `json:"username"`
	}

	// successful request with a valid client_id/client_secret and a valid token
	t.Run("successful request with valid token", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", "/login/oauth/introspect", map[string]string{
			"token": parsed.AccessToken,
		})
		req.Header.Add("Authorization", "Basic ZGE3ZGEzYmEtOWExMy00MTY3LTg1NmYtMzg5OWRlMGIwMTM4OjRNSzhOYTZSNTVzbWRDWTBXdUNDdW1aNmhqUlBuR1k1c2FXVlJISGpKaUE9")
		resp := MakeRequest(t, req, http.StatusOK)

		introspectParsed := new(introspectResponse)
		DecodeJSON(t, resp, introspectParsed)
		assert.True(t, introspectParsed.Active)
		assert.Equal(t, "user1", introspectParsed.Username)
	})

	// successful request with a valid client_id/client_secret, but an invalid token
	t.Run("successful request with invalid token", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", "/login/oauth/introspect", map[string]string{
			"token": "xyzzy",
		})
		req.Header.Add("Authorization", "Basic ZGE3ZGEzYmEtOWExMy00MTY3LTg1NmYtMzg5OWRlMGIwMTM4OjRNSzhOYTZSNTVzbWRDWTBXdUNDdW1aNmhqUlBuR1k1c2FXVlJISGpKaUE9")
		resp := MakeRequest(t, req, http.StatusOK)
		introspectParsed := new(introspectResponse)
		DecodeJSON(t, resp, introspectParsed)
		assert.False(t, introspectParsed.Active)
	})

	// unsuccessful request with an invalid client_id/client_secret
	t.Run("unsuccessful request due to invalid basic auth", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", "/login/oauth/introspect", map[string]string{
			"token": parsed.AccessToken,
		})
		req.Header.Add("Authorization", "Basic ZGE3ZGEzYmEtOWExMy00MTY3LTg1NmYtMzg5OWRlMGIwMTM4OjRNSzhOYTZSNTVzbWRDWTBXdUNDdW1aNmhqUlBuR1k1c2FXVlJISGpK")
		resp := MakeRequest(t, req, http.StatusUnauthorized)
		assert.Contains(t, resp.Body.String(), "no valid authorization")
	})
}

func TestOAuth_GrantScopesReadUser(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	appBody := api.CreateOAuth2ApplicationOptions{
		Name: "oauth-provider-scopes-test",
		RedirectURIs: []string{
			"a",
		},
		ConfidentialClient: true,
	}

	req := NewRequestWithJSON(t, "POST", "/api/v1/user/applications/oauth2", &appBody).
		AddBasicAuth(user.Name)
	resp := MakeRequest(t, req, http.StatusCreated)

	var app *api.OAuth2Application
	DecodeJSON(t, resp, &app)

	grant := &auth_model.OAuth2Grant{
		ApplicationID: app.ID,
		UserID:        user.ID,
		Scope:         "openid profile email read:user",
	}

	err := db.Insert(db.DefaultContext, grant)
	require.NoError(t, err)

	assert.Contains(t, grant.Scope, "openid profile email read:user")

	ctx := loginUserWithPasswordRemember(t, user.Name, "password", true)

	authorizeURL := fmt.Sprintf("/login/oauth/authorize?client_id=%s&redirect_uri=a&response_type=code&state=thestate", app.ClientID)
	authorizeReq := NewRequest(t, "GET", authorizeURL)
	authorizeResp := ctx.MakeRequest(t, authorizeReq, http.StatusSeeOther)

	authcode := strings.Split(strings.Split(authorizeResp.Body.String(), "?code=")[1], "&amp")[0]
	grantReq := NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
		"client_id":    app.ClientID,
		"redirect_uri": "a",
		"state":        "thestate",
		"granted":      "true",
	})
	ctx.MakeRequest(t, grantReq, http.StatusBadRequest)

	accessTokenReq := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     app.ClientID,
		"client_secret": app.ClientSecret,
		"redirect_uri":  "a",
		"code":          authcode,
	})
	accessTokenResp := ctx.MakeRequest(t, accessTokenReq, 200)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(response)

	require.NoError(t, json.Unmarshal(accessTokenResp.Body.Bytes(), parsed))
	userReq := NewRequest(t, "GET", "/api/v1/user")
	userReq.SetHeader("Authorization", "Bearer "+parsed.AccessToken)
	userResp := MakeRequest(t, userReq, http.StatusOK)

	// assert.Contains(t, string(userResp.Body.Bytes()), "blah")
	type userResponse struct {
		Login string `json:"login"`
		Email string `json:"email"`
	}

	userParsed := new(userResponse)
	require.NoError(t, json.Unmarshal(userResp.Body.Bytes(), userParsed))
	assert.Contains(t, userParsed.Email, "user2@example.com")
}

func TestOAuth_GrantScopesFailReadRepository(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	appBody := api.CreateOAuth2ApplicationOptions{
		Name: "oauth-provider-scopes-test",
		RedirectURIs: []string{
			"a",
		},
		ConfidentialClient: true,
	}

	req := NewRequestWithJSON(t, "POST", "/api/v1/user/applications/oauth2", &appBody).
		AddBasicAuth(user.Name)
	resp := MakeRequest(t, req, http.StatusCreated)

	var app *api.OAuth2Application
	DecodeJSON(t, resp, &app)

	grant := &auth_model.OAuth2Grant{
		ApplicationID: app.ID,
		UserID:        user.ID,
		Scope:         "openid profile email read:user",
	}

	err := db.Insert(db.DefaultContext, grant)
	require.NoError(t, err)

	assert.Contains(t, grant.Scope, "openid profile email read:user")

	ctx := loginUserWithPasswordRemember(t, user.Name, "password", true)

	authorizeURL := fmt.Sprintf("/login/oauth/authorize?client_id=%s&redirect_uri=a&response_type=code&state=thestate", app.ClientID)
	authorizeReq := NewRequest(t, "GET", authorizeURL)
	authorizeResp := ctx.MakeRequest(t, authorizeReq, http.StatusSeeOther)

	authcode := strings.Split(strings.Split(authorizeResp.Body.String(), "?code=")[1], "&amp")[0]
	grantReq := NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
		"client_id":    app.ClientID,
		"redirect_uri": "a",
		"state":        "thestate",
		"granted":      "true",
	})
	ctx.MakeRequest(t, grantReq, http.StatusBadRequest)

	accessTokenReq := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     app.ClientID,
		"client_secret": app.ClientSecret,
		"redirect_uri":  "a",
		"code":          authcode,
	})
	accessTokenResp := ctx.MakeRequest(t, accessTokenReq, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(response)

	require.NoError(t, json.Unmarshal(accessTokenResp.Body.Bytes(), parsed))
	userReq := NewRequest(t, "GET", "/api/v1/users/user2/repos")
	userReq.SetHeader("Authorization", "Bearer "+parsed.AccessToken)
	userResp := MakeRequest(t, userReq, http.StatusForbidden)

	type userResponse struct {
		Message string `json:"message"`
	}

	userParsed := new(userResponse)
	require.NoError(t, json.Unmarshal(userResp.Body.Bytes(), userParsed))
	assert.Contains(t, userParsed.Message, "token does not have at least one of required scope(s): [read:repository]")
}

func TestOAuth_GrantScopesReadRepository(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
	appBody := api.CreateOAuth2ApplicationOptions{
		Name: "oauth-provider-scopes-test",
		RedirectURIs: []string{
			"a",
		},
		ConfidentialClient: true,
	}

	req := NewRequestWithJSON(t, "POST", "/api/v1/user/applications/oauth2", &appBody).
		AddBasicAuth(user.Name)
	resp := MakeRequest(t, req, http.StatusCreated)

	var app *api.OAuth2Application
	DecodeJSON(t, resp, &app)

	grant := &auth_model.OAuth2Grant{
		ApplicationID: app.ID,
		UserID:        user.ID,
		Scope:         "openid profile email read:user read:repository",
	}

	err := db.Insert(db.DefaultContext, grant)
	require.NoError(t, err)

	assert.Contains(t, grant.Scope, "openid profile email read:user read:repository")

	ctx := loginUserWithPasswordRemember(t, user.Name, "password", true)

	authorizeURL := fmt.Sprintf("/login/oauth/authorize?client_id=%s&redirect_uri=a&response_type=code&state=thestate", app.ClientID)
	authorizeReq := NewRequest(t, "GET", authorizeURL)
	authorizeResp := ctx.MakeRequest(t, authorizeReq, http.StatusSeeOther)

	authcode := strings.Split(strings.Split(authorizeResp.Body.String(), "?code=")[1], "&amp")[0]
	grantReq := NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
		"client_id":    app.ClientID,
		"redirect_uri": "a",
		"state":        "thestate",
		"granted":      "true",
	})
	ctx.MakeRequest(t, grantReq, http.StatusBadRequest)

	accessTokenReq := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     app.ClientID,
		"client_secret": app.ClientSecret,
		"redirect_uri":  "a",
		"code":          authcode,
	})
	accessTokenResp := ctx.MakeRequest(t, accessTokenReq, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	parsed := new(response)

	require.NoError(t, json.Unmarshal(accessTokenResp.Body.Bytes(), parsed))
	userReq := NewRequest(t, "GET", "/api/v1/users/user2/repos")
	userReq.SetHeader("Authorization", "Bearer "+parsed.AccessToken)
	userResp := MakeRequest(t, userReq, http.StatusOK)

	type repos struct {
		FullRepoName string `json:"full_name"`
	}
	var userResponse []*repos
	require.NoError(t, json.Unmarshal(userResp.Body.Bytes(), &userResponse))
	if assert.NotEmpty(t, userResponse) {
		assert.Contains(t, userResponse[0].FullRepoName, "user2/repo1")
	}
}

func TestOAuth_GrantScopesReadPrivateGroups(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// setting.OAuth2.EnableAdditionalGrantScopes = true
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user5"})

	appBody := api.CreateOAuth2ApplicationOptions{
		Name: "oauth-provider-scopes-test",
		RedirectURIs: []string{
			"a",
		},
		ConfidentialClient: true,
	}

	appReq := NewRequestWithJSON(t, "POST", "/api/v1/user/applications/oauth2", &appBody).
		AddBasicAuth(user.Name)
	appResp := MakeRequest(t, appReq, http.StatusCreated)

	var app *api.OAuth2Application
	DecodeJSON(t, appResp, &app)

	grant := &auth_model.OAuth2Grant{
		ApplicationID: app.ID,
		UserID:        user.ID,
		Scope:         "openid profile email groups read:user",
	}

	err := db.Insert(db.DefaultContext, grant)
	require.NoError(t, err)

	assert.Contains(t, grant.Scope, "openid profile email groups read:user")

	ctx := loginUserWithPasswordRemember(t, user.Name, "password", true)

	authorizeURL := fmt.Sprintf("/login/oauth/authorize?client_id=%s&redirect_uri=a&response_type=code&state=thestate", app.ClientID)
	authorizeReq := NewRequest(t, "GET", authorizeURL)
	authorizeResp := ctx.MakeRequest(t, authorizeReq, http.StatusSeeOther)

	authcode := strings.Split(strings.Split(authorizeResp.Body.String(), "?code=")[1], "&amp")[0]
	grantReq := NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
		"client_id":    app.ClientID,
		"redirect_uri": "a",
		"state":        "thestate",
		"granted":      "true",
	})
	ctx.MakeRequest(t, grantReq, http.StatusBadRequest)

	accessTokenReq := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     app.ClientID,
		"client_secret": app.ClientSecret,
		"redirect_uri":  "a",
		"code":          authcode,
	})
	accessTokenResp := ctx.MakeRequest(t, accessTokenReq, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token,omitempty"`
	}
	parsed := new(response)
	require.NoError(t, json.Unmarshal(accessTokenResp.Body.Bytes(), parsed))
	parts := strings.Split(parsed.IDToken, ".")

	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	type IDTokenClaims struct {
		Groups []string `json:"groups"`
	}

	claims := new(IDTokenClaims)
	require.NoError(t, json.Unmarshal(payload, claims))
	for _, group := range []string{"limited_org36", "limited_org36:team20writepackage", "org6", "org6:owners", "org7", "org7:owners", "privated_org", "privated_org:team14writeauth"} {
		assert.Contains(t, claims.Groups, group)
	}
}

func TestOAuth_GrantScopesReadOnlyPublicGroups(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	setting.OAuth2.EnableAdditionalGrantScopes = true
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user5"})

	appBody := api.CreateOAuth2ApplicationOptions{
		Name: "oauth-provider-scopes-test",
		RedirectURIs: []string{
			"a",
		},
		ConfidentialClient: true,
	}

	appReq := NewRequestWithJSON(t, "POST", "/api/v1/user/applications/oauth2", &appBody).
		AddBasicAuth(user.Name)
	appResp := MakeRequest(t, appReq, http.StatusCreated)

	var app *api.OAuth2Application
	DecodeJSON(t, appResp, &app)

	grant := &auth_model.OAuth2Grant{
		ApplicationID: app.ID,
		UserID:        user.ID,
		Scope:         "openid profile email groups read:user",
	}

	err := db.Insert(db.DefaultContext, grant)
	require.NoError(t, err)

	assert.Contains(t, grant.Scope, "openid profile email groups read:user")

	ctx := loginUserWithPasswordRemember(t, user.Name, "password", true)

	authorizeURL := fmt.Sprintf("/login/oauth/authorize?client_id=%s&redirect_uri=a&response_type=code&state=thestate", app.ClientID)
	authorizeReq := NewRequest(t, "GET", authorizeURL)
	authorizeResp := ctx.MakeRequest(t, authorizeReq, http.StatusSeeOther)

	authcode := strings.Split(strings.Split(authorizeResp.Body.String(), "?code=")[1], "&amp")[0]
	grantReq := NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
		"client_id":    app.ClientID,
		"redirect_uri": "a",
		"state":        "thestate",
		"granted":      "true",
	})
	ctx.MakeRequest(t, grantReq, http.StatusBadRequest)

	accessTokenReq := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     app.ClientID,
		"client_secret": app.ClientSecret,
		"redirect_uri":  "a",
		"code":          authcode,
	})
	accessTokenResp := ctx.MakeRequest(t, accessTokenReq, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token,omitempty"`
	}
	parsed := new(response)
	require.NoError(t, json.Unmarshal(accessTokenResp.Body.Bytes(), parsed))
	parts := strings.Split(parsed.IDToken, ".")

	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	type IDTokenClaims struct {
		Groups []string `json:"groups"`
	}

	claims := new(IDTokenClaims)
	require.NoError(t, json.Unmarshal(payload, claims))
	for _, privOrg := range []string{"org7", "org7:owners", "privated_org", "privated_org:team14writeauth"} {
		assert.NotContains(t, claims.Groups, privOrg)
	}

	userReq := NewRequest(t, "GET", "/login/oauth/userinfo")
	userReq.SetHeader("Authorization", "Bearer "+parsed.AccessToken)
	userResp := MakeRequest(t, userReq, http.StatusOK)

	type userinfo struct {
		Groups []string `json:"groups"`
	}
	parsedUserInfo := new(userinfo)
	require.NoError(t, json.Unmarshal(userResp.Body.Bytes(), parsedUserInfo))

	for _, privOrg := range []string{"org7", "org7:owners", "privated_org", "privated_org:team14writeauth"} {
		assert.NotContains(t, parsedUserInfo.Groups, privOrg)
	}
}

func TestOAuth_GrantScopesReadPublicGroupsWithTheReadScope(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	setting.OAuth2.EnableAdditionalGrantScopes = true
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user5"})

	appBody := api.CreateOAuth2ApplicationOptions{
		Name: "oauth-provider-scopes-test",
		RedirectURIs: []string{
			"a",
		},
		ConfidentialClient: true,
	}

	appReq := NewRequestWithJSON(t, "POST", "/api/v1/user/applications/oauth2", &appBody).
		AddBasicAuth(user.Name)
	appResp := MakeRequest(t, appReq, http.StatusCreated)

	var app *api.OAuth2Application
	DecodeJSON(t, appResp, &app)

	grant := &auth_model.OAuth2Grant{
		ApplicationID: app.ID,
		UserID:        user.ID,
		Scope:         "openid profile email groups read:user read:organization",
	}

	err := db.Insert(db.DefaultContext, grant)
	require.NoError(t, err)

	assert.Contains(t, grant.Scope, "openid profile email groups read:user read:organization")

	ctx := loginUserWithPasswordRemember(t, user.Name, "password", true)

	authorizeURL := fmt.Sprintf("/login/oauth/authorize?client_id=%s&redirect_uri=a&response_type=code&state=thestate", app.ClientID)
	authorizeReq := NewRequest(t, "GET", authorizeURL)
	authorizeResp := ctx.MakeRequest(t, authorizeReq, http.StatusSeeOther)

	authcode := strings.Split(strings.Split(authorizeResp.Body.String(), "?code=")[1], "&amp")[0]
	grantReq := NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
		"client_id":    app.ClientID,
		"redirect_uri": "a",
		"state":        "thestate",
		"granted":      "true",
	})
	ctx.MakeRequest(t, grantReq, http.StatusBadRequest)

	accessTokenReq := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     app.ClientID,
		"client_secret": app.ClientSecret,
		"redirect_uri":  "a",
		"code":          authcode,
	})
	accessTokenResp := ctx.MakeRequest(t, accessTokenReq, http.StatusOK)
	type response struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token,omitempty"`
	}
	parsed := new(response)
	require.NoError(t, json.Unmarshal(accessTokenResp.Body.Bytes(), parsed))
	parts := strings.Split(parsed.IDToken, ".")

	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	type IDTokenClaims struct {
		Groups []string `json:"groups"`
	}

	claims := new(IDTokenClaims)
	require.NoError(t, json.Unmarshal(payload, claims))
	for _, privOrg := range []string{"org7", "org7:owners", "privated_org", "privated_org:team14writeauth"} {
		assert.Contains(t, claims.Groups, privOrg)
	}

	userReq := NewRequest(t, "GET", "/login/oauth/userinfo")
	userReq.SetHeader("Authorization", "Bearer "+parsed.AccessToken)
	userResp := MakeRequest(t, userReq, http.StatusOK)

	type userinfo struct {
		Groups []string `json:"groups"`
	}
	parsedUserInfo := new(userinfo)
	require.NoError(t, json.Unmarshal(userResp.Body.Bytes(), parsedUserInfo))
	for _, privOrg := range []string{"org7", "org7:owners", "privated_org", "privated_org:team14writeauth"} {
		assert.Contains(t, parsedUserInfo.Groups, privOrg)
	}
}

func TestSignUpViaOAuthDefaultRestricted(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.OAuth2Client.EnableAutoRegistration, true)()
	defer test.MockVariableValue(&setting.Service.DefaultUserIsRestricted, true)()

	gitlabName := "gitlab"
	addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))
	userGitLabUserID := "BB(5)=47176870"

	defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
		return goth.User{
			Provider: gitlabName,
			UserID:   userGitLabUserID,
			Name:     "gitlab-user",
			NickName: "gitlab-user",
			Email:    "gitlab@example.com",
		}, nil
	})()
	req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
	resp := MakeRequest(t, req, http.StatusSeeOther)
	assert.Equal(t, "/", test.RedirectURL(resp))

	unittest.AssertExistsIf(t, true, &user_model.User{Name: "gitlab-user"}, "is_restricted = true")
}

func TestSignUpViaOAuthLinking2FA(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.OAuth2Client.EnableAutoRegistration, true)()
	defer test.MockVariableValue(&setting.OAuth2Client.AccountLinking, setting.OAuth2AccountLinkingAuto)()

	gitlabName := "gitlab"
	addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))

	t.Run("WebAuthn", func(t *testing.T) {
		// Fake that user 2 is enrolled into WebAuthn.
		t.Cleanup(func() {
			unittest.AssertSuccessfulDelete(t, &auth_model.WebAuthnCredential{UserID: 2})
		})
		unittest.AssertSuccessfulInsert(t, &auth_model.WebAuthnCredential{UserID: 2})

		defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
			return goth.User{
				Provider: gitlabName,
				UserID:   "BB(4)=107",
				NickName: "user2",
				Email:    "user2@example.com",
			}, nil
		})()
		req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
		resp := MakeRequest(t, req, http.StatusSeeOther)

		// Make sure the user has to go through 2FA after linking.
		assert.Equal(t, "/user/webauthn", test.RedirectURL(resp))
	})

	t.Run("Case-insensitive username", func(t *testing.T) {
		defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
			return goth.User{
				Provider: gitlabName,
				UserID:   "BB(3)=21",
				NickName: "UsEr4",
				Email:    "user4@example.org",
			}, nil
		})()
		req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
		resp := MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/", test.RedirectURL(resp))
	})
}

func TestSignUpViaOAuth2FA(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.OAuth2Client.EnableAutoRegistration, true)()
	defer test.MockVariableValue(&setting.OAuth2Client.AccountLinking, setting.OAuth2AccountLinkingAuto)()

	gitlabName := "gitlab"
	addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))
	userGitLabUserID := "BB(3)=21"

	defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
		return goth.User{
			Provider: gitlabName,
			UserID:   userGitLabUserID,
			NickName: "user2",
			Email:    "user2@example.com",
		}, nil
	})()
	req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
	resp := MakeRequest(t, req, http.StatusSeeOther)

	// Make sure the user can login normally and is linked.
	assert.Equal(t, "/", test.RedirectURL(resp))

	// Fake that user 2 is enrolled into WebAuthn.
	t.Cleanup(func() {
		unittest.AssertSuccessfulDelete(t, &auth_model.WebAuthnCredential{UserID: 2})
	})
	unittest.AssertSuccessfulInsert(t, &auth_model.WebAuthnCredential{UserID: 2})

	req = NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
	resp = MakeRequest(t, req, http.StatusSeeOther)

	// Make sure user has to go through 2FA.
	assert.Equal(t, "/user/webauthn", test.RedirectURL(resp))
}

func TestAccessTokenWithPKCE(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user4")

	t.Run("Plain method", func(t *testing.T) {
		defer unittest.AssertSuccessfulDelete(t, &auth_model.OAuth2Grant{UserID: 4, ApplicationID: 2})

		var u *url.URL
		t.Run("Grant", func(t *testing.T) {
			session.MakeRequest(t, NewRequest(t, "GET", "/login/oauth/authorize?client_id=ce5a1322-42a7-11ed-b878-0242ac120002&redirect_uri=b&response_type=code&code_challenge_method=plain&code_challenge=CODE&state=thestate"), http.StatusOK)
			req := NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
				"client_id":    "ce5a1322-42a7-11ed-b878-0242ac120002",
				"redirect_uri": "b",
				"state":        "thestate",
				"granted":      "true",
			})
			resp := session.MakeRequest(t, req, http.StatusSeeOther)

			var err error
			u, err = url.Parse(test.RedirectURL(resp))
			require.NoError(t, err)
		})

		t.Run("Incorrect code verifier", func(t *testing.T) {
			req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
				"client_id":     "ce5a1322-42a7-11ed-b878-0242ac120002",
				"code":          u.Query().Get("code"),
				"code_verifier": "just a guess",
				"grant_type":    "authorization_code",
				"redirect_uri":  "b",
			})
			resp := MakeRequest(t, req, http.StatusBadRequest)

			var respBody map[string]any
			DecodeJSON(t, resp, &respBody)

			if assert.Len(t, respBody, 2) {
				assert.Equal(t, "unauthorized_client", respBody["error"])
				assert.Equal(t, "failed PKCE code challenge", respBody["error_description"])
			}
		})

		t.Run("Get access token", func(t *testing.T) {
			req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
				"client_id":     "ce5a1322-42a7-11ed-b878-0242ac120002",
				"code":          u.Query().Get("code"),
				"code_verifier": "CODE",
				"grant_type":    "authorization_code",
				"redirect_uri":  "b",
			})
			resp := MakeRequest(t, req, http.StatusOK)

			var respBody map[string]any
			DecodeJSON(t, resp, &respBody)

			if assert.Len(t, respBody, 4) {
				assert.NotEmpty(t, respBody["access_token"])
				assert.NotEmpty(t, respBody["token_type"])
				assert.NotEmpty(t, respBody["expires_in"])
				assert.NotEmpty(t, respBody["refresh_token"])
			}
		})
	})

	t.Run("S256 method", func(t *testing.T) {
		var u *url.URL
		t.Run("Grant", func(t *testing.T) {
			h := sha256.Sum256([]byte("CODE"))
			hashedVerifier := base64.RawURLEncoding.EncodeToString(h[:])

			session.MakeRequest(t, NewRequest(t, "GET", "/login/oauth/authorize?client_id=ce5a1322-42a7-11ed-b878-0242ac120002&redirect_uri=b&response_type=code&code_challenge_method=S256&code_challenge="+hashedVerifier+"&state=thestate"), http.StatusOK)
			req := NewRequestWithValues(t, "POST", "/login/oauth/grant", map[string]string{
				"client_id":    "ce5a1322-42a7-11ed-b878-0242ac120002",
				"redirect_uri": "b",
				"state":        "thestate",
				"granted":      "true",
			})
			resp := session.MakeRequest(t, req, http.StatusSeeOther)

			var err error
			u, err = url.Parse(test.RedirectURL(resp))
			require.NoError(t, err)
		})

		t.Run("Incorrect code verifier", func(t *testing.T) {
			req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
				"client_id":     "ce5a1322-42a7-11ed-b878-0242ac120002",
				"code":          u.Query().Get("code"),
				"code_verifier": "just a guess",
				"grant_type":    "authorization_code",
				"redirect_uri":  "b",
			})
			resp := MakeRequest(t, req, http.StatusBadRequest)

			var respBody map[string]any
			DecodeJSON(t, resp, &respBody)

			if assert.Len(t, respBody, 2) {
				assert.Equal(t, "unauthorized_client", respBody["error"])
				assert.Equal(t, "failed PKCE code challenge", respBody["error_description"])
			}
		})

		t.Run("Get access token", func(t *testing.T) {
			req := NewRequestWithValues(t, "POST", "/login/oauth/access_token", map[string]string{
				"client_id":     "ce5a1322-42a7-11ed-b878-0242ac120002",
				"code":          u.Query().Get("code"),
				"code_verifier": "CODE",
				"grant_type":    "authorization_code",
				"redirect_uri":  "b",
			})
			resp := MakeRequest(t, req, http.StatusOK)

			var respBody map[string]any
			DecodeJSON(t, resp, &respBody)

			if assert.Len(t, respBody, 4) {
				assert.NotEmpty(t, respBody["access_token"])
				assert.NotEmpty(t, respBody["token_type"])
				assert.NotEmpty(t, respBody["expires_in"])
				assert.NotEmpty(t, respBody["refresh_token"])
			}
		})
	})
}

func TestSignInOAuthCallbackGothUserFields(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// OAuth2 authentication source GitLab
	gitlabName := "gitlab"
	gitlab := addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))

	// Create a user as if it had been previously created by the GitLab
	// authentication source.
	userGitLabUserID := "5678"
	userGitLab := &user_model.User{
		Name:        "gitlabuser",
		Email:       "gitlabuser@example.com",
		Passwd:      "gitlabuserpassword",
		Type:        user_model.UserTypeIndividual,
		LoginType:   auth_model.OAuth2,
		LoginSource: gitlab.ID,
		LoginName:   userGitLabUserID,
	}
	defer createUser(t.Context(), t, userGitLab)()

	t.Run("Callback with all gothUser fields", func(t *testing.T) {
		// Set up log checker to verify trace logs
		logChecker, cleanup := test.NewLogChecker(log.DEFAULT, log.TRACE)
		defer cleanup()
		logChecker.Filter(
			"OAuth2 Provider gitlab returned gothUser",
			"OAuth2 Provider gitlab RawData:",
			"OAuth2 Provider gitlab IDToken",
		)

		// Return a goth.User with all fields populated including RawData and IDToken
		defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
			return goth.User{
				Provider:  gitlabName,
				UserID:    userGitLabUserID,
				Email:     userGitLab.Email,
				NickName:  "gitlabnick",
				Name:      "GitLab User",
				FirstName: "GitLab",
				LastName:  "User",
				AvatarURL: "https://example.com/avatar.png",
				RawData: map[string]any{
					"sub":             userGitLabUserID,
					"groups":          []string{"group1", "group2"},
					"custom_claim":    "custom_value",
					"nested_claim":    map[string]any{"key": "value"},
					"array_of_values": []any{"val1", "val2", "val3"},
				},
				IDToken: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.test",
			}, nil
		})()

		req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
		resp := MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/", test.RedirectURL(resp))

		// Verify the user was logged in successfully
		userAfterLogin := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: userGitLab.ID})
		assert.Greater(t, userAfterLogin.LastLoginUnix, userGitLab.LastLoginUnix)

		// Verify all trace logs were outputted
		logFiltered, _ := logChecker.Check(5 * time.Second)
		assert.True(t, logFiltered[0], "Expected trace log with gothUser fields")
		assert.True(t, logFiltered[1], "Expected trace log with RawData")
		assert.True(t, logFiltered[2], "Expected trace log with IDToken")
	})

	t.Run("Callback with minimal gothUser fields", func(t *testing.T) {
		// Set up log checker to verify trace logs
		logChecker, cleanup := test.NewLogChecker(log.DEFAULT, log.TRACE)
		defer cleanup()
		logChecker.Filter(
			"OAuth2 Provider gitlab returned gothUser",
		)

		// Return a goth.User with only required fields (no RawData or IDToken)
		defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
			return goth.User{
				Provider: gitlabName,
				UserID:   userGitLabUserID,
				Email:    userGitLab.Email,
			}, nil
		})()

		req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
		resp := MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/", test.RedirectURL(resp))

		// Verify basic trace log was outputted (but not RawData or IDToken logs)
		logFiltered, _ := logChecker.Check(5 * time.Second)
		assert.True(t, logFiltered[0], "Expected trace log with gothUser fields")
	})

	t.Run("Callback with RawData but no IDToken", func(t *testing.T) {
		// Set up log checker to verify trace logs
		logChecker, cleanup := test.NewLogChecker(log.DEFAULT, log.TRACE)
		defer cleanup()
		logChecker.Filter(
			"OAuth2 Provider gitlab returned gothUser",
			"OAuth2 Provider gitlab RawData:",
		)

		defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
			return goth.User{
				Provider: gitlabName,
				UserID:   userGitLabUserID,
				Email:    userGitLab.Email,
				RawData: map[string]any{
					"sub":    userGitLabUserID,
					"groups": []string{"developers", "admins"},
				},
			}, nil
		})()

		req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
		resp := MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/", test.RedirectURL(resp))

		// Verify gothUser and RawData logs were outputted (but not IDToken log)
		logFiltered, _ := logChecker.Check(5 * time.Second)
		assert.True(t, logFiltered[0], "Expected trace log with gothUser fields")
		assert.True(t, logFiltered[1], "Expected trace log with RawData")
	})

	t.Run("Callback with IDToken but no RawData", func(t *testing.T) {
		// Set up log checker to verify trace logs
		logChecker, cleanup := test.NewLogChecker(log.DEFAULT, log.TRACE)
		defer cleanup()
		logChecker.Filter(
			"OAuth2 Provider gitlab returned gothUser",
			"OAuth2 Provider gitlab IDToken",
		)

		defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
			return goth.User{
				Provider: gitlabName,
				UserID:   userGitLabUserID,
				Email:    userGitLab.Email,
				IDToken:  "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.test",
			}, nil
		})()

		req := NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName))
		resp := MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "/", test.RedirectURL(resp))

		// Verify gothUser and IDToken logs were outputted (but not RawData log)
		logFiltered, _ := logChecker.Check(5 * time.Second)
		assert.True(t, logFiltered[0], "Expected trace log with gothUser fields")
		assert.True(t, logFiltered[1], "Expected trace log with IDToken")
	})
}

func TestSignInOAuthCallbackSignInRetrieveError(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	gitlabName := "gitlab"
	gitlab := addAuthSource(t, authSourcePayloadGitLabCustom(gitlabName))

	userGitLabUserID := "5678"
	userGitLab := &user_model.User{
		Name:        "gitlabuser",
		Email:       "gitlabuser@example.com",
		Passwd:      "gitlabuserpassword",
		Type:        user_model.UserTypeIndividual,
		LoginType:   auth_model.OAuth2,
		LoginSource: gitlab.ID,
		LoginName:   userGitLabUserID,
	}
	defer createUser(t.Context(), t, userGitLab)()

	defer mockCompleteUserAuth(func(res http.ResponseWriter, req *http.Request) (goth.User, error) {
		return goth.User{}, &go_oauth2.RetrieveError{
			Response: &http.Response{
				Status: "404 Not Found",
			},
			Body: []byte("cooked"),
		}
	})()
	sess := emptyTestSession(t)
	resp := sess.MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/user/oauth2/%s/callback?code=XYZ&state=XYZ", gitlabName)), http.StatusSeeOther)

	assert.Equal(t, "/user/login", test.RedirectURL(resp))
	flashCookie := sess.GetCookie(app_context.CookieNameFlash)
	assert.NotNil(t, flashCookie)
	assert.Equal(t, "error%3DOAuth2%2BRetrieveError%253A%2Boauth2%253A%2Bcannot%2Bfetch%2Btoken%253A%2B404%2BNot%2BFound%250AResponse%253A%2Bcooked", flashCookie.Value)
}

// The desktop uses a public client and real S256 exchange. Enrollment metadata
// cannot replace that authorization or activate execution/publication.
func TestNativeDesktopEnrollment(t *testing.T) {
	if os.Getenv("FACTORY_FOUNTAIN_NATIVE_PRODUCT_UI") == "1" {
		// Real fixture file/branch creation needs the standard live hook server
		// and its exact repository-root configuration, just like other Git tests.
		onApplicationRun(t, func(t *testing.T, _ *url.URL) { testNativeDesktopEnrollment(t) })
		return
	}
	defer tests.PrepareTestEnv(t)()
	testNativeDesktopEnrollment(t)
}

func testNativeDesktopEnrollment(t *testing.T) {
	defer test.MockVariableValue(&setting.OAuth2.EnableAdditionalGrantScopes, true)()
	clientID := "ce5a1322-42a7-11ed-b878-0242ac120002"
	redirect := "http://127.0.0.1/"
	MakeRequest(t, NewRequest(t, http.MethodGet, "/api/v1/factory/desktop/oauth"), http.StatusNotFound)
	defer test.MockVariableValue(&setting.OAuth2.FactoryDesktopClientID, clientID)()
	discovery := MakeRequest(t, NewRequest(t, http.MethodGet, "/api/v1/factory/desktop/oauth"), http.StatusOK)
	var configured map[string]string
	DecodeJSON(t, discovery, &configured)
	require.Equal(t, clientID, configured["client_id"])
	require.Equal(t, "read:repository", configured["scope"])
	collaborationDiscovery := MakeRequest(t, NewRequest(t, http.MethodGet, "/api/v1/factory/desktop/oauth").SetHeader("X-Factory-Desktop-Protocol", "2"), http.StatusOK)
	DecodeJSON(t, collaborationDiscovery, &configured)
	require.Equal(t, "read:repository read:issue", configured["scope"])
	MakeRequest(t, NewRequest(t, http.MethodGet, "/api/v1/factory/desktop/oauth").SetHeader("X-Factory-Desktop-Protocol", "unknown"), http.StatusBadRequest)
	verifier := strings.Repeat("v", 43)
	digest := sha256.Sum256([]byte(verifier))
	scope := "read:repository"
	if os.Getenv("FACTORY_FOUNTAIN_NATIVE_UI") == "1" {
		// The installed reader explicitly requests protocol 2's issue scope.
		// Use the same fixture consent without broadening any retained grant.
		scope = "read:repository read:issue"
	}
	values := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {scope}, "state": {"desktop-fixture"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}}
	session := loginUser(t, "user2")
	session.MakeRequest(t, NewRequest(t, http.MethodGet, "/login/oauth/authorize?"+values.Encode()), http.StatusOK)
	consent := session.MakeRequest(t, NewRequestWithValues(t, http.MethodPost, "/login/oauth/grant", map[string]string{"client_id": clientID, "redirect_uri": redirect, "state": "desktop-fixture", "granted": "true", "scope": scope}), http.StatusSeeOther)
	location, err := consent.Result().Location()
	require.NoError(t, err)
	require.Equal(t, "desktop-fixture", location.Query().Get("state"))
	code := location.Query().Get("code")
	require.NotEmpty(t, code)
	exchange := MakeRequest(t, NewRequestWithValues(t, http.MethodPost, "/login/oauth/access_token", map[string]string{"grant_type": "authorization_code", "client_id": clientID, "redirect_uri": redirect, "code": code, "code_verifier": verifier}), http.StatusOK)
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	DecodeJSON(t, exchange, &tokens)
	require.NotEmpty(t, tokens.AccessToken)
	base := "/api/v1/repos/user2/repo1/factory/desktop/grants"
	input := extension.DesktopGrantEnrollment{CommandID: strings.Repeat("a", 32), RuntimeID: strings.Repeat("b", 32)}
	enroll := func(status int) extension.DesktopGrant {
		reply := MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base, input).SetHeader("Authorization", "Bearer "+tokens.AccessToken), status)
		var grant extension.DesktopGrant
		DecodeJSON(t, reply, &grant)
		require.Equal(t, "no-store", reply.Header().Get("Cache-Control"))
		require.NotContains(t, reply.Body.String(), tokens.AccessToken)
		return grant
	}
	grant := enroll(http.StatusCreated)
	require.True(t, grant.Active)
	require.Equal(t, input.CommandID, grant.ID)
	require.Equal(t, input.RuntimeID, grant.RuntimeID)
	require.Equal(t, clientID, grant.ApplicationID)
	require.Equal(t, "user2", grant.Actor.Username)
	require.Equal(t, []string{extension.DesktopRepositoryRead}, grant.Operations)
	require.Equal(t, grant, enroll(http.StatusOK))
	read := MakeRequest(t, NewRequest(t, http.MethodGet, base+"/"+grant.ID+"/repository").SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusOK)
	var repository extension.Repository
	DecodeJSON(t, read, &repository)
	require.Equal(t, grant.Repository, repository)
	sourceSelection := map[string]any{"source_commit": "", "source_paths": []string{}, "source_actors": []string{grant.Actor.ID}}
	sourceReply := MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base+"/"+grant.ID+"/source", sourceSelection).SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusOK)
	var source extension.RepositorySource
	DecodeJSON(t, sourceReply, &source)
	require.Equal(t, grant.Repository.ID, source.RepositoryID)
	require.Len(t, source.Commit, 40)
	require.NotEmpty(t, source.Bundle)
	require.True(t, source.Writers[grant.Actor.ID])
	MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base+"/"+grant.ID+"/source", map[string]any{"source_paths": []string{"../README.md"}}).SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusBadRequest)
	MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base+"/"+grant.ID+"/source", map[string]any{"repository_id": "2"}).SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusBadRequest)
	withdrawn := MakeRequest(t, NewRequest(t, http.MethodDelete, base+"/"+grant.ID).SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusOK)
	var retired extension.DesktopGrant
	DecodeJSON(t, withdrawn, &retired)
	require.False(t, retired.Active)
	MakeRequest(t, NewRequest(t, http.MethodGet, base+"/"+grant.ID+"/repository").SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusForbidden)
	MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base+"/"+grant.ID+"/source", sourceSelection).SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusForbidden)
	require.False(t, enroll(http.StatusOK).Active)
	readback := MakeRequest(t, NewRequest(t, http.MethodGet, base+"/"+grant.ID).SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusOK)
	var retained extension.DesktopGrant
	DecodeJSON(t, readback, &retained)
	require.False(t, retained.Active)

	// A shared OAuth grant cannot substitute for the native profile proof.
	proof := strings.Repeat("c", 64)
	proofDigest := sha256.Sum256([]byte(proof))
	input.CommandID = strings.Repeat("d", 32)
	input.ProofSHA256 = hex.EncodeToString(proofDigest[:])
	MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base, input).SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusForbidden)
	bound := MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base, input).SetHeader("Authorization", "Bearer "+tokens.AccessToken).SetHeader("X-Factory-Desktop-Proof", proof), http.StatusCreated)
	var binding extension.DesktopGrant
	DecodeJSON(t, bound, &binding)
	require.True(t, binding.RuntimeBound)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		MakeRequest(t, NewRequest(t, method, base+"/"+binding.ID).SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusForbidden)
	}
	MakeRequest(t, NewRequest(t, http.MethodGet, base+"/"+binding.ID+"/repository").SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusForbidden)
	MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, base+"/"+binding.ID+"/source", sourceSelection).SetHeader("Authorization", "Bearer "+tokens.AccessToken), http.StatusForbidden)
	require.NotContains(t, bound.Body.String(), proof)

	t.Run("FiniteControllerAuthority", func(t *testing.T) {
		// Read login and native profile proof still cannot request control.
		MakeRequest(t, NewRequest(t, http.MethodGet, "/api/v1/factory/desktop/oauth/control"), http.StatusNotFound)
		defer test.MockVariableValue(&setting.OAuth2.FactoryAppliancePackageID, "soda")()
		appResponse := MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, "/api/v1/user/applications/oauth2", api.CreateOAuth2ApplicationOptions{Name: "Factory control fixture", RedirectURIs: []string{redirect}, ConfidentialClient: false}).AddBasicAuth("user2"), http.StatusCreated)
		var controlApp api.OAuth2Application
		DecodeJSON(t, appResponse, &controlApp)
		defer test.MockVariableValue(&setting.OAuth2.FactoryDesktopControlClientID, controlApp.ClientID)()
		controlDiscovery := MakeRequest(t, NewRequest(t, http.MethodGet, "/api/v1/factory/desktop/oauth/control"), http.StatusOK)
		DecodeJSON(t, controlDiscovery, &configured)
		require.Equal(t, "write:repository", configured["scope"])
		collaborationDiscovery := MakeRequest(t, NewRequest(t, http.MethodGet, "/api/v1/factory/desktop/oauth/control").SetHeader("X-Factory-Desktop-Protocol", "2"), http.StatusOK)
		DecodeJSON(t, collaborationDiscovery, &configured)
		require.Equal(t, "write:repository write:issue", configured["scope"])
		selected := base + "/" + binding.ID
		selection := extension.RuntimeSelection{CommandID: strings.Repeat("1", 32), Action: "activate", NotAfter: time.Now().Unix() + 120}
		MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, selected+"/placement", selection).SetHeader("Authorization", "Bearer "+tokens.AccessToken).SetHeader("X-Factory-Desktop-Proof", proof), http.StatusForbidden)
		values.Set("scope", "write:repository write:issue")
		values.Set("client_id", controlApp.ClientID)
		session.MakeRequest(t, NewRequest(t, http.MethodGet, "/login/oauth/authorize?"+values.Encode()), http.StatusOK)
		consent := session.MakeRequest(t, NewRequestWithValues(t, http.MethodPost, "/login/oauth/grant", map[string]string{"client_id": controlApp.ClientID, "redirect_uri": redirect, "state": "desktop-fixture", "granted": "true", "scope": "write:repository write:issue"}), http.StatusSeeOther)
		location, err := consent.Result().Location()
		require.NoError(t, err)
		require.Empty(t, location.Query().Get("error"), location.Query().Get("error_description"))
		exchange := MakeRequest(t, NewRequestWithValues(t, http.MethodPost, "/login/oauth/access_token", map[string]string{"grant_type": "authorization_code", "client_id": controlApp.ClientID, "redirect_uri": redirect, "code": location.Query().Get("code"), "code_verifier": verifier}), http.StatusOK)
		var controlTokens struct {
			AccessToken string `json:"access_token"`
		}
		DecodeJSON(t, exchange, &controlTokens)
		call := func(method, path string, body any, status int) *httptest.ResponseRecorder {
			var request *RequestWrapper
			if body == nil {
				request = NewRequest(t, method, path)
			} else {
				request = NewRequestWithJSON(t, method, path, body)
			}
			return MakeRequest(t, request.SetHeader("Authorization", "Bearer "+controlTokens.AccessToken).SetHeader("X-Factory-Desktop-Proof", proof), status)
		}
		// Control has its own application authorization and bound enrollment;
		// the original read authorization and enrollment remain intact.
		controlInput := input
		controlInput.CommandID = strings.Repeat("e", 32)
		DecodeJSON(t, call(http.MethodPost, base, controlInput, http.StatusCreated), &binding)
		selected = base + "/" + binding.ID
		var selectedReceipt extension.RuntimeSelectionReceipt
		DecodeJSON(t, call(http.MethodPost, selected+"/placement", selection, http.StatusOK), &selectedReceipt)
		require.EqualValues(t, 1, selectedReceipt.Placement.Generation)
		require.Equal(t, "active", selectedReceipt.Placement.State)
		var replay extension.RuntimeSelectionReceipt
		DecodeJSON(t, call(http.MethodPost, selected+"/placement", selection, http.StatusOK), &replay)
		require.Equal(t, selectedReceipt, replay, "lost initial selection reply adopts its exact journal receipt")
		changed := selection
		changed.NotAfter++
		call(http.MethodPost, selected+"/placement", changed, http.StatusConflict)
		competing := input
		competing.CommandID = strings.Repeat("2", 32)
		competing.RuntimeID = strings.Repeat("3", 32)
		var other extension.DesktopGrant
		DecodeJSON(t, call(http.MethodPost, base, competing, http.StatusCreated), &other)
		changed.CommandID = strings.Repeat("4", 32)
		changed.ExpectedRevision = selectedReceipt.Placement.Revision
		var refusal extension.RuntimeSelectionReceipt
		DecodeJSON(t, call(http.MethodPost, base+"/"+other.ID+"/placement", changed, http.StatusOK), &refusal)
		require.Equal(t, "refused", refusal.Outcome)
		require.Equal(t, selectedReceipt.Placement, refusal.Placement)
		authorize := extension.DesktopOperationAuthorization{CommandID: strings.Repeat("5", 32), ExpectedPlacement: selectedReceipt.Placement.Revision, Generation: 1, Operations: []string{"execution.launch", extension.OperationKindLifecycleNote}, NotAfter: time.Now().Unix() + 120}
		publishCheck := os.Getenv("FACTORY_FOUNTAIN_PUBLISH_CHECK") == "1"
		if publishCheck {
			authorize.Operations = []string{extension.OperationKindRefPublish}
		}
		var operationGrant extension.DesktopOperationGrant
		DecodeJSON(t, call(http.MethodPost, selected+"/operations", authorize, http.StatusOK), &operationGrant)
		require.True(t, operationGrant.Active)
		opBase := selected + "/operations/" + operationGrant.ID
		if publishCheck {
			repo, err := repo_model.GetRepositoryByID(t.Context(), 1)
			require.NoError(t, err)
			require.NoError(t, repo.LoadOwner(t.Context()))
			server := httptest.NewServer(testWebRoutes)
			defer server.Close()
			configuration, err := setting.NewConfigProviderFromFile(setting.CustomConf)
			require.NoError(t, err)
			configuration.Section("server").Key("LOCAL_ROOT_URL").SetValue(server.URL + "/")
			configuration.Section("repository").Key("ROOT").SetValue(setting.RepoRootPath)
			configuration.Section("security").Key("INTERNAL_TOKEN").SetValue(setting.InternalToken)
			configuration.Section("security").DeleteKey("INTERNAL_TOKEN_URI")
			hookConfig := filepath.Join(t.TempDir(), "hooks.ini")
			require.NoError(t, configuration.SaveTo(hookConfig))
			require.NoError(t, os.Chmod(hookConfig, 0o600))
			binary := os.Getenv("FACTORY_FOUNTAIN_PUBLISH_BINARY")
			require.True(t, filepath.IsAbs(binary))
			defer test.MockVariableValue(&setting.AppPath, binary)()
			defer test.MockVariableValue(&setting.CustomConf, hookConfig)()
			require.NoError(t, repo_module.CreateDelegateHooks(repo.RepoPath()))
			client := filepath.Join(t.TempDir(), "candidate.git")
			output, err := exec.Command("git", "clone", "--bare", repo.RepoPath(), client).CombinedOutput()
			require.NoError(t, err, string(output))
			git := func(arguments ...string) []byte {
				t.Helper()
				out, e := exec.Command("git", append([]string{"-C", client}, arguments...)...).CombinedOutput()
				require.NoError(t, e, string(out))
				return bytes.TrimSpace(out)
			}
			baseOID := string(git("rev-parse", "refs/heads/master"))
			tree := string(git("rev-parse", "refs/heads/master^{tree}"))
			candidate := string(git("-c", "user.name=Native publish fixture", "-c", "user.email=publish@example.invalid", "commit-tree", tree, "-p", baseOID, "-m", "Q21 exact candidate"))
			var revision struct {
				Revision int64 `json:"revision"`
			}
			DecodeJSON(t, call(http.MethodGet, selected+"/native-revision", nil, http.StatusOK), &revision)
			payload, err := json.Marshal(extension.PublishPayload{Ref: "refs/heads/q21-public-grant", ExpectedOld: extension.PublishExpectedOldAbsent, NewOID: candidate, ComparisonRef: "refs/heads/master", ExpectedComparisonOID: baseOID})
			require.NoError(t, err)
			intent := extension.OperationIntent{ControllerGrantID: operationGrant.ID, OperationID: "q21-public-grant-1", InstallationID: operationGrant.InstallationID, ActorID: binding.Actor.ID, RepositoryID: binding.Repository.ID, Kind: extension.OperationKindRefPublish, AuthorizationRevision: operationGrant.ID, ExpectedNativeRevision: revision.Revision, NotAfter: authorize.NotAfter, Payload: payload}
			var registered extension.OperationRecord
			DecodeJSON(t, call(http.MethodPost, opBase+"/submit", intent, http.StatusOK), &registered)
			require.Equal(t, extension.EffectPending, registered.EffectState)
			packCommand := exec.Command("git", "-C", client, "pack-objects", "--stdout", "--revs")
			packCommand.Stdin = strings.NewReader(candidate + "\n^" + baseOID + "\n")
			pack, err := packCommand.Output()
			require.NoError(t, err)
			line := strings.Repeat("0", 40) + " " + candidate + " refs/heads/q21-public-grant\x00report-status\n"
			wire := append([]byte(fmt.Sprintf("%04x%s0000", len(line)+4, line)), pack...)
			receive := func(nativeProof string, status int) *httptest.ResponseRecorder {
				r := NewRequestWithBody(t, http.MethodPost, "/user2/repo1.git/git-receive-pack", bytes.NewReader(wire)).SetHeader("Content-Type", "application/x-git-receive-pack-request").SetHeader("Authorization", "Bearer "+controlTokens.AccessToken).SetHeader("X-Factory-Operation-Grant", operationGrant.ID).SetHeader("X-Forgejo-Operation", intent.OperationID).SetHeader("X-Factory-Desktop-Proof", nativeProof)
				return MakeRequest(t, r, status)
			}
			receive(strings.Repeat("f", 64), http.StatusForbidden)
			response := receive(proof, http.StatusOK)
			var lookup struct {
				Record extension.OperationRecord `json:"record"`
			}
			DecodeJSON(t, call(http.MethodGet, opBase+"/journal/"+intent.OperationID, nil, http.StatusOK), &lookup)
			t.Logf("Original conditional receive: effect=%s completion=%s reason=%s receipt=%s", lookup.Record.EffectState, lookup.Record.CompletionState, lookup.Record.ReasonCode, lookup.Record.Receipt)
			require.Contains(t, response.Body.String(), "ok refs/heads/q21-public-grant")
			require.Equal(t, extension.EffectCommitted, lookup.Record.EffectState)
			require.Equal(t, extension.CompletionComplete, lookup.Record.CompletionState)
			require.Contains(t, string(lookup.Record.Receipt), candidate)
			replay := receive(proof, http.StatusConflict)
			require.Contains(t, replay.Body.String(), "already_committed")
			t.Log("PASS: real OAuth/native proof/controller grant conditional Git receive, exact head/base, wrong-profile refusal, committed host receipt and replay without a second receiver; local candidate declared, no worker or inference")
			return
		}
		func() {
			defer test.MockVariableValue(&setting.OAuth2.FactoryDesktopControlClientID, "")()
			var unavailable extension.DesktopOperationGrant
			DecodeJSON(t, call(http.MethodGet, opBase, nil, http.StatusOK), &unavailable)
			require.False(t, unavailable.Active, "operator configuration withdrawal stops new admission while preserving metadata")
			call(http.MethodPost, opBase+"/claims", extension.ExecutionClaimRequest{CommandID: strings.Repeat("0", 32), ManifestDigest: strings.Repeat("1", 64), NotAfter: authorize.NotAfter}, http.StatusForbidden)
		}()
		claimRequest := extension.ExecutionClaimRequest{CommandID: strings.Repeat("6", 32), ManifestDigest: strings.Repeat("7", 64), NotAfter: authorize.NotAfter}
		var claim extension.ExecutionClaimReceipt
		claimPath := opBase + "/claims/" + claimRequest.CommandID
		call(http.MethodGet, claimPath, nil, http.StatusNotFound)
		DecodeJSON(t, call(http.MethodPost, opBase+"/claims", claimRequest, http.StatusOK), &claim)
		require.True(t, claim.Acquired)
		originalClaim := claim
		originalClaim.Acquired = false
		DecodeJSON(t, call(http.MethodGet, claimPath, nil, http.StatusOK), &claim)
		require.Equal(t, originalClaim, claim, "lookup observes exact original scope without acquiring dispatch")
		DecodeJSON(t, call(http.MethodPost, opBase+"/claims", claimRequest, http.StatusOK), &claim)
		require.False(t, claim.Acquired, "same claim replay never authorizes another native launch")
		var revision struct {
			Revision int64 `json:"revision"`
		}
		DecodeJSON(t, call(http.MethodGet, selected+"/native-revision", nil, http.StatusOK), &revision)
		payload, err := json.Marshal(extension.LifecycleNotePayload{IssueID: 5, Content: "Controller qualification receipt", MergeRef: "qualification-no-merge"})
		require.NoError(t, err)
		intent := extension.OperationIntent{ControllerGrantID: operationGrant.ID, OperationID: "controller-comment-1", InstallationID: operationGrant.InstallationID, ActorID: binding.Actor.ID, RepositoryID: binding.Repository.ID,
			Kind: extension.OperationKindLifecycleNote, AuthorizationRevision: operationGrant.ID, ExpectedNativeRevision: revision.Revision, NotAfter: authorize.NotAfter, Payload: payload}
		var record extension.OperationRecord
		DecodeJSON(t, call(http.MethodPost, opBase+"/submit", intent, http.StatusOK), &record)
		require.Equal(t, extension.EffectCommitted, record.EffectState)
		require.Equal(t, extension.CompletionComplete, record.CompletionState)
		require.Equal(t, operationGrant.ID, record.ControllerGrantID)
		var original extension.OperationRecord
		DecodeJSON(t, call(http.MethodPost, opBase+"/submit", intent, http.StatusOK), &original)
		require.Equal(t, record, original, "comment publication replays its original receipt without another write")
		otherAuthorization := authorize
		otherAuthorization.CommandID = strings.Repeat("f", 32)
		var otherOperationGrant extension.DesktopOperationGrant
		DecodeJSON(t, call(http.MethodPost, selected+"/operations", otherAuthorization, http.StatusOK), &otherOperationGrant)
		call(http.MethodGet, selected+"/operations/"+otherOperationGrant.ID+"/claims/"+claim.CommandID, nil, http.StatusNotFound)
		changedIntent := intent
		changedIntent.ControllerGrantID = otherOperationGrant.ID
		call(http.MethodPost, selected+"/operations/"+otherOperationGrant.ID+"/submit", changedIntent, http.StatusConflict)
		// A real finite grant expires independently of the still-active
		// controller. Identical replay observes it; a new deadline conflicts.
		expiring := authorize
		expiring.CommandID = strings.Repeat("d", 32)
		expiring.NotAfter = time.Now().Unix() + 2
		var expired extension.DesktopOperationGrant
		DecodeJSON(t, call(http.MethodPost, selected+"/operations", expiring, http.StatusOK), &expired)
		expiringPath := selected + "/operations/" + expired.ID + "/claims"
		expiringRequest := extension.ExecutionClaimRequest{CommandID: strings.Repeat("e", 32), ManifestDigest: strings.Repeat("2", 64), NotAfter: expiring.NotAfter}
		var expiredReceipt extension.ExecutionClaimReceipt
		DecodeJSON(t, call(http.MethodPost, expiringPath, expiringRequest, http.StatusOK), &expiredReceipt)
		require.True(t, expiredReceipt.Acquired)
		time.Sleep(time.Until(time.Unix(expiring.NotAfter+1, 0)))
		DecodeJSON(t, call(http.MethodGet, expiringPath+"/"+expiringRequest.CommandID, nil, http.StatusOK), &expiredReceipt)
		require.False(t, expiredReceipt.Acquired)
		require.Equal(t, "claimed", expiredReceipt.State, "expiry cannot infer whether the native worker started")
		call(http.MethodPost, expiringPath+"/"+expiringRequest.CommandID+"/retire", extension.ExecutionRetirement{ManifestDigest: expiringRequest.ManifestDigest, State: "not_started", ReceiptDigest: strings.Repeat("3", 64)}, http.StatusOK)
		DecodeJSON(t, call(http.MethodPost, selected+"/operations", expiring, http.StatusOK), &expired)
		require.False(t, expired.Active)
		changedDeadline := expiring
		changedDeadline.NotAfter = authorize.NotAfter
		call(http.MethodPost, selected+"/operations", changedDeadline, http.StatusConflict)
		expiredClaim := extension.ExecutionClaimRequest{CommandID: strings.Repeat("0", 32), ManifestDigest: strings.Repeat("1", 64), NotAfter: authorize.NotAfter}
		call(http.MethodPost, selected+"/operations/"+expired.ID+"/claims", expiredClaim, http.StatusForbidden)
		withdraw := extension.RuntimeSelection{CommandID: strings.Repeat("8", 32), ExpectedRevision: selectedReceipt.Placement.Revision, Action: "withdraw", NotAfter: time.Now().Unix() + 120}
		DecodeJSON(t, call(http.MethodPost, selected+"/placement", withdraw, http.StatusOK), &selectedReceipt)
		require.Equal(t, "retiring", selectedReceipt.Placement.State)
		DecodeJSON(t, call(http.MethodGet, claimPath, nil, http.StatusOK), &claim)
		require.Equal(t, originalClaim, claim, "placement withdrawal preserves lookup without renewed launch authority")
		claimRequest.CommandID = strings.Repeat("9", 32)
		call(http.MethodPost, opBase+"/claims", claimRequest, http.StatusForbidden)
		retire := extension.RuntimeSelection{CommandID: strings.Repeat("a", 32), ExpectedRevision: selectedReceipt.Placement.Revision, Action: "retire", NotAfter: time.Now().Unix() + 120}
		DecodeJSON(t, call(http.MethodPost, selected+"/placement", retire, http.StatusOK), &refusal)
		require.Equal(t, "prior_controller_unsettled", refusal.ReasonCode)
		// This claim was never connected to a worker. Record exact no-start
		// evidence; no expiry or empty lookup is used as a retirement shortcut.
		retirement := extension.ExecutionRetirement{ManifestDigest: claim.ManifestDigest, State: "not_started", ReceiptDigest: strings.Repeat("b", 64)}
		call(http.MethodPost, opBase+"/claims/"+claim.CommandID+"/retire", retirement, http.StatusOK)
		DecodeJSON(t, call(http.MethodGet, claimPath, nil, http.StatusOK), &claim)
		require.False(t, claim.Acquired)
		require.Equal(t, "not_started", claim.State)
		require.Equal(t, retirement.ReceiptDigest, claim.RetirementDigest)
		DecodeJSON(t, call(http.MethodPost, selected+"/placement", retire, http.StatusOK), &refusal)
		require.Equal(t, "refused", refusal.Outcome, "settlement never renews a previously refused command")
		retire.CommandID = strings.Repeat("c", 32)
		DecodeJSON(t, call(http.MethodPost, selected+"/placement", retire, http.StatusOK), &selectedReceipt)
		require.Equal(t, "inactive", selectedReceipt.Placement.State)
		call(http.MethodDelete, opBase, nil, http.StatusOK)
		var revoked extension.DesktopOperationGrant
		DecodeJSON(t, call(http.MethodPost, selected+"/operations", authorize, http.StatusOK), &revoked)
		require.False(t, revoked.Active, "grant replay cannot reactivate withdrawal")
		DecodeJSON(t, call(http.MethodGet, claimPath, nil, http.StatusOK), &claim)
		require.False(t, claim.Acquired)
		require.Equal(t, retirement.ReceiptDigest, claim.RetirementDigest)
		var lookup extension.OperationLookup
		DecodeJSON(t, call(http.MethodGet, opBase+"/journal/"+intent.OperationID, nil, http.StatusOK), &lookup)
		require.Equal(t, record, *lookup.Record, "withdrawal preserves the original committed effect receipt")
		if checker := os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_CHECK"); checker != "" {
			root := os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_ROOT")
			require.True(t, filepath.IsAbs(root) && strings.HasPrefix(root, "/home/"))
			if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PUBLICATION") == "1" {
				repository, err := repo_model.GetRepositoryByID(t.Context(), 1)
				require.NoError(t, err)
				require.NoError(t, repository.LoadOwner(t.Context()))
				hooks := httptest.NewServer(testWebRoutes)
				defer hooks.Close()
				configuration, err := setting.NewConfigProviderFromFile(setting.CustomConf)
				require.NoError(t, err)
				configuration.Section("server").Key("LOCAL_ROOT_URL").SetValue(hooks.URL + "/")
				configuration.Section("repository").Key("ROOT").SetValue(setting.RepoRootPath)
				configuration.Section("security").Key("INTERNAL_TOKEN").SetValue(setting.InternalToken)
				configuration.Section("security").DeleteKey("INTERNAL_TOKEN_URI")
				hookConfig := filepath.Join(root, "hooks.ini")
				require.NoError(t, configuration.SaveTo(hookConfig))
				require.NoError(t, os.Chmod(hookConfig, 0o600))
				binary := os.Getenv("FACTORY_FOUNTAIN_PUBLISH_BINARY")
				require.True(t, filepath.IsAbs(binary))
				defer test.MockVariableValue(&setting.AppPath, binary)()
				defer test.MockVariableValue(&setting.CustomConf, hookConfig)()
				require.NoError(t, repo_module.CreateDelegateHooks(repository.RepoPath()))
				candidateRepo := filepath.Join(root, "candidate.git")
				output, err := exec.Command("git", "clone", "--bare", repository.RepoPath(), candidateRepo).CombinedOutput()
				require.NoError(t, err, string(output))
				git := func(arguments ...string) []byte {
					t.Helper()
					out, e := exec.Command("git", append([]string{"-C", candidateRepo}, arguments...)...).CombinedOutput()
					require.NoError(t, e, string(out))
					return bytes.TrimSpace(out)
				}
				base := string(git("rev-parse", "HEAD"))
				tree := string(git("rev-parse", "HEAD^{tree}"))
				if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PULL") == "1" {
					name := "native-publication-fixture.txt"
					require.Empty(t, git("ls-tree", "--name-only", "HEAD", "--", name))
					blobCommand := exec.Command("git", "-C", candidateRepo, "hash-object", "-w", "--stdin")
					blobCommand.Stdin = strings.NewReader("Real candidate object for conditional publication qualification.\n")
					blob, err := blobCommand.Output()
					require.NoError(t, err)
					entries, err := exec.Command("git", "-C", candidateRepo, "ls-tree", "-z", "HEAD").Output()
					require.NoError(t, err)
					entries = append(entries, []byte("100644 blob "+strings.TrimSpace(string(blob))+"\t"+name+"\x00")...)
					treeCommand := exec.Command("git", "-C", candidateRepo, "mktree", "-z")
					treeCommand.Stdin = bytes.NewReader(entries)
					changed, err := treeCommand.Output()
					require.NoError(t, err)
					tree = strings.TrimSpace(string(changed))
				}
				candidate := string(git("-c", "user.name=Native publish fixture", "-c", "user.email=publish@example.invalid", "commit-tree", tree, "-p", base, "-m", "Native conditional publication fixture"))
				git("update-ref", "HEAD", candidate)
				bundle, err := exec.Command("git", "-C", candidateRepo, "bundle", "create", "-", "HEAD", "^"+base).Output()
				require.NoError(t, err)
				fixture, err := json.Marshal(map[string]any{"Base": base, "Candidate": candidate, "Bundle": bundle})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "publication-fixture.json"), fixture, 0o600))
			}
			if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_INPUTS") == "1" {
				var repositorySettings map[string]any
				DecodeJSON(t, call(http.MethodGet, "/api/v1/repos/user2/repo1", nil, http.StatusOK), &repositorySettings)
				tracker, ok := repositorySettings["internal_tracker"].(map[string]any)
				require.True(t, ok, "fixture repository has no internal issue tracker")
				tracker["enable_issue_dependencies"] = true
				call(http.MethodPatch, "/api/v1/repos/user2/repo1", map[string]any{"internal_tracker": tracker}, http.StatusOK)
				type issueRef struct {
					ID     int64 `json:"id"`
					Number int64 `json:"number"`
				}
				var prerequisite, objective issueRef
				DecodeJSON(t, call(http.MethodPost, "/api/v1/repos/user2/repo1/issues", map[string]string{"title": "Desktop native prerequisite", "body": "Preserve this dependency context."}, http.StatusCreated), &prerequisite)
				DecodeJSON(t, call(http.MethodPost, "/api/v1/repos/user2/repo1/issues", map[string]string{"title": "Desktop native intake", "body": "Preserve this exact admitted objective.\n"}, http.StatusCreated), &objective)
				var comment struct {
					ID int64 `json:"id"`
				}
				DecodeJSON(t, call(http.MethodPost, fmt.Sprintf("/api/v1/repos/user2/repo1/issues/%d/comments", objective.Number), map[string]string{"body": "Selected native context.\n"}, http.StatusCreated), &comment)
				call(http.MethodPost, fmt.Sprintf("/api/v1/repos/user2/repo1/issues/%d/dependencies", objective.Number), map[string]any{"index": prerequisite.Number, "owner": "user2", "repo": "repo1"}, http.StatusCreated)
				fixture, err := json.Marshal(map[string]any{"number": objective.Number, "id": strconv.FormatInt(objective.ID, 10), "comment": strconv.FormatInt(comment.ID, 10), "dependency": strconv.FormatInt(prerequisite.ID, 10), "dependency_number": prerequisite.Number})
				require.NoError(t, err)
				require.NoFileExists(t, filepath.Join(root, "inputs-fixture.json"))
				require.NoError(t, os.WriteFile(filepath.Join(root, "inputs-fixture.json"), fixture, 0o600))
			}
			pair, err := tls.LoadX509KeyPair(filepath.Join(root, "server.pem"), filepath.Join(root, "server.key"))
			require.NoError(t, err)
			var lost atomic.Bool
			var effectSends atomic.Int32
			var receiveSends atomic.Int32
			var pullSends atomic.Int32
			var claimSends atomic.Int32
			var lostPreparation atomic.Bool
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var claimNumber int32
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/claims") {
					claimNumber = claimSends.Add(1)
				}
				if claimNumber == 3 && os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PREPARATION_LOST") == "1" {
					recorder := httptest.NewRecorder()
					testWebRoutes.ServeHTTP(recorder, r)
					if recorder.Code == http.StatusOK {
						lostPreparation.Store(true)
						http.Error(w, "fixture lost preparation claim reply", http.StatusServiceUnavailable)
						return
					}
					w.WriteHeader(recorder.Code)
					_, _ = w.Write(recorder.Body.Bytes())
					return
				}
				if claimNumber == 2 {
					require.NoError(t, os.WriteFile(filepath.Join(root, "claim-request-paused"), []byte("original claim received"), 0o600))
					deadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(deadline) {
						if _, err := os.Stat(filepath.Join(root, "claim-request-release")); err == nil {
							break
						}
						time.Sleep(10 * time.Millisecond)
					}
					recorder := httptest.NewRecorder()
					testWebRoutes.ServeHTTP(recorder, r)
					if recorder.Code == http.StatusOK {
						http.Error(w, "fixture lost claim reply", http.StatusServiceUnavailable)
						return
					}
					w.WriteHeader(recorder.Code)
					_, _ = w.Write(recorder.Body.Bytes())
					return
				}
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/operations/123456789abcdef0123456789abcdef4/submit") {
					effectSends.Add(1)
					require.NoError(t, os.WriteFile(filepath.Join(root, "effect-request-paused"), []byte("original request received"), 0o600))
					deadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(deadline) {
						if _, err := os.Stat(filepath.Join(root, "effect-request-release")); err == nil {
							break
						}
						time.Sleep(10 * time.Millisecond)
					}
					recorder := httptest.NewRecorder()
					testWebRoutes.ServeHTTP(recorder, r)
					if recorder.Code == http.StatusOK {
						http.Error(w, "fixture lost native effect reply", http.StatusServiceUnavailable)
						return
					}
					for key, values := range recorder.Header() {
						for _, value := range values {
							w.Header().Add(key, value)
						}
					}
					w.WriteHeader(recorder.Code)
					_, _ = w.Write(recorder.Body.Bytes())
					return
				}
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/placement") && !lost.Load() {
					recorder := httptest.NewRecorder()
					testWebRoutes.ServeHTTP(recorder, r)
					if recorder.Code == http.StatusOK && lost.CompareAndSwap(false, true) {
						http.Error(w, "fixture lost initial controller reply", http.StatusServiceUnavailable)
						return
					}
					for key, values := range recorder.Header() {
						for _, value := range values {
							w.Header().Add(key, value)
						}
					}
					w.WriteHeader(recorder.Code)
					_, _ = w.Write(recorder.Body.Bytes())
					return
				}
				if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_INPUTS") == "1" && strings.HasSuffix(r.URL.Path, "/snapshot") {
					recorder := httptest.NewRecorder()
					testWebRoutes.ServeHTTP(recorder, r)
					require.NoError(t, os.WriteFile(filepath.Join(root, "inputs-snapshot.json"), recorder.Body.Bytes(), 0o600))
					for key, values := range recorder.Header() {
						for _, value := range values {
							w.Header().Add(key, value)
						}
					}
					w.WriteHeader(recorder.Code)
					_, _ = w.Write(recorder.Body.Bytes())
					return
				}
				if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PULL") == "1" && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/operations/123456789abcdef0123456789abcdefa/submit") {
					body, err := io.ReadAll(io.LimitReader(r.Body, 128<<10))
					require.NoError(t, err)
					r.Body = io.NopCloser(bytes.NewReader(body))
					var intent extension.OperationIntent
					require.NoError(t, json.Unmarshal(body, &intent))
					if intent.Kind == extension.OperationKindPRCreate {
						pullSends.Add(1)
						recorder := httptest.NewRecorder()
						testWebRoutes.ServeHTTP(recorder, r)
						require.NoError(t, os.WriteFile(filepath.Join(root, "publication-pull-reply.json"), recorder.Body.Bytes(), 0o600))
						if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PULL_LOST") == "1" {
							http.Error(w, "fixture lost original PR creation reply", http.StatusServiceUnavailable)
							return
						}
						for key, values := range recorder.Header() {
							for _, value := range values {
								w.Header().Add(key, value)
							}
						}
						w.WriteHeader(recorder.Code)
						_, _ = w.Write(recorder.Body.Bytes())
						return
					}
				}
				if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PUBLICATION") == "1" && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
					receiveSends.Add(1)
					recorder := httptest.NewRecorder()
					testWebRoutes.ServeHTTP(recorder, r)
					require.NoError(t, os.WriteFile(filepath.Join(root, "publication-receive-reply.txt"), recorder.Body.Bytes(), 0o600))
					if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PUBLICATION_LOST") == "1" {
						http.Error(w, "fixture lost original receive reply", http.StatusServiceUnavailable)
						return
					}
					for key, values := range recorder.Header() {
						for _, value := range values {
							w.Header().Add(key, value)
						}
					}
					w.WriteHeader(recorder.Code)
					_, _ = w.Write(recorder.Body.Bytes())
					return
				}
				testWebRoutes.ServeHTTP(w, r)
			}))
			server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			defer server.Close()
			restoreURL := test.MockVariableValue(&setting.AppURL, server.URL+"/")
			command := exec.CommandContext(t.Context(), checker, "--fountain-controller-declared", filepath.Join(root, "profile"), server.URL, controlApp.ClientID)
			command.Env = append(os.Environ(), "SSL_CERT_FILE="+filepath.Join(root, "ca.pem"))
			command.Stdin = strings.NewReader(tokens.AccessToken + "\n" + controlTokens.AccessToken)
			output, err := command.CombinedOutput()
			restoreURL()
			t.Log(string(output))
			require.NoError(t, err)
			require.True(t, lost.Load())
			require.EqualValues(t, 1, effectSends.Load(), "lookup absence and native reopen never send the completion note twice")
			if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PUBLICATION") == "1" {
				require.EqualValues(t, 1, receiveSends.Load(), "original conditional transfer is never replayed")
			}
			if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PULL") == "1" {
				require.EqualValues(t, 1, pullSends.Load(), "original PR creation is never replayed")
			}
			expectedClaims := 2
			if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PAYLOAD") != "" {
				if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PREPARATION_LOST") != "1" {
					expectedClaims++ // Exact additional native worker; no paid inference.
				}
				if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PREPARATION") == "1" {
					expectedClaims++ // Privileged preparation has its own exact claim.
					if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PRODUCT_SETUP") == "1" {
						expectedClaims++ // Independent reviewer setup.
					}
				}
			}
			if os.Getenv("FACTORY_FOUNTAIN_CONTROLLER_PREPARATION_LOST") == "1" {
				require.True(t, lostPreparation.Load(), "original preparation claim reply was not lost")
			}
			require.EqualValues(t, expectedClaims, claimSends.Load(), "each native claim is reserved once; absence, lost reply, reopen and retirement only look up")
		}
		// Provider-side revocation cannot be replaced by today's login. A
		// fresh authorization may observe the old profile's exact history,
		// but cannot dispatch using the old operation grant.
		var placement extension.RuntimePlacement
		DecodeJSON(t, call(http.MethodGet, selected+"/placement", nil, http.StatusOK), &placement)
		freshSelection := extension.RuntimeSelection{CommandID: "19191919191919191919191919191901", ExpectedRevision: placement.Revision, Action: "activate", NotAfter: time.Now().Unix() + 120}
		DecodeJSON(t, call(http.MethodPost, selected+"/placement", freshSelection, http.StatusOK), &selectedReceipt)
		freshAuthorization := authorize
		freshAuthorization.CommandID = "19191919191919191919191919191902"
		freshAuthorization.ExpectedPlacement = selectedReceipt.Placement.Revision
		freshAuthorization.Generation = selectedReceipt.Placement.Generation
		freshAuthorization.NotAfter = time.Now().Unix() + 120
		var beforeRevocation extension.DesktopOperationGrant
		DecodeJSON(t, call(http.MethodPost, selected+"/operations", freshAuthorization, http.StatusOK), &beforeRevocation)
		require.True(t, beforeRevocation.Active)
		application, err := auth_model.GetOAuth2ApplicationByID(db.DefaultContext, controlApp.ID)
		require.NoError(t, err)
		authorization, err := application.GetGrantByUserID(db.DefaultContext, 2)
		require.NoError(t, err)
		require.NotNil(t, authorization)
		session.MakeRequest(t, NewRequest(t, http.MethodPost, fmt.Sprintf("/user/settings/applications/oauth2/%d/revoke/%d", controlApp.ID, authorization.ID)), http.StatusOK)
		call(http.MethodGet, selected+"/operations/"+beforeRevocation.ID, nil, http.StatusUnauthorized)
		session.MakeRequest(t, NewRequest(t, http.MethodGet, "/login/oauth/authorize?"+values.Encode()), http.StatusOK)
		reconsent := session.MakeRequest(t, NewRequestWithValues(t, http.MethodPost, "/login/oauth/grant", map[string]string{"client_id": controlApp.ClientID, "redirect_uri": redirect, "state": "desktop-fixture", "granted": "true", "scope": "write:repository write:issue"}), http.StatusSeeOther)
		relocation, err := reconsent.Result().Location()
		require.NoError(t, err)
		require.Empty(t, relocation.Query().Get("error"))
		reexchange := MakeRequest(t, NewRequestWithValues(t, http.MethodPost, "/login/oauth/access_token", map[string]string{"grant_type": "authorization_code", "client_id": controlApp.ClientID, "redirect_uri": redirect, "code": relocation.Query().Get("code"), "code_verifier": verifier}), http.StatusOK)
		DecodeJSON(t, reexchange, &controlTokens)
		var afterRevocation extension.DesktopOperationGrant
		DecodeJSON(t, call(http.MethodGet, selected+"/operations/"+beforeRevocation.ID, nil, http.StatusOK), &afterRevocation)
		require.False(t, afterRevocation.Active)
		DecodeJSON(t, call(http.MethodGet, claimPath, nil, http.StatusOK), &claim)
		require.False(t, claim.Acquired)
		require.Equal(t, retirement.ReceiptDigest, claim.RetirementDigest, "new OAuth consent permits history lookup without renewing dispatch")
		DecodeJSON(t, call(http.MethodGet, opBase+"/journal/"+intent.OperationID, nil, http.StatusOK), &lookup)
		require.Equal(t, record, *lookup.Record)
		changedIntent = intent
		changedIntent.ControllerGrantID = beforeRevocation.ID
		changedIntent.OperationID = "controller-after-reauthorization"
		changedIntent.NotAfter = freshAuthorization.NotAfter
		call(http.MethodPost, selected+"/operations/"+beforeRevocation.ID+"/submit", changedIntent, http.StatusForbidden)
		newEnrollment := input
		newEnrollment.CommandID = "19191919191919191919191919191903"
		var newBinding extension.DesktopGrant
		DecodeJSON(t, call(http.MethodPost, base, newEnrollment, http.StatusCreated), &newBinding)
		newBase := base + "/" + newBinding.ID
		freshSelection.CommandID = "19191919191919191919191919191904"
		freshSelection.ExpectedRevision = selectedReceipt.Placement.Revision
		freshSelection.Action = "withdraw"
		DecodeJSON(t, call(http.MethodPost, newBase+"/placement", freshSelection, http.StatusOK), &selectedReceipt)
		freshSelection.CommandID = "19191919191919191919191919191905"
		freshSelection.ExpectedRevision = selectedReceipt.Placement.Revision
		freshSelection.Action = "retire"
		DecodeJSON(t, call(http.MethodPost, newBase+"/placement", freshSelection, http.StatusOK), &selectedReceipt)
		require.Equal(t, "inactive", selectedReceipt.Placement.State)
	})

	if checker := os.Getenv("FACTORY_FOUNTAIN_NATIVE_CHECK"); checker != "" {
		root := os.Getenv("FACTORY_FOUNTAIN_NATIVE_ROOT")
		require.True(t, filepath.IsAbs(root) && strings.HasPrefix(root, "/home/"))
		nativeBase := base
		if os.Getenv("FACTORY_FOUNTAIN_NATIVE_PRODUCT_UI") == "1" {
			// The historical fixture marks PR2 merged while its issue remains
			// open. The native reader correctly refuses those contradictory facts.
			// Leave that history intact. Prepare a separate repository and real
			// branch/PR through authenticated APIs, never Factory phase facts.
			fixtureToken := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteUser, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeWriteIssue)
			created := session.MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, "/api/v1/user/repos", api.CreateRepoOption{Name: "factory-native-reader", AutoInit: true, Readme: "Default", DefaultBranch: "main"}).SetHeader("Authorization", "Bearer "+fixtureToken), http.StatusCreated)
			require.Equal(t, http.StatusCreated, created.Code)
			var repository api.Repository
			DecodeJSON(t, created, &repository)
			require.Positive(t, repository.ID)
			prefix := "/api/v1/repos/user2/factory-native-reader"
			file := session.MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, prefix+"/contents/reader-note.md", api.CreateFileOptions{FileOptions: api.FileOptions{BranchName: "main", NewBranchName: "reader-change", Message: "Add native desktop reader fixture"}, ContentBase64: base64.StdEncoding.EncodeToString([]byte("Native desktop reader fixture.\n"))}).SetHeader("Authorization", "Bearer "+fixtureToken), http.StatusCreated)
			require.Equal(t, http.StatusCreated, file.Code)
			opened := session.MakeRequest(t, NewRequestWithJSON(t, http.MethodPost, prefix+"/pulls", api.CreatePullRequestOption{Head: "reader-change", Base: "main", Title: "Read the native desktop candidate", Body: "Preserve this fixture text through native PR views."}).SetHeader("Authorization", "Bearer "+fixtureToken), http.StatusCreated)
			require.Equal(t, http.StatusCreated, opened.Code)
			var pull api.PullRequest
			DecodeJSON(t, opened, &pull)
			require.NotNil(t, pull.Head)
			require.NotNil(t, pull.Base)
			require.NotEqual(t, pull.Head.Sha, pull.Base.Sha)
			captured, err := json.Marshal(map[string]any{"repository": repository.ID, "name": repository.FullName, "pull": pull.Index, "pull_id": pull.ID, "base": pull.Base.Sha, "head": pull.Head.Sha, "actor": "2"})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(root, "reader-fixture.json"), captured, 0o600))
			nativeBase = prefix + "/factory/desktop/grants"
		}
		keyPair, err := tls.LoadX509KeyPair(filepath.Join(root, "server.pem"), filepath.Join(root, "server.key"))
		require.NoError(t, err)
		var enrollmentSends atomic.Int32
		var lostEnrollment atomic.Bool
		var refreshSends atomic.Int32
		uiFlow := os.Getenv("FACTORY_FOUNTAIN_NATIVE_UI") == "1"
		if os.Getenv("FACTORY_FOUNTAIN_NATIVE_OAUTH") == "1" && !uiFlow {
			defer test.MockVariableValue(&setting.OAuth2.AccessTokenExpirationTime, int64(5))()
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if os.Getenv("FACTORY_FOUNTAIN_NATIVE_OAUTH") == "1" && r.Method == http.MethodPost && r.URL.Path == nativeBase {
				enrollmentSends.Add(1)
				if lostEnrollment.CompareAndSwap(false, true) {
					written := httptest.NewRecorder()
					testWebRoutes.ServeHTTP(written, r)
					if written.Code != http.StatusCreated {
						w.WriteHeader(written.Code)
						return
					}
					// The actual host transaction committed; lose only its reply.
					connection, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = connection.Close()
					}
					return
				}
			}
			if os.Getenv("FACTORY_FOUNTAIN_NATIVE_OAUTH") == "1" && r.URL.Path == "/login/oauth/access_token" {
				if err := r.ParseForm(); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Form.Get("grant_type") == "refresh_token" && refreshSends.Add(1) == 2 && !uiFlow {
					written := httptest.NewRecorder()
					testWebRoutes.ServeHTTP(written, r)
					if written.Code != http.StatusOK {
						w.WriteHeader(written.Code)
						return
					}
					if err := os.WriteFile(filepath.Join(root, "refresh-entered"), []byte("committed"), 0o600); err != nil {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					deadline := time.Now().Add(6 * time.Second)
					for {
						if _, err := os.Stat(filepath.Join(root, "refresh-release")); err == nil {
							break
						}
						if time.Now().After(deadline) {
							w.WriteHeader(http.StatusGatewayTimeout)
							return
						}
						time.Sleep(20 * time.Millisecond)
					}
					for key, values := range written.Header() {
						w.Header()[key] = values
					}
					w.WriteHeader(written.Code)
					_, _ = w.Write(written.Body.Bytes())
					return
				}
			}
			testWebRoutes.ServeHTTP(w, r)
		})
		server := httptest.NewUnstartedServer(handler)
		server.TLS = &tls.Config{Certificates: []tls.Certificate{keyPair}, MinVersion: tls.VersionTLS12}
		server.StartTLS()
		defer server.Close()
		if os.Getenv("FACTORY_FOUNTAIN_NATIVE_OAUTH") == "1" {
			backend, payload := os.Getenv("FACTORY_FOUNTAIN_NATIVE_BACKEND"), os.Getenv("FACTORY_FOUNTAIN_NATIVE_POSTGRES")
			require.True(t, filepath.IsAbs(backend) && filepath.IsAbs(payload))
			command := exec.CommandContext(t.Context(), checker, "--fountain-desktop-login", filepath.Join(root, "profile"), server.URL, clientID, backend, payload)
			command.Env = append(os.Environ(), "SSL_CERT_FILE="+filepath.Join(root, "ca.pem"), "SODA_FOUNTAIN_LOST_ENROLLMENT=1", "SODA_FOUNTAIN_REFRESH_RACE=1")
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			require.NoError(t, command.Start())
			waited := false
			defer func() {
				if !waited {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
				_ = os.WriteFile(filepath.Join(root, "native-check.log"), output.Bytes(), 0o600)
			}()
			var authorization []byte
			loginWait := 30 * time.Second
			if uiFlow {
				loginWait = 5 * time.Minute
			}
			require.Eventually(t, func() bool {
				var err error
				authorization, err = os.ReadFile(filepath.Join(root, "fountain-authorize-uri"))
				return err == nil && len(authorization) > 0
			}, loginWait, 20*time.Millisecond)
			authorize, err := url.Parse(string(authorization))
			require.NoError(t, err)
			require.Equal(t, server.URL, authorize.Scheme+"://"+authorize.Host)
			query := authorize.Query()
			require.Equal(t, "S256", query.Get("code_challenge_method"))
			require.Equal(t, clientID, query.Get("client_id"))
			session.MakeRequest(t, NewRequest(t, http.MethodGet, authorize.RequestURI()), http.StatusOK)
			consent := session.MakeRequest(t, NewRequestWithValues(t, http.MethodPost, "/login/oauth/grant", map[string]string{"client_id": clientID, "redirect_uri": query.Get("redirect_uri"), "state": query.Get("state"), "granted": "true", "scope": query.Get("scope")}), http.StatusSeeOther)
			callback, err := consent.Result().Location()
			require.NoError(t, err)
			require.Empty(t, callback.Query().Get("error"), "native consent error: %s", callback.Query().Get("error_description"))
			require.Equal(t, query.Get("state"), callback.Query().Get("state"))
			browser := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			reply, err := browser.Get(callback.String())
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, reply.StatusCode)
			_ = reply.Body.Close()
			if uiFlow {
				// Browser consent uses the fixture origin above; subsequent native
				// repository pagination must advertise this actual TLS host.
				defer test.MockVariableValue(&setting.AppURL, server.URL+"/")()
			}
			err = command.Wait()
			waited = true
			t.Log(output.String())
			require.NoError(t, err)
			require.True(t, lostEnrollment.Load())
			if uiFlow {
				require.EqualValues(t, 1, refreshSends.Load(), "installed UI explicit refresh")
			} else {
				require.EqualValues(t, 2, refreshSends.Load(), "expiry refresh and single in-flight withdrawal; no replay")
			}
			require.EqualValues(t, 3, enrollmentSends.Load(), "original send plus exact replay after refresh and withdrawal; recovery lookup must not dispatch")
			return
		}
		command := exec.CommandContext(t.Context(), checker, "--fountain-desktop-declared", filepath.Join(root, "profile"), server.URL, clientID)
		command.Env = append(os.Environ(), "SSL_CERT_FILE="+filepath.Join(root, "ca.pem"))
		command.Stdin = strings.NewReader(tokens.AccessToken)
		output, err := command.CombinedOutput()
		t.Log(string(output))
		require.NoError(t, err)
	}
}

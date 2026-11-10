// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCallbackRequestRejectsDuplicateDecodedKeys(t *testing.T) {
	for _, body := range []string{
		`{"operation":"actor.current","operation":"repository.get"}`,
		`{"operation":"actor.current","\u006fperation":"repository.get"}`,
		`{"operation":"actor.current","authority":{"actor":{"id":"2","\u0069d":"3"}}}`,
		`null`,
		`[]`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/native", strings.NewReader(body))
		response := httptest.NewRecorder()
		if _, ok := decodeCallbackRequest(response, request); ok {
			t.Errorf("invalid callback request accepted: %s", body)
		}
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid request HTTP status %d, want 400", response.Code)
		}
	}
}

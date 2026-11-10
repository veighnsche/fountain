// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package context

import (
	"errors"
	"net/http"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/modules/extensions"
)

// policyErrorStatus preserves a policy veto versus a required runtime outage at
// the shared web/API error boundary, including wrapped external-auth errors.
// Native-mutation contention maps to 503 through the neutral typed sentinel
// shared by model and service adapters; this package need not import services.
func policyErrorStatus(err error) int {
	if errors.Is(err, nativeoperation_model.ErrBusy) || errors.Is(err, nativeoperation_model.ErrInhibited) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, extensions.ErrRequiredPolicyUnavailable) {
		return http.StatusServiceUnavailable
	}
	var denied extensions.ErrPolicyDenied
	if errors.As(err, &denied) {
		return http.StatusUnprocessableEntity
	}
	return 0
}

// HandlePolicyError renders only typed policy errors; callers retain all their
// existing handling for native validation and unrelated failures.
func (ctx *Context) HandlePolicyError(err error) bool {
	if status := policyErrorStatus(err); status != 0 {
		ctx.PlainText(status, err.Error())
		return true
	}
	return false
}

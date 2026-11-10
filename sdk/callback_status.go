// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// callbackHTTPStatus is transport information, interpreted by the requesting
// operation family only after the private transport has finished.
type callbackHTTPStatus struct{ status int }

func (e *callbackHTTPStatus) Error() string {
	return fmt.Sprintf("native callback returned HTTP %d", e.status)
}

func (c *nativeClient) call(ctx context.Context, request CallbackRequest) (CallbackResponse, error) {
	result, err := c.exchange(ctx, request)
	var status *callbackHTTPStatus
	if errors.As(err, &status) {
		return CallbackResponse{}, callbackStatusError(request.Operation, status.status)
	}
	return result, err
}

func callbackStatusError(operation string, status int) error {
	spec, _, ok := callbackOperationSpec(operation)
	if ok && spec.statusError != nil {
		if err := spec.statusError(status); err != nil {
			return err
		}
	}
	return &callbackHTTPStatus{status: status}
}

// callbackStatusProfile supplies only HTTP mechanics. Each callback family owns
// its status vocabulary and whether unlisted statuses have a family fallback.
type callbackStatusProfile struct {
	badRequest    error
	admission     error
	notFound      error
	conflict      error
	unprocessable error
	tooLarge      error
	unavailable   error
	otherwise     error
}

func (profile callbackStatusProfile) forStatus(status int) error {
	var err error
	switch status {
	case http.StatusBadRequest:
		err = profile.badRequest
	case http.StatusUnauthorized, http.StatusForbidden:
		err = profile.admission
	case http.StatusNotFound:
		err = profile.notFound
	case http.StatusConflict:
		err = profile.conflict
	case http.StatusUnprocessableEntity:
		err = profile.unprocessable
	case http.StatusRequestEntityTooLarge:
		err = profile.tooLarge
	case http.StatusServiceUnavailable:
		err = profile.unavailable
	}
	if err != nil {
		return err
	}
	return profile.otherwise
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"net/http"
	"slices"

	nativeoperation_service "forgejo.org/services/nativeoperation"
)

type snapshotReadStatus int

func (s snapshotReadStatus) Error() string { return http.StatusText(int(s)) }

func equalSnapshot[T comparable](first, second *T) bool { return *first == *second }

func observeCallbackSnapshot[T any](ctx context.Context, read func(context.Context) (*T, int), equal func(*T, *T) bool, recheck func(context.Context) int, disappeared []int) (*T, int64, int) {
	var final func(context.Context) error
	if recheck != nil {
		final = func(ctx context.Context) error {
			if status := recheck(ctx); status != http.StatusOK {
				return snapshotReadStatus(status)
			}
			return nil
		}
	}
	value, revision, err := nativeoperation_service.ObserveStable(ctx, nativeoperation_service.Default().ReadNativeRevision,
		func(ctx context.Context, _ int64) (*T, error) {
			value, status := read(ctx)
			if status != http.StatusOK {
				return nil, snapshotReadStatus(status)
			}
			return value, nil
		}, equal, final)
	if err == nil {
		return value, revision, http.StatusOK
	}
	var observation *nativeoperation_service.ObservationError
	if !errors.As(err, &observation) {
		return nil, 0, http.StatusServiceUnavailable
	}
	if observation.Stage == nativeoperation_service.ObservationChanged {
		return nil, 0, http.StatusConflict
	}
	var status snapshotReadStatus
	if errors.As(observation.Err, &status) {
		if observation.Stage == nativeoperation_service.ObservationSecondRead && slices.Contains(disappeared, int(status)) {
			return nil, 0, http.StatusConflict
		}
		return nil, 0, int(status)
	}
	return nil, 0, http.StatusServiceUnavailable
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
)

type ObservationStage uint8

const (
	ObservationBefore ObservationStage = iota
	ObservationFirstRead
	ObservationSecondRead
	ObservationRecheck
	ObservationAfter
	ObservationChanged
)

var (
	ErrObservationBusy    = errors.New("native observation is not idle")
	ErrObservationChanged = errors.New("native observation changed")
)

// ObservationError retains the read boundary so wire adapters can preserve
// their distinct disappearance, availability and conflict profiles.
type ObservationError struct {
	Stage ObservationStage
	Err   error
}

func (e *ObservationError) Error() string { return e.Err.Error() }
func (e *ObservationError) Unwrap() error { return e.Err }

// ObserveStable brackets two typed reads with one idle native revision. The
// optional recheck is a read-admission check immediately before the final
// revision; execution admission remains with the caller's write transaction.
func ObserveStable[T any](ctx context.Context, revision func(context.Context) (NativeRevisionObservation, error), read func(context.Context, int64) (T, error), equal func(T, T) bool, recheck func(context.Context) error) (T, int64, error) {
	var zero T
	refuse := func(stage ObservationStage, err error) (T, int64, error) {
		return zero, 0, &ObservationError{Stage: stage, Err: err}
	}
	before, err := revision(ctx)
	if err != nil {
		return refuse(ObservationBefore, err)
	}
	if !before.Idle {
		return refuse(ObservationBefore, ErrObservationBusy)
	}
	first, err := read(ctx, before.Revision)
	if err != nil {
		return refuse(ObservationFirstRead, err)
	}
	second, err := read(ctx, before.Revision)
	if err != nil {
		return refuse(ObservationSecondRead, err)
	}
	if recheck != nil {
		if err := recheck(ctx); err != nil {
			return refuse(ObservationRecheck, err)
		}
	}
	after, err := revision(ctx)
	if err != nil {
		return refuse(ObservationAfter, err)
	}
	if !after.Idle || after.Revision != before.Revision || !equal(first, second) {
		return refuse(ObservationChanged, ErrObservationChanged)
	}
	return first, before.Revision, nil
}

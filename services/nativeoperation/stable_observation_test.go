// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestObserveStableRechecksAfterHydrationBeforeFinalRevision(t *testing.T) {
	var calls []string
	revision := func(context.Context) (NativeRevisionObservation, error) {
		calls = append(calls, "revision")
		return NativeRevisionObservation{Revision: 17, Idle: true}, nil
	}
	read := func(_ context.Context, revision int64) ([]string, error) {
		require.EqualValues(t, 17, revision)
		calls = append(calls, "read")
		return []string{"private-source", "public-fact"}, nil
	}
	value, revisionID, err := ObserveStable(t.Context(), revision, read,
		func(a, b []string) bool { return reflect.DeepEqual(a, b) }, func(context.Context) error {
			calls = append(calls, "admit")
			return nil
		})
	require.NoError(t, err)
	require.EqualValues(t, 17, revisionID)
	require.Equal(t, []string{"private-source", "public-fact"}, value)
	require.Equal(t, []string{"revision", "read", "read", "admit", "revision"}, calls)
}

func TestObserveStableRefusesRacesAndRetainsFailureBoundary(t *testing.T) {
	disappeared := errors.New("resource disappeared")
	for _, tc := range []struct {
		name                                  string
		busyBefore, busyAfter, moved, changed bool
		readErrorAt                           int
		recheckError                          bool
		stage                                 ObservationStage
		cause                                 error
	}{
		{name: "writer already held", busyBefore: true, stage: ObservationBefore, cause: ErrObservationBusy},
		{name: "initial resource missing", readErrorAt: 1, stage: ObservationFirstRead, cause: disappeared},
		{name: "disappearance after first read", readErrorAt: 2, stage: ObservationSecondRead, cause: disappeared},
		{name: "authority changed after hydration", recheckError: true, stage: ObservationRecheck, cause: disappeared},
		{name: "writer claimed during read", busyAfter: true, stage: ObservationChanged, cause: ErrObservationChanged},
		{name: "writer completed during read", moved: true, stage: ObservationChanged, cause: ErrObservationChanged},
		{name: "facts changed without writer", changed: true, stage: ObservationChanged, cause: ErrObservationChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, revisions := 0, 0
			revision := func(context.Context) (NativeRevisionObservation, error) {
				revisions++
				return NativeRevisionObservation{Revision: 4 + int64(boolInt(tc.moved && revisions == 2)), Idle: !(tc.busyBefore && revisions == 1 || tc.busyAfter && revisions == 2)}, nil
			}
			read := func(context.Context, int64) (string, error) {
				reads++
				if reads == tc.readErrorAt {
					return "", disappeared
				}
				if tc.changed && reads == 2 {
					return "replacement", nil
				}
				return "original", nil
			}
			value, revisionID, err := ObserveStable(t.Context(), revision, read, func(a, b string) bool { return a == b }, func(context.Context) error {
				if tc.recheckError {
					return disappeared
				}
				return nil
			})
			require.ErrorIs(t, err, tc.cause)
			var observation *ObservationError
			require.ErrorAs(t, err, &observation)
			require.Equal(t, tc.stage, observation.Stage)
			require.Empty(t, value)
			require.Zero(t, revisionID)
		})
	}
}

func TestObserveStableRetainsRevisionReadAvailabilityErrors(t *testing.T) {
	unavailable := errors.New("revision storage unavailable")
	for _, failAt := range []int{1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			revisions := 0
			_, _, err := ObserveStable(t.Context(), func(context.Context) (NativeRevisionObservation, error) {
				revisions++
				if revisions == failAt {
					return NativeRevisionObservation{}, unavailable
				}
				return NativeRevisionObservation{Revision: 3, Idle: true}, nil
			}, func(context.Context, int64) (string, error) { return "facts", nil }, func(a, b string) bool { return a == b }, nil)
			require.ErrorIs(t, err, unavailable)
			var observation *ObservationError
			require.ErrorAs(t, err, &observation)
			if failAt == 1 {
				require.Equal(t, ObservationBefore, observation.Stage)
			} else {
				require.Equal(t, ObservationAfter, observation.Stage)
			}
		})
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

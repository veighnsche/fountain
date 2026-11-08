// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"testing"
	"time"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestOrdinaryOwnershipSerializesAndReleases(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := admissionService(t, nil, 0)

	start, err := svc.ReadNativeRevision(ctx)
	require.NoError(t, err)
	require.True(t, start.Idle)

	ran := false
	err = svc.WithOrdinaryOwnership(ctx, "branch", "1/topic", Scope{RepositoryID: 1, Ref: "refs/heads/topic"}, func(ctx context.Context) error {
		ran = true
		exec := execcontext.FromContext(ctx)
		require.NotNil(t, exec)
		require.NotEmpty(t, exec.CapabilityPath)
		reservation, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		require.Equal(t, `{"kind":"ordinary","repository_id":1,"ref":"refs/heads/topic","family":"branch"}`, reservation.Scope)
		held, err := svc.ReadNativeRevision(ctx)
		require.NoError(t, err)
		require.False(t, held.Idle)
		require.Equal(t, start.Revision+1, held.Revision)
		// Nested participation reuses the enclosing ownership.
		return svc.WithOrdinaryOwnership(ctx, "branch", "1/nested", Scope{}, func(context.Context) error {
			return nil
		})
	})
	require.NoError(t, err)
	require.True(t, ran)

	idle, err := svc.ReadNativeRevision(ctx)
	require.NoError(t, err)
	require.True(t, idle.Idle)
	require.Equal(t, start.Revision+1, idle.Revision)

	// A real conditional owner lends its execution without another claim or
	// replacing its broader scope with the ordinary caller's projection.
	token := writerToken(t)
	owner, _ := claimMergeOwner(t, ctx, token, "ordinary-nesting", Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: 1, Ref: "refs/heads/main"}, 1000)
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	execution := &execcontext.Execution{Owner: owner, Generation: held.Generation}
	nested := execcontext.NewContext(ctx, execution)
	require.NoError(t, svc.WithOrdinaryOwnership(nested, "", "", Scope{}, func(owned context.Context) error {
		require.Same(t, execution, execcontext.FromContext(owned))
		return nil
	}))
	after, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, held, after)
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, owner))
}

func TestOrdinaryOwnershipRefusesWhenBusy(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := admissionService(t, nil, 0)

	_, err := nativeoperation_model.ClaimOrdinary(ctx, "ord:test/holder", `{}`, "v")
	require.NoError(t, err)
	ran := false
	err = svc.WithOrdinaryOwnership(ctx, "branch", "1/topic", Scope{RepositoryID: 1, Ref: "refs/heads/topic"}, func(context.Context) error {
		ran = true
		return nil
	})
	require.Error(t, err)
	require.True(t, IsBusy(err))
	require.False(t, ran)

	// A failing writer still releases its owner.
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, "ord:test/holder"))
	boom := errors.New("writer failed")
	err = svc.WithOrdinaryOwnership(ctx, "branch", "1/topic", Scope{RepositoryID: 1, Ref: "refs/heads/topic"}, func(context.Context) error {
		return boom
	})
	require.ErrorIs(t, err, boom)
	idle, err := svc.ReadNativeRevision(ctx)
	require.NoError(t, err)
	require.True(t, idle.Idle)
	// Simulate ownership loss at release. The shared primitive must remove
	// its own capability, surface failure and leave the replacement fence held.
	for _, fnErr := range []error{nil, boom} {
		var path string
		err := svc.WithOrdinaryOwnership(ctx, "branch", "1/topic", Scope{RepositoryID: 1}, func(owned context.Context) error {
			execution := execcontext.FromContext(owned)
			path = execution.CapabilityPath
			require.FileExists(t, path)
			require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, execution.Owner))
			_, err := nativeoperation_model.ClaimOrdinary(ctx, "ord:test/replacement", "{}", "replacement-proof")
			require.NoError(t, err)
			return fnErr
		})
		require.ErrorContains(t, err, "failed to release")
		if fnErr != nil {
			require.ErrorIs(t, err, fnErr)
		} else {
			require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
		}
		require.NoFileExists(t, path)
		held, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		require.Equal(t, "ord:test/replacement", held.Owner)
		require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, held.Owner))
	}
}

func TestOrdinaryOwnershipRefusesCanceledContext(t *testing.T) {
	unittest.PrepareTestEnv(t)
	svc := admissionService(t, nil, 0)

	start, err := svc.ReadNativeRevision(t.Context())
	require.NoError(t, err)
	require.True(t, start.Idle)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	ran := false
	err = svc.WithOrdinaryOwnership(canceled, "branch", "1/topic", Scope{RepositoryID: 1, Ref: "refs/heads/topic"}, func(context.Context) error {
		ran = true
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, ran)

	idle, err := svc.ReadNativeRevision(t.Context())
	require.NoError(t, err)
	require.True(t, idle.Idle)
	require.Equal(t, start.Revision, idle.Revision)
}

func TestOrdinaryOwnershipReleasesAfterCancel(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := admissionService(t, nil, 0)

	cancellable, cancel := context.WithCancel(ctx)
	err := svc.WithOrdinaryOwnership(cancellable, "branch", "1/topic", Scope{RepositoryID: 1, Ref: "refs/heads/topic"}, func(ctx context.Context) error {
		cancel()
		return nil
	})
	require.NoError(t, err)

	idle, err := svc.ReadNativeRevision(ctx)
	require.NoError(t, err)
	require.True(t, idle.Idle)
}

func TestOrdinaryOwnershipRefusesWhileInhibited(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := admissionService(t, nil, 0)
	useIsolatedAppData(t)

	start, err := svc.ReadNativeRevision(ctx)
	require.NoError(t, err)
	require.True(t, start.Idle)
	inhibitDomain(t)

	ran := false
	err = svc.WithOrdinaryOwnership(ctx, "branch", "1/topic", Scope{RepositoryID: 1, Ref: "refs/heads/topic"}, func(context.Context) error {
		ran = true
		return nil
	})
	require.Error(t, err)
	require.True(t, IsBusy(err))
	require.ErrorIs(t, err, nativeoperation_model.ErrInhibited)
	require.False(t, ran)

	idle, err := svc.ReadNativeRevision(ctx)
	require.NoError(t, err)
	require.True(t, idle.Idle)
	require.Equal(t, start.Revision, idle.Revision)
}

func TestOrdinaryOwnershipWaitDoesNotRepeatEffects(t *testing.T) {
	unittest.PrepareTestEnv(t)
	service := admissionService(t, nil, 0)
	calls := 0
	err := service.WithOrdinaryOwnershipWait(t.Context(), FamilyCollaboration, execcontext.CollabBatchResource("pr-sweep"), Scope{RepositoryID: 1}, time.Second, func(context.Context) error {
		calls++
		return nativeoperation_model.ErrBusy
	})
	require.ErrorIs(t, err, nativeoperation_model.ErrBusy)
	require.Equal(t, 1, calls, "callback errors must never replay effects")
	state, err := service.ReadNativeRevision(t.Context())
	require.NoError(t, err)
	require.True(t, state.Idle)
}

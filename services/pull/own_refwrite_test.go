// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"reflect"
	"testing"

	execcontext "forgejo.org/modules/nativeoperation"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

func TestMergeRetryRequiresRefusalBeforeEffects(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	scope := refWriteScope{RepositoryID: 1, Ref: "refs/heads/main"}
	_, err := nativeoperation_model.ClaimOrdinary(ctx, "ord:test/holder", `{}`, "v")
	require.NoError(t, err)
	calls := 0
	err = withMergeRefWriteOwnership(ctx, "1/merge/refs/heads/main", scope, func(context.Context) error {
		calls++
		return nil
	})
	require.ErrorIs(t, err, ErrMergeNotStarted)
	require.ErrorIs(t, err, nativeoperation_model.ErrBusy)
	require.Zero(t, calls)
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, "ord:test/holder"))

	// A callback may return the same busy error after starting effects. It
	// must retain that error without authorizing a second merge attempt.
	err = withMergeRefWriteOwnership(ctx, "1/merge/refs/heads/main", scope, func(context.Context) error {
		calls++
		return nativeoperation_model.ErrBusy
	})
	require.ErrorIs(t, err, nativeoperation_model.ErrBusy)
	require.False(t, errors.Is(err, ErrMergeNotStarted))
	require.Equal(t, 1, calls)
}

// TestRefWriteScopeWireContract pins the pull-local ownership mirror to
// the native-operation scope wire shape: the same JSON field names the
// service scope decodes, so offline recovery reads pull claims exactly
// like service claims. The mirror exists only to break the pull to
// native-operation import cycle; any drift here is a recovery bug.
func TestRefWriteScopeWireContract(t *testing.T) {
	raw, err := json.Marshal(refWriteScope{
		Kind:         "ordinary",
		Family:       familyRefWrite,
		RepositoryID: 7,
		Ref:          "refs/heads/main",
		OldOID:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NewOID:       "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		PRNumber:     3,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"kind":          "ordinary",
		"family":        "ref-write",
		"repository_id": float64(7),
		"ref":           "refs/heads/main",
		"old_oid":       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"new_oid":       "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"pr_number":     float64(3),
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("mirror scope JSON = %v, want %v", decoded, want)
	}

	if got := execcontext.RefWriteResource(7, refWriteMerge, "refs/heads/main"); got != "7/merge/refs/heads/main" {
		t.Fatalf("mirror resource = %q, want %q", got, "7/merge/refs/heads/main")
	}
}

// TestRefWriteOwnershipCanceledContext proves a canceled caller never
// strands the reservation: a pre-canceled claim refuses before any
// effect, and a cancellation racing the writer still releases.
func TestRefWriteOwnershipCanceledContext(t *testing.T) {
	unittest.PrepareTestEnv(t)
	scope := refWriteScope{RepositoryID: 1, Ref: "refs/heads/main"}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	ran := false
	require.ErrorIs(t, withRefWriteOwnership(canceled, "1/merge/refs/heads/main", scope, func(ctx context.Context) error {
		ran = true
		return nil
	}), context.Canceled)
	require.False(t, ran)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	require.NoError(t, withRefWriteOwnership(ctx, "1/merge/refs/heads/main", scope, func(ctx context.Context) error {
		cancel()
		return nil
	}))

	idle, err := nativeoperation_model.ReadReservation(context.WithoutCancel(ctx))
	require.NoError(t, err)
	require.Empty(t, idle.Owner)
}

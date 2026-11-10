// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

type ordinaryTestStore struct {
	claims, releases       int
	owner, scope, verifier string
	generation             int64
	claimErr, releaseErr   error
	release                func(context.Context)
}

func (store *ordinaryTestStore) Claim(_ context.Context, owner, scope, verifier string) (int64, error) {
	store.claims++
	store.owner, store.scope, store.verifier = owner, scope, verifier
	return store.generation, store.claimErr
}

func (store *ordinaryTestStore) Release(ctx context.Context, owner string, generation int64) error {
	store.releases++
	if owner != store.owner || generation != store.generation {
		return errors.New("released a different execution")
	}
	if store.release != nil {
		store.release(ctx)
	}
	return store.releaseErr
}

func TestRunOrdinaryCancelledBeforeClaim(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dir := t.TempDir()
	store := &ordinaryTestStore{generation: 7}
	err := RunOrdinary(ctx, store, dir, "branch", "7/main", `{}`, func(context.Context) error {
		t.Fatal("cancelled writer entered its effect")
		return nil
	})
	if !errors.Is(err, context.Canceled) || store.claims != 0 || store.releases != 0 {
		t.Fatalf("cancelled writer touched durable ownership: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled writer left a capability: %v", err)
	}
}

func TestRunOrdinaryCapabilityAndNestedOwnership(t *testing.T) {
	dir := t.TempDir()
	store := &ordinaryTestStore{generation: 7}
	var path string
	err := RunOrdinary(t.Context(), store, dir, "branch", "7/main", `{"future":1e+09}`, func(ctx context.Context) error {
		execution := FromContext(ctx)
		if execution == nil || execution.Owner != store.owner || execution.Generation != 7 || !strings.HasPrefix(execution.Owner, "ord:branch/7/main/") || store.scope != `{"future":1e+09}` {
			t.Fatal("effect did not receive its claimed execution")
		}
		path = execution.CapabilityPath
		secret, err := ReadCapabilityFile(path)
		if err != nil || !VerifyProof(store.verifier, secret) {
			t.Fatal("effect capability did not match the durable verifier")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("effect capability is not private: %v", err)
		}
		return RunOrdinary(ctx, nil, "", "", "", "", func(nested context.Context) error {
			if FromContext(nested) != execution {
				t.Fatal("nested writer replaced enclosing ownership")
			}
			return nil
		})
	})
	if err != nil || store.claims != 1 || store.releases != 1 {
		t.Fatalf("nested writer did not share one lease: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("returned writer retained a reusable capability")
	}
}

func TestRunOrdinaryReleasesWithDetachedContext(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "writer"))
	defer cancel()
	callbackErr := errors.New("effect refused")
	store := &ordinaryTestStore{generation: 7, release: func(release context.Context) {
		if release.Err() != nil || release.Done() != nil || release.Value(contextKey{}) != "writer" {
			t.Fatal("release inherited cancellation or lost context values")
		}
	}}
	err := RunOrdinary(ctx, store, t.TempDir(), "branch", "7/main", `{}`, func(context.Context) error {
		cancel()
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || store.releases != 1 {
		t.Fatalf("effect failure skipped or masked release: %v", err)
	}
}

func TestRunOrdinaryFailureRetiresCapability(t *testing.T) {
	claimErr := errors.New("claim failed")
	releaseErr := errors.New("release failed")
	callbackErr := errors.New("effect failed")
	for _, test := range []struct {
		name                              string
		claimErr, callbackErr, releaseErr error
		want                              error
		releases                          int
	}{
		{"claim", claimErr, nil, nil, claimErr, 0},
		{"busy", fmt.Errorf("fenced: %w", ErrBusy), nil, nil, ErrBusy, 0},
		{"inhibited", fmt.Errorf("offline: %w", ErrInhibited), nil, nil, ErrInhibited, 0},
		{"release", nil, nil, releaseErr, releaseErr, 1},
		{"effect and release", nil, callbackErr, releaseErr, callbackErr, 1},
		{"indeterminate", nil, fmt.Errorf("partial result: %w", ErrIndeterminateEffect), nil, ErrIndeterminateEffect, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store := &ordinaryTestStore{generation: 7, claimErr: test.claimErr, releaseErr: test.releaseErr}
			err := RunOrdinary(t.Context(), store, dir, "branch", "7/main", `{}`, func(context.Context) error {
				if test.claimErr != nil {
					t.Fatal("failed claim entered its effect")
				}
				return test.callbackErr
			})
			if !errors.Is(err, test.want) || store.claims != 1 || store.releases != test.releases {
				t.Fatalf("failure lost ownership/error rules: %v; claims=%d releases=%d", err, store.claims, store.releases)
			}
			if test.callbackErr != nil && test.releaseErr != nil && !strings.Contains(err.Error(), test.releaseErr.Error()) {
				t.Fatal("release failure disappeared behind effect failure")
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("failed writer retained a capability: %v", readErr)
			}
		})
	}
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package nativeownership owns the ordinary reservation and its capability lifetime.
package nativeownership

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
)

// WithOrdinary passes the caller's encoded scope unchanged to the existing
// reservation. Nested execution reuses its owner; release outlives cancellation.
func WithOrdinary(ctx context.Context, dir, family, resource, scopeJSON string, fn func(context.Context) error) error {
	if execcontext.FromContext(ctx) != nil {
		return fn(ctx)
	}
	// A canceled caller cannot hold the domain: claiming would strand
	// the reservation when the release below fails on the same ctx.
	if err := ctx.Err(); err != nil {
		return err
	}
	if family == "" || resource == "" {
		return errors.New("ordinary ownership requires a writer identity")
	}

	path, secret, err := execcontext.WriteCapabilityFile(dir)
	if err != nil {
		return err
	}
	owner := NewOwner(family, resource)
	release := func() {
		_ = os.Remove(path)
	}

	claimed, err := nativeoperation_model.ClaimOrdinary(ctx, owner, scopeJSON, execcontext.Verifier(secret))
	if err != nil {
		release()
		if errors.Is(err, nativeoperation_model.ErrBusy) {
			return fmt.Errorf("%w: ordinary writer %s is fenced", nativeoperation_model.ErrBusy, family)
		}
		if errors.Is(err, nativeoperation_model.ErrInhibited) {
			return fmt.Errorf("%w: ordinary writer %s refuses while offline recovery holds the domain", nativeoperation_model.ErrInhibited, family)
		}
		return err
	}
	owned := execcontext.NewContext(ctx, &execcontext.Execution{Owner: owner, Generation: claimed.Generation, CapabilityPath: path})
	fnErr := fn(owned)
	if err := nativeoperation_model.ReleaseOwner(context.WithoutCancel(ctx), owner); err != nil {
		release()
		if fnErr != nil {
			return fmt.Errorf("%w (and failed to release %s: %v)", fnErr, owner, err)
		}
		return fmt.Errorf("failed to release %s: %w", owner, err)
	}
	release()
	return fnErr
}

// NewOwner gives independently held ordinary protocols the same owner encoding.
func NewOwner(family, resource string) string {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	return "ord:" + family + "/" + resource + "/" + hex.EncodeToString(nonce)
}

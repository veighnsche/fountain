// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

var (
	ErrBusy      = errors.New("native mutation reservation is busy")
	ErrInhibited = errors.New("native mutation domain is inhibited for offline recovery")
	// ErrIndeterminateEffect keeps ownership when an entered native effect has
	// no proven complete outcome. It never authorizes retry or automatic release.
	ErrIndeterminateEffect = errors.New("native effect requires stopped-domain reconciliation")
)

type OrdinaryStore interface {
	Claim(context.Context, string, string, string) (int64, error)
	Release(context.Context, string, int64) error
}

func NewOrdinaryOwner(family, resource string) string {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	return "ord:" + family + "/" + resource + "/" + hex.EncodeToString(nonce)
}

func RunOrdinary(ctx context.Context, store OrdinaryStore, dir, family, resource, scopeJSON string, fn func(context.Context) error) error {
	if FromContext(ctx) != nil {
		return fn(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if family == "" || resource == "" {
		return errors.New("ordinary ownership requires a writer identity")
	}
	path, secret, err := WriteCapabilityFile(dir)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()
	owner := NewOrdinaryOwner(family, resource)
	generation, err := store.Claim(ctx, owner, scopeJSON, Verifier(secret))
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return fmt.Errorf("%w: ordinary writer %s is fenced", ErrBusy, family)
		}
		if errors.Is(err, ErrInhibited) {
			return fmt.Errorf("%w: ordinary writer %s refuses while offline recovery holds the domain", ErrInhibited, family)
		}
		return err
	}
	owned := NewContext(ctx, &Execution{Owner: owner, Generation: generation, CapabilityPath: path})
	fnErr := fn(owned)
	if errors.Is(fnErr, ErrIndeterminateEffect) {
		return fnErr
	}
	if err := store.Release(context.WithoutCancel(ctx), owner, generation); err != nil {
		if fnErr != nil {
			return fmt.Errorf("%w (and failed to release %s: %v)", fnErr, owner, err)
		}
		return fmt.Errorf("failed to release %s: %w", owner, err)
	}
	return fnErr
}

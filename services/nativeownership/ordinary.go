// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package nativeownership composes ordinary ownership with model persistence.
package nativeownership

import (
	"context"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
)

func WithOrdinary(ctx context.Context, dir, family, resource, scopeJSON string, fn func(context.Context) error) error {
	return execcontext.RunOrdinary(ctx, nativeoperation_model.OrdinaryStore{}, dir, family, resource, scopeJSON, fn)
}

func NewOwner(family, resource string) string { return execcontext.NewOrdinaryOwner(family, resource) }

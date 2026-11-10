// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package context

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestOwnershipContentionClassificationUsesIdentity(t *testing.T) {
	require.Equal(t, http.StatusServiceUnavailable, policyErrorStatus(fmt.Errorf("writer refused: %w", execcontext.ErrBusy)))
	require.Equal(t, http.StatusServiceUnavailable, policyErrorStatus(nativeoperation_model.ErrInhibited))
	require.Zero(t, policyErrorStatus(errors.New(nativeoperation_model.ErrBusy.Error())))
	require.Zero(t, policyErrorStatus(fmt.Errorf("diagnostic repeats %s", nativeoperation_model.ErrBusy)))
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPullCreationResourceAcceptsUnknownPreinsertAnchor(t *testing.T) {
	for _, id := range []int64{0, 1, 123} {
		claim, err := ParseCollabResource(PullCreateResource(id))
		require.NoError(t, err)
		require.Equal(t, CollabClaim{Kind: "pull", ID: id, Op: "create"}, claim)
	}
	for _, resource := range []string{
		"pull/new/-1", "pull/new/-0", "pull/new/+0", "pull/new/00",
		"pull/new/NaN", "pull/new/", "pull/new/0/extra", "pull/0/update",
		"issue/new/0", "comment/new/0", "review/new/0",
	} {
		t.Run(resource, func(t *testing.T) {
			_, err := ParseCollabResource(resource)
			require.Error(t, err)
		})
	}
}

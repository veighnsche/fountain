// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"testing"
	"time"

	sdk "forgejo.org/extension-sdk"
	nativeoperation_model "forgejo.org/models/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestBackgroundIntentCannotOccupyHumanMergeOrPullCreationNamespace(t *testing.T) {
	now := time.Now().Unix()
	payload := mergePayload(t, nil)
	validate := func(id string) (*ValidIntent, error) {
		return ValidateIntent(id, 2, 1, nativeoperation_model.KindMerge, "rev-1", 7, now+300, payload, now)
	}
	ordinary, err := validate("background-merge")
	require.NoError(t, err)
	require.NotNil(t, ordinary.Merge, "the payload must pass merge validation independently of its operation namespace")

	cases := []struct {
		name string
		id   func(string, string, string) (string, error)
	}{
		{name: "manual merge", id: sdk.HumanManualMergeOperationID},
		{name: "pull creation", id: sdk.HumanPullCreateOperationID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := tc.id("2", "1", "command-1")
			require.NoError(t, err)
			intent, err := validate(id)
			require.ErrorIs(t, err, ErrInvalidIntent)
			require.Nil(t, intent)
		})
	}
}

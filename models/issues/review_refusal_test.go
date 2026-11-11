// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"errors"
	"testing"

	"forgejo.org/modules/json"
	"forgejo.org/modules/util"

	"github.com/stretchr/testify/require"
)

func TestExactReviewProducerSeparatesCodeFromDiagnosticAndPreservesErrorType(t *testing.T) {
	err := checkExactReviewTarget(&Issue{IsClosed: true}, &PullRequest{}, ExactReviewTarget{})
	refused, ok := err.(ErrExactReviewRefused)
	require.True(t, ok)
	require.True(t, IsErrExactReviewRefused(err))
	require.Equal(t, ReviewRefusalPullRequestIsClosedOrMerged, refused.Code)
	require.Equal(t, "pull request is closed or merged", refused.Reason)
	require.Equal(t, "exact review refused: pull request is closed or merged", err.Error())
	require.True(t, errors.Is(err, util.ErrInvalidArgument))
	encoded, marshalErr := json.Marshal(refused)
	require.NoError(t, marshalErr)
	require.JSONEq(t, `{"Reason":"pull request is closed or merged"}`, string(encoded))
	refused.Reason = "head changed"
	require.Equal(t, ReviewRefusalPullRequestIsClosedOrMerged, refused.RefusalCode())
	legacy := ErrExactReviewRefused{Reason: "head changed"}
	require.Equal(t, ReviewRefusalHeadChanged, legacy.RefusalCode())
}

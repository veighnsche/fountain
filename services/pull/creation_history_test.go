// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"strings"
	"testing"

	"forgejo.org/modules/git"

	"github.com/stretchr/testify/require"
)

func TestPullRequestCreationHistoryPreservesNativeBytes(t *testing.T) {
	oldest, newest := strings.Repeat("1", 40), strings.Repeat("2", 40)
	commits := []*git.Commit{{ID: git.MustIDFromString(newest)}, {ID: git.MustIDFromString(oldest)}}
	history, err := PullRequestCreationHistory(commits)
	require.NoError(t, err)
	require.Equal(t, `{"is_force_push":false,"commit_ids":["`+oldest+`","`+newest+`"]}`, history)
	require.Equal(t, newest, commits[0].ID.String(), "encoding must not reorder the comparison owned by its caller")
	empty, err := PullRequestCreationHistory(nil)
	require.NoError(t, err)
	require.Empty(t, empty, "an empty comparison has no native history comment")
}

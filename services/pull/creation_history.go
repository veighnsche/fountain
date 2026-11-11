// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	issues_model "forgejo.org/models/issues"
	"forgejo.org/modules/git"
	"forgejo.org/modules/json"
)

// PullRequestCreationHistory is the native initial push-history encoding.
// An empty comparison creates no history comment; comparison commits are
// recorded oldest first, preserving the existing content bytes.
func PullRequestCreationHistory(commits []*git.Commit) (string, error) {
	if len(commits) == 0 {
		return "", nil
	}
	data := issues_model.PushActionContent{IsForcePush: false, CommitIDs: make([]string, 0, len(commits))}
	for i := len(commits) - 1; i >= 0; i-- {
		data.CommitIDs = append(data.CommitIDs, commits[i].ID.String())
	}
	raw, err := json.Marshal(data)
	return string(raw), err
}

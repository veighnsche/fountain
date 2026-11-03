// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

import (
	"context"
	"errors"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	pull_model "forgejo.org/models/pull"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/timeutil"
)

var ErrMergeMetadata = errors.New("native merge metadata differs from the committed merge")

// ApplyMergeMetadata is the shared native SQL merge completion owner. It uses
// SetMerged for issue closure, history and counters, and consumes a scheduled
// merge in the same transaction. Exact replay makes no new history; a different
// prior merge refuses. Notifications, cross references and ref writes are separate.
func ApplyMergeMetadata(ctx context.Context, pullID int64, merger *user_model.User, commitID, mergeBase string) (changed bool, err error) {
	if pullID < 1 || merger == nil || merger.ID < 1 || commitID == "" {
		return false, ErrMergeMetadata
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
			return err
		}
		// Lock the natural idempotence marker before reading its merged state.
		if _, err := db.GetEngine(ctx).Exec("UPDATE `pull_request` SET `issue_id`=`issue_id` WHERE `id`=?", pullID); err != nil {
			return err
		}
		pr, err := issues_model.GetPullRequestByID(ctx, pullID)
		if err != nil {
			return err
		}
		if err := pr.LoadIssue(ctx); err != nil {
			return err
		}
		if pr.HasMerged {
			if pr.MergedCommitID != commitID || pr.MergerID != merger.ID || pr.MergeBase != mergeBase || pr.MergedUnix <= 0 || !pr.Issue.IsClosed {
				return ErrMergeMetadata
			}
			return nil
		}
		pr.MergedCommitID, pr.MergeBase = commitID, mergeBase
		pr.MergedUnix, pr.Merger, pr.MergerID = timeutil.TimeStampNow(), merger, merger.ID
		if err := pull_model.DeleteScheduledAutoMerge(ctx, pr.ID); err != nil && !db.IsErrNotExist(err) {
			return err
		}
		changed, err = pr.SetMerged(ctx)
		return err
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

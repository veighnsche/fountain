// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"context"
	"errors"

	"forgejo.org/models/db"

	"xorm.io/builder"
)

// FindIssueCommentsAfterID reads a bounded, ascending ID window and counts all
// comments remaining after the cursor. Native chronological comment reads keep
// using FindComments; an ID cursor must not page that different ordering.
func FindIssueCommentsAfterID(ctx context.Context, issueID, afterID int64, limit int) (CommentList, int64, error) {
	if issueID <= 0 || afterID < 0 || limit <= 0 {
		return nil, 0, errors.New("invalid comment ID window")
	}
	cond := builder.Eq{"issue_id": issueID}.And(builder.Gt{"id": afterID})
	total, err := db.GetEngine(ctx).Where(cond).Count(new(Comment))
	if err != nil {
		return nil, 0, err
	}
	var comments CommentList
	err = db.GetEngine(ctx).Where(cond).Asc("id").Limit(limit).Find(&comments)
	return comments, total, err
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"context"
	"errors"

	"forgejo.org/models/db"

	"xorm.io/builder"
)

// FindIssueReviewsAfterID reads a bounded, ascending ID window and counts all
// reviews remaining after the cursor, independently of native chronological
// review-list ordering.
func FindIssueReviewsAfterID(ctx context.Context, issueID, afterID int64, limit int) (ReviewList, int64, error) {
	if issueID <= 0 || afterID < 0 || limit <= 0 {
		return nil, 0, errors.New("invalid review ID window")
	}
	cond := builder.Eq{"issue_id": issueID}.And(builder.Gt{"id": afterID})
	total, err := db.GetEngine(ctx).Where(cond).Count(new(Review))
	if err != nil {
		return nil, 0, err
	}
	var reviews ReviewList
	err = db.GetEngine(ctx).Where(cond).Asc("id").Limit(limit).Find(&reviews)
	return reviews, total, err
}

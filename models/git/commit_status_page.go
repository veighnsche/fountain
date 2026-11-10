// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"errors"

	"forgejo.org/models/db"

	"xorm.io/builder"
)

// FindLatestCommitStatusesAfterID selects the latest native index for every
// context before applying the exclusive ID cursor. Applying the cursor inside
// the context selection could resurrect an older status. Total counts all
// latest contexts remaining after the cursor, while rows are bounded by limit.
func FindLatestCommitStatusesAfterID(ctx context.Context, repoID int64, sha string, afterID int64, limit int) ([]*CommitStatus, int64, error) {
	if repoID <= 0 || afterID < 0 || limit <= 0 {
		return nil, 0, errors.New("invalid commit status ID window")
	}
	base := builder.Eq{"repo_id": repoID, "sha": sha}
	latest := builder.Select("max(`index`)").From("commit_status").Where(base).GroupBy("context_hash")
	cond := base.And(builder.In("`index`", latest)).And(builder.Gt{"id": afterID})
	total, err := db.GetEngine(ctx).Where(cond).Count(new(CommitStatus))
	if err != nil {
		return nil, 0, err
	}
	var statuses []*CommitStatus
	err = db.GetEngine(ctx).Where(cond).Asc("id").Limit(limit).Find(&statuses)
	return statuses, total, err
}

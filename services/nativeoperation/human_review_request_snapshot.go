// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
)

var ErrHumanReviewRequestTooLarge = errors.New("complete selected review request set exceeds bounds")

// Persisted columns only: native associations and later hydration never enter
// the defining digest. This also lets the primary prove historical flag changes
// without exposing completed or pending review prose.
type humanReviewRequestRow struct {
	ID, IssueID, ReviewerID, TeamID, OriginalAuthorID int64
	Type                                              issues_model.ReviewType
	Content, OriginalAuthor, CommitID                 string
	Official, Stale, Dismissed                        bool
	Created, Updated                                  int64
}

func humanReviewRequestStored(r *issues_model.Review) humanReviewRequestRow {
	return humanReviewRequestRow{r.ID, r.IssueID, r.ReviewerID, r.ReviewerTeamID, r.OriginalAuthorID, r.Type, r.Content, r.OriginalAuthor, r.CommitID, r.Official, r.Stale, r.Dismissed, int64(r.CreatedUnix), int64(r.UpdatedUnix)}
}

func humanReviewRequestDigest(rows []humanReviewRequestRow) (string, error) {
	raw, err := json.Marshal(rows)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ReadHumanReviewRequestEvidence is shared by preparation and the conditional
// native writer. Read only request rows: a selected human's private pending
// review or prose from completed reviews is outside this evidence.
func ReadHumanReviewRequestEvidence(ctx context.Context, issueID int64, kind string, reviewerID int64) (sdk.HumanReviewRequestEvidence, error) {
	e := sdk.HumanReviewRequestEvidence{RequestIDs: []string{}}
	if issueID <= 0 || reviewerID <= 0 || kind != "user" && kind != "team" {
		return e, errors.New("invalid review request evidence scope")
	}
	query := db.GetEngine(ctx).Where("issue_id=? AND type=?", issueID, issues_model.ReviewTypeRequest)
	if kind == "user" {
		query = query.And("reviewer_id=? AND reviewer_team_id=0 AND original_author_id=0", reviewerID)
	} else {
		query = query.And("reviewer_team_id=?", reviewerID)
	}
	var reviews []*issues_model.Review
	if err := query.Asc("id").Limit(sdk.MaxReviewRequestSnapshotRecords + 1).Find(&reviews); err != nil {
		return e, err
	}
	if len(reviews) > sdk.MaxReviewRequestSnapshotRecords {
		return e, ErrHumanReviewRequestTooLarge
	}
	// Serialize only persisted columns. Associated objects, future hydration and
	// map iteration cannot change the digest. Exact strings and timestamps matter.
	rows := make([]humanReviewRequestRow, 0, len(reviews))
	total := 0
	for _, r := range reviews {
		if !utf8.ValidString(r.Content) || !utf8.ValidString(r.OriginalAuthor) || !utf8.ValidString(r.CommitID) {
			return e, errors.New("invalid persisted review request text")
		}
		total += len(r.Content) + len(r.OriginalAuthor) + len(r.CommitID)
		if total > sdk.MaxReviewRequestSnapshotBodyBytes {
			return e, ErrHumanReviewRequestTooLarge
		}
		e.RequestIDs = append(e.RequestIDs, strconv.FormatInt(r.ID, 10))
		rows = append(rows, humanReviewRequestStored(r))
	}
	digest, err := humanReviewRequestDigest(rows)
	e.Digest = digest
	return e, err
}

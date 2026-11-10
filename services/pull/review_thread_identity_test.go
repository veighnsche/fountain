// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"strings"
	"testing"

	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/timeutil"

	"github.com/stretchr/testify/require"
)

func TestLegacyLocationReplyRefusesAmbiguousOrOversizedContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows int
	}{
		{"different original contexts", 3},
		{"over ten thousand retained comments", 10001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, unittest.PrepareTestDatabase())
			issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 3})
			require.NoError(t, issue.LoadRepo(t.Context()))
			actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
			for offset := 0; offset < tc.rows; offset += 100 {
				var rows []*issues_model.Comment
				for i := offset; i < min(offset+100, tc.rows); i++ {
					comment := &issues_model.Comment{
						ID: int64(20000 + i), IssueID: issue.ID, ReviewID: 6, Type: issues_model.CommentTypeCode,
						PosterID: 2, TreePath: "retained.go", Line: 7, Content: "retained reply",
						CreatedUnix: timeutil.TimeStamp(1700000000 + i), UpdatedUnix: timeutil.TimeStamp(1700100000),
					}
					if tc.rows == 3 && i > 0 {
						comment.OriginalAnchor = &issues_model.CodeCommentAnchor{
							Base: strings.Repeat("1", 40), Head: strings.Repeat(string(rune('1'+i)), 40),
							Path: "retained.go", Side: "new", Scope: "lines", StartLine: 7, EndLine: 7,
						}
					}
					rows = append(rows, comment)
				}
				unittest.AssertSuccessfulInsert(t, rows)
			}
			opts := &issues_model.FindCommentsOptions{IssueID: issue.ID, ReviewID: 6, TreePath: "retained.go", Line: 7, Type: issues_model.CommentTypeCode}
			before, err := issues_model.CountComments(t.Context(), opts)
			require.NoError(t, err)
			comment, err := CreateCodeCommentKnownReviewID(t.Context(), actor, issue.Repo, issue,
				"must not attach", "retained.go", "", "", 7, 6, nil)
			require.EqualError(t, err, "reply location has ambiguous original context")
			require.Nil(t, comment)
			after, err := issues_model.CountComments(t.Context(), opts)
			require.NoError(t, err)
			require.Equal(t, before, after, "refusal must not create a comment in an arbitrary conversation")
		})
	}
}

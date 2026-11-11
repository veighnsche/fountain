// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"context"
	"strings"
	"unicode/utf8"

	"forgejo.org/models/db"
)

const MaxPreparedHumanInlineBodyBytes = 64 << 10

// PreparedHumanInlineTarget separates captured conditions from trusted Git
// evidence freshly read by the internal caller under native ownership. Neither
// Current* proof nor ownership can come from a browser or SDK JSON payload.
// The caller owns live session/deadline/revision and exact receipt admission.
type PreparedHumanInlineTarget struct {
	Review                                               PreparedHumanReviewTarget
	Anchor                                               CodeCommentAnchor
	EntryOID, EntryKind, DiffDigest                      string
	CurrentAnchor                                        CodeCommentAnchor
	CurrentEntryOID, CurrentEntryKind, CurrentDiffDigest string
}
type PreparedHumanInlineResult struct {
	Review  *Review
	Inline  *Comment
	History *Comment // nil for a pending draft; finalization creates native review history
}

func validPreparedInlineDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// CreatePreparedHumanInlineComment joins the caller's transaction and composes
// only native SQL. Draft creation/continuation preserves the summary and every
// older inline's original context. Immediate publication explicitly consumes the
// own pending review, preserving its summary, through ordinary native SubmitReview.
// It performs no Git/blame I/O, notifications, replay or attachment handling.
func CreatePreparedHumanInlineComment(ctx context.Context, content string, draft bool, want PreparedHumanInlineTarget) (*PreparedHumanInlineResult, error) {
	if err := validatePreparedHumanReviewTarget(ctx, want.Review); err != nil {
		return nil, err
	}
	refuse := func(code ExactReviewRefusalCode, reason string) (*PreparedHumanInlineResult, error) {
		return nil, NewExactReviewRefusal(code, reason)
	}
	a := want.Anchor
	if a.Validate() != nil || a.EndLine > 9007199254740991 || a.Base != want.Review.MergeBaseOID || a.Head != want.Review.HeadOID || !validFullOID(want.EntryOID) || want.EntryOID != strings.ToLower(want.EntryOID) || !validPreparedInlineDigest(want.DiffDigest) || want.EntryKind != "blob" && (want.EntryKind != "commit" || a.Scope != "file") {
		return refuse(ReviewRefusalInvalidHumanInlineTarget, "invalid human inline target")
	}
	if a != want.CurrentAnchor || want.EntryOID != want.CurrentEntryOID || want.EntryKind != want.CurrentEntryKind || want.DiffDigest != want.CurrentDiffDigest {
		return refuse(ReviewRefusalInlineProofChanged, "inline proof changed")
	}
	if len(content) > MaxPreparedHumanInlineBodyBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return refuse(ReviewRefusalInvalidHumanInlineBody, "invalid human inline body")
	}
	if strings.TrimSpace(content) == "" {
		return nil, ContentEmptyErr{}
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, err
	}
	defer committer.Close()
	actor, issue, review, err := loadPreparedHumanReview(ctx, want.Review)
	if err != nil {
		return nil, err
	}
	if review == nil {
		review, err = CreateReview(ctx, CreateReviewOptions{Type: ReviewTypePending, Issue: issue, Reviewer: actor, CommitID: want.Review.HeadOID})
		if err != nil {
			return nil, err
		}
	}
	line := a.EndLine
	commit := a.Head
	if a.Side == "old" {
		line = -line
		commit = a.Base
	}
	inline, err := CreateComment(ctx, &CreateCommentOptions{Type: CommentTypeCode, Doer: actor, Repo: issue.Repo, Issue: issue, Content: content, TreePath: a.Path, LineNum: line, OriginalAnchor: a.Copy(), CommitSHA: commit, ReviewID: review.ID})
	if err != nil {
		return nil, err
	}
	result := &PreparedHumanInlineResult{Review: review, Inline: inline}
	if !draft {
		result.Review, result.History, err = SubmitReview(ctx, actor, issue, ReviewTypeComment, review.Content, want.Review.HeadOID, false, nil)
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := committer.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateReviewSubmitPayload(t *testing.T) {
	valid := ReviewSubmitPayload{
		PullRequestNumber: 3,
		PRAuthorID:        1,
		HeadRepositoryID:  7,
		HeadRef:           "refs/heads/feature",
		BaseRef:           "refs/heads/main",
		ExpectedHeadOID:   strings.Repeat("1", 40),
		ExpectedBaseOID:   strings.Repeat("2", 40),
		CommitID:          strings.Repeat("1", 40),
		Event:             ReviewSubmitEventApproved,
		Body:              "looks good",
	}
	if err := ValidateReviewSubmitPayload(7, valid); err != nil {
		t.Fatalf("valid review-submit payload refused: %v", err)
	}
	approveEmpty := valid
	approveEmpty.Body = ""
	if err := ValidateReviewSubmitPayload(7, approveEmpty); err != nil {
		t.Fatalf("empty-body approval refused: %v", err)
	}
	reject := valid
	reject.Event = ReviewSubmitEventRequestChanges
	reject.Body = "please fix"
	if err := ValidateReviewSubmitPayload(7, reject); err != nil {
		t.Fatalf("valid request-changes payload refused: %v", err)
	}

	cases := map[string]ReviewSubmitPayload{
		"zero pr number": func() ReviewSubmitPayload { p := valid; p.PullRequestNumber = 0; return p }(),
		"zero author":    func() ReviewSubmitPayload { p := valid; p.PRAuthorID = 0; return p }(),
		"fork source":    func() ReviewSubmitPayload { p := valid; p.HeadRepositoryID = 9; return p }(),
		"identical refs": func() ReviewSubmitPayload { p := valid; p.BaseRef = "refs/heads/feature"; return p }(),
		"short head ref": func() ReviewSubmitPayload { p := valid; p.HeadRef = "feature"; return p }(),
		"bad head oid":   func() ReviewSubmitPayload { p := valid; p.ExpectedHeadOID = "xyz"; return p }(),
		"bad base oid":   func() ReviewSubmitPayload { p := valid; p.ExpectedBaseOID = "xyz"; return p }(),
		"commit differs": func() ReviewSubmitPayload { p := valid; p.CommitID = strings.Repeat("2", 40); return p }(),
		"empty comment":  func() ReviewSubmitPayload { p := valid; p.Event = "COMMENT"; p.Body = " "; return p }(),
		"pending event":  func() ReviewSubmitPayload { p := valid; p.Event = "PENDING"; return p }(),
		"empty event":    func() ReviewSubmitPayload { p := valid; p.Event = ""; return p }(),
		"reject without body": func() ReviewSubmitPayload {
			p := valid
			p.Event = ReviewSubmitEventRequestChanges
			p.Body = "  "
			return p
		}(),
	}
	for name, payload := range cases {
		if err := ValidateReviewSubmitPayload(7, payload); !errors.Is(err, ErrInvalidReviewSubmitPayload) {
			t.Fatalf("%s: expected ErrInvalidReviewSubmitPayload, got %v", name, err)
		}
	}
}

func TestValidateReviewSubmitPayloadComments(t *testing.T) {
	valid := ReviewSubmitPayload{
		PullRequestNumber: 3,
		PRAuthorID:        1,
		HeadRepositoryID:  7,
		HeadRef:           "refs/heads/feature",
		BaseRef:           "refs/heads/main",
		ExpectedHeadOID:   strings.Repeat("1", 40),
		ExpectedBaseOID:   strings.Repeat("2", 40),
		CommitID:          strings.Repeat("1", 40),
		Event:             ReviewSubmitEventRequestChanges,
		Body:              "please fix",
		Comments: []ReviewComment{
			{Key: "f1", Path: "a.go", Side: ReviewCommentSideNew, Line: 4, Body: "handle the new branch"},
			{Key: "f2", Path: "a.go", Side: ReviewCommentSideOld, Line: 2, Body: "removal drops a guard"},
		},
	}
	if err := ValidateReviewSubmitPayload(7, valid); err != nil {
		t.Fatalf("valid inline payload refused: %v", err)
	}

	mutate := func(f func(*ReviewSubmitPayload)) ReviewSubmitPayload {
		p := valid
		p.Comments = []ReviewComment{
			{Key: "f1", Path: "a.go", Side: ReviewCommentSideNew, Line: 4, Body: "handle the new branch"},
			{Key: "f2", Path: "a.go", Side: ReviewCommentSideOld, Line: 2, Body: "removal drops a guard"},
		}
		f(&p)
		return p
	}
	many := make([]ReviewComment, 0, MaxReviewComments+1)
	for i := 0; i <= MaxReviewComments; i++ {
		many = append(many, ReviewComment{Key: "k" + strings.Repeat("0", 10) + string(rune('a'+i/26)) + string(rune('a'+i%26)), Path: "a.go", Side: ReviewCommentSideNew, Line: int64(i + 1), Body: "b"})
	}
	cases := map[string]ReviewSubmitPayload{
		"empty key":      mutate(func(p *ReviewSubmitPayload) { p.Comments[0].Key = "" }),
		"key with space": mutate(func(p *ReviewSubmitPayload) { p.Comments[0].Key = "has space" }),
		"empty path":     mutate(func(p *ReviewSubmitPayload) { p.Comments[0].Path = "" }),
		"absolute path":  mutate(func(p *ReviewSubmitPayload) { p.Comments[0].Path = "/a.go" }),
		"dotdot path":    mutate(func(p *ReviewSubmitPayload) { p.Comments[0].Path = "../a.go" }),
		"untrimmed path": mutate(func(p *ReviewSubmitPayload) { p.Comments[0].Path = " a.go" }),
		"bad side":       mutate(func(p *ReviewSubmitPayload) { p.Comments[0].Side = "both" }),
		"zero line":      mutate(func(p *ReviewSubmitPayload) { p.Comments[0].Line = 0 }),
		"empty body":     mutate(func(p *ReviewSubmitPayload) { p.Comments[0].Body = "  " }),
		"duplicate keys": mutate(func(p *ReviewSubmitPayload) { p.Comments[1].Key = "f1" }),
		"duplicate anchor": mutate(func(p *ReviewSubmitPayload) {
			p.Comments[1].Key = "f3"
			p.Comments[1].Side = ReviewCommentSideNew
			p.Comments[1].Line = 4
		}),
		"too many": mutate(func(p *ReviewSubmitPayload) { p.Comments = many }),
	}
	for name, payload := range cases {
		if err := ValidateReviewSubmitPayload(7, payload); !errors.Is(err, ErrInvalidReviewSubmitPayload) {
			t.Fatalf("%s: expected ErrInvalidReviewSubmitPayload, got %v", name, err)
		}
	}
}

func TestValidateReviewSubmitPayloadSizeBound(t *testing.T) {
	base := ReviewSubmitPayload{
		PullRequestNumber: 3,
		PRAuthorID:        1,
		HeadRepositoryID:  7,
		HeadRef:           "refs/heads/feature",
		BaseRef:           "refs/heads/main",
		ExpectedHeadOID:   strings.Repeat("1", 40),
		ExpectedBaseOID:   strings.Repeat("2", 40),
		CommitID:          strings.Repeat("1", 40),
		Event:             ReviewSubmitEventApproved,
		Body:              "looks good",
	}
	roomy := base
	roomy.Body = strings.Repeat("b", 40<<10)
	if err := ValidateReviewSubmitPayload(7, roomy); err != nil {
		t.Fatalf("40 KiB payload refused: %v", err)
	}
	oversize := base
	oversize.Body = strings.Repeat("b", MaxReviewPayloadBytes)
	if err := ValidateReviewSubmitPayload(7, oversize); !errors.Is(err, ErrInvalidReviewSubmitPayload) {
		t.Fatalf("48 KiB-body payload accepted: %v", err)
	}
	size, err := EncodedReviewSubmitSize(base)
	if err != nil || size <= 0 || size >= MaxReviewPayloadBytes {
		t.Fatalf("unexpected encoded size %d, err %v", size, err)
	}
}

func TestReviewSubmitReceiptRoundTrip(t *testing.T) {
	receipt := ReviewSubmitReceipt{
		ReviewID:     11,
		CommentID:    12,
		Comments:     []ReviewCommentReceipt{{Key: "f1", CommentID: 13}},
		MergeBaseOID: strings.Repeat("3", 40),
	}
	size, err := EncodedReviewSubmitSize(ReviewSubmitPayload{})
	if err != nil || size <= 0 {
		t.Fatalf("size probe failed: %d, %v", size, err)
	}
	if receipt.ReviewID != 11 || receipt.Comments[0].CommentID != 13 || receipt.MergeBaseOID != strings.Repeat("3", 40) {
		t.Fatalf("receipt shape mismatch: %+v", receipt)
	}
}

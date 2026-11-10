// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/modules/json"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reviewSubmitPayload(number, author, headRepo int64, headRef, baseRef, headOID, baseOID, commit, event, body string) string {
	return fmt.Sprintf(`{"pull_request_number":%d,"pr_author_id":%d,`+
		`"head_repository_id":%d,`+
		`"head_ref":%q,"base_ref":%q,`+
		`"expected_head_oid":%q,"expected_base_oid":%q,`+
		`"commit_id":%q,"event":%q,"body":%q}`,
		number, author, headRepo, headRef, baseRef, headOID, baseOID, commit, event, body)
}

func TestParseReviewSubmitPayloadAcceptsExactInput(t *testing.T) {
	head := strings.Repeat("a", 40)
	base := strings.Repeat("b", 40)
	review, err := parseReviewSubmitPayload([]byte(reviewSubmitPayload(3, 1, 7,
		"refs/heads/feature", "refs/heads/main",
		strings.ToUpper(head), strings.ToUpper(base), strings.ToUpper(head),
		"APPROVED", "looks good")), 7)
	require.NoError(t, err)
	assert.Equal(t, int64(3), review.PullRequestNumber)
	assert.Equal(t, int64(1), review.PRAuthorID)
	assert.Equal(t, int64(7), review.HeadRepositoryID)
	assert.Equal(t, "refs/heads/feature", review.HeadRef)
	assert.Equal(t, "refs/heads/main", review.BaseRef)
	assert.Equal(t, head, review.ExpectedHeadOID)
	assert.Equal(t, base, review.ExpectedBaseOID)
	assert.Equal(t, head, review.CommitID)
	assert.Equal(t, "APPROVED", review.Event)
	assert.Equal(t, "looks good", review.Body)
	assert.Equal(t, issues_model.ReviewTypeApprove, reviewSubmitType(review.Event))
	assert.Equal(t, issues_model.ReviewTypeReject, reviewSubmitType("REQUEST_CHANGES"))
}

func TestParseReviewSubmitPayloadRejects(t *testing.T) {
	head := strings.Repeat("a", 40)
	base := strings.Repeat("b", 40)
	valid := func() string {
		return reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", head, base, head, "APPROVED", "looks good")
	}
	cases := map[string]string{
		"empty":          "",
		"unknown field":  `{"pull_request_number":3,"pr_author_id":1,"head_repository_id":7,"head_ref":"refs/heads/feature","base_ref":"refs/heads/main","expected_head_oid":"` + head + `","expected_base_oid":"` + base + `","commit_id":"` + head + `","event":"APPROVED","body":"b","drafts":[]}`,
		"trailing data":  valid() + `{}`,
		"zero pr number": reviewSubmitPayload(0, 1, 7, "refs/heads/feature", "refs/heads/main", head, base, head, "APPROVED", "b"),
		"zero author":    reviewSubmitPayload(3, 0, 7, "refs/heads/feature", "refs/heads/main", head, base, head, "APPROVED", "b"),
		"fork source":    reviewSubmitPayload(3, 1, 9, "refs/heads/feature", "refs/heads/main", head, base, head, "APPROVED", "b"),
		"identical refs": reviewSubmitPayload(3, 1, 7, "refs/heads/main", "refs/heads/main", head, base, head, "APPROVED", "b"),
		"short head ref": reviewSubmitPayload(3, 1, 7, "feature", "refs/heads/main", head, base, head, "APPROVED", "b"),
		"bad head oid":   reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", "xyz", base, head, "APPROVED", "b"),
		"bad base oid":   reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", head, "xyz", head, "APPROVED", "b"),
		"commit differs": reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", head, base, base, "APPROVED", "b"),
		"empty comment":  reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", head, base, head, "COMMENT", " "),
		"pending event":  reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", head, base, head, "PENDING", "b"),
		"empty event":    reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", head, base, head, "", "b"),
		"reject no body": reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", head, base, head, "REQUEST_CHANGES", "  "),
	}
	for name, payload := range cases {
		_, err := parseReviewSubmitPayload([]byte(payload), 7)
		require.ErrorIs(t, err, ErrInvalidIntent, name)
	}

	reject := reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", head, base, head, "REQUEST_CHANGES", "please fix")
	parsed, err := parseReviewSubmitPayload([]byte(reject), 7)
	require.NoError(t, err)
	assert.Equal(t, "REQUEST_CHANGES", parsed.Event)
}

func reviewSubmitPayloadWithComments(head, base string) string {
	return fmt.Sprintf(`{"pull_request_number":3,"pr_author_id":1,`+
		`"head_repository_id":7,`+
		`"head_ref":"refs/heads/feature","base_ref":"refs/heads/main",`+
		`"expected_head_oid":%q,"expected_base_oid":%q,`+
		`"commit_id":%q,"event":"REQUEST_CHANGES","body":"please fix",`+
		`"comments":[{"key":"f1","path":"a.go","side":"new","line":4,"body":"new finding"},`+
		`{"key":"f2","path":"a.go","side":"old","line":2,"body":"old finding"}]}`,
		head, base, head)
}

func TestParseReviewSubmitPayloadComments(t *testing.T) {
	head := strings.Repeat("a", 40)
	base := strings.Repeat("b", 40)
	parsed, err := parseReviewSubmitPayload([]byte(reviewSubmitPayloadWithComments(head, base)), 7)
	require.NoError(t, err)
	require.Len(t, parsed.Comments, 2)
	assert.Equal(t, "f1", parsed.Comments[0].Key)
	assert.Equal(t, "a.go", parsed.Comments[0].Path)
	assert.Equal(t, "new", parsed.Comments[0].Side)
	assert.Equal(t, int64(4), parsed.Comments[0].Line)
	assert.Equal(t, "old", parsed.Comments[1].Side)

	mutate := func(f func(string) string) string {
		return f(reviewSubmitPayloadWithComments(head, base))
	}
	big := strings.Repeat("b", 48<<10)
	cases := map[string]string{
		"bad side":         strings.Replace(mutate(func(s string) string { return s }), `"side":"new"`, `"side":"both"`, 1),
		"zero line":        strings.Replace(mutate(func(s string) string { return s }), `"line":4`, `"line":0`, 1),
		"empty key":        strings.Replace(mutate(func(s string) string { return s }), `"key":"f1"`, `"key":""`, 1),
		"duplicate keys":   strings.Replace(mutate(func(s string) string { return s }), `"key":"f2"`, `"key":"f1"`, 1),
		"duplicate anchor": strings.Replace(strings.Replace(mutate(func(s string) string { return s }), `"key":"f2"`, `"key":"f3"`, 1), `"side":"old","line":2`, `"side":"new","line":4`, 1),
		"bad comment body": strings.Replace(mutate(func(s string) string { return s }), `"body":"new finding"`, `"body":"  "`, 1),
		"oversize intent":  reviewSubmitPayload(3, 1, 7, "refs/heads/feature", "refs/heads/main", head, base, head, "APPROVED", big),
	}
	for name, payload := range cases {
		_, err := parseReviewSubmitPayload([]byte(payload), 7)
		require.ErrorIs(t, err, ErrInvalidIntent, name)
	}
}

func TestReviewSubmitReceiptKeepsNativeIDs(t *testing.T) {
	// The receipt round-trips through JSON unchanged so replay returns
	// the recorded native ids instead of minting new ones.
	receipt := ReviewSubmitReceipt{
		ReviewID: 11, CommentID: 12, ReviewerID: 2, PRID: 5, IssueID: 11,
		PRNumber: 5, CommitID: strings.Repeat("a", 40), Event: "APPROVED",
		HeadOID: strings.Repeat("a", 40), BaseOID: strings.Repeat("b", 40),
		Comments:     []sdk.ReviewCommentReceipt{{Key: "f1", CommentID: 13}},
		MergeBaseOID: strings.Repeat("c", 40),
	}
	encoded, err := json.Marshal(receipt)
	require.NoError(t, err)
	var decoded ReviewSubmitReceipt
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, receipt, decoded)
}

func TestMapExactReviewRefusal(t *testing.T) {
	cases := map[string]string{
		"head changed":                                 nativeoperation_model.ReasonStaleHead,
		"base changed":                                 nativeoperation_model.ReasonStaleBaseOrResult,
		"pending draft exists":                         nativeoperation_model.ReasonPendingReviewExists,
		"cannot review your own pull request":          nativeoperation_model.ReasonAuthorityLost,
		"reviewer is blocked":                          nativeoperation_model.ReasonAuthorityLost,
		"reviewer cannot read this pull request":       nativeoperation_model.ReasonAuthorityLost,
		"pull request is closed or merged":             nativeoperation_model.ReasonPRMismatch,
		"pr author mismatch":                           nativeoperation_model.ReasonPRMismatch,
		"head branch mismatch":                         nativeoperation_model.ReasonPRMismatch,
		"invalid object id":                            nativeoperation_model.ReasonNativeRefused,
		"commit must equal the expected head":          nativeoperation_model.ReasonNativeRefused,
		"only approve or reject reviews are supported": nativeoperation_model.ReasonNativeRefused,
		"invalid inline comment":                       nativeoperation_model.ReasonNativeRefused,
		"invalid inline anchor":                        nativeoperation_model.ReasonNativeRefused,
		"binary-only inline anchor":                    nativeoperation_model.ReasonNativeRefused,
		"duplicate inline anchor":                      nativeoperation_model.ReasonNativeRefused,
		"duplicate inline comment key":                 nativeoperation_model.ReasonNativeRefused,
		"too many inline comments":                     nativeoperation_model.ReasonNativeRefused,
		"merge base unavailable":                       nativeoperation_model.ReasonNativeRefused,
	}
	for reason, want := range cases {
		assert.Equal(t, want, mapExactReviewRefusal(issues_model.ErrExactReviewRefused{Reason: reason}), reason)
	}
	assert.Equal(t, nativeoperation_model.ReasonNativeRefused, mapExactReviewRefusal(issues_model.ContentEmptyErr{}))
	assert.Equal(t, nativeoperation_model.ReasonNativeRefused, mapExactReviewRefusal(errors.New("boom")))
	assert.Equal(t, nativeoperation_model.ReasonPRMismatch, mapExactReviewRefusal(issues_model.ErrExactReviewRefused{Reason: "future reason"}))
}

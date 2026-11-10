// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"encoding/json"
	"errors"
	"strings"
)

// Review submit events. Final comments carry no vote. Attachments, pending/draft
// creation, submission of an existing draft, editing and dismissal remain outside
// this operation. The values match the native API review states.
const (
	ReviewSubmitEventApproved       = "APPROVED"
	ReviewSubmitEventComment        = "COMMENT"
	ReviewSubmitEventRequestChanges = "REQUEST_CHANGES"
)

// MaxReviewPayloadBytes bounds the complete encoded review-submit intent,
// inline comments included. It matches the factory D04 native-review
// payload ceiling; the host refuses anything larger before admission.
const MaxReviewPayloadBytes = 48 << 10

// MaxReviewComments bounds the inline comments in one review submission,
// mirroring the factory findings-per-report cap.
const MaxReviewComments = 64

// Inline comment sides use native PR diff semantics: old is the merge-base
// tree, new is the exact head.
const (
	ReviewCommentSideOld = "old"
	ReviewCommentSideNew = "new"
)

// ReviewComment is one keyed inline finding anchored to the candidate
// diff: an opaque client correlation key, a repo-relative path, the diff
// side, the one-based actual diff line and the prose body. Keys never
// carry factory severity, disposition or classifier state; the host
// stores key plus prose in the native comment. For renames the old side
// uses the old path and the new side uses the new path. Context lines
// follow native rules and are anchorable on either side.
type ReviewComment struct {
	Key  string `json:"key"`
	Path string `json:"path"`
	Side string `json:"side"`
	Line int64  `json:"line"`
	Body string `json:"body"`
}

// ReviewSubmitPayload is the pull_request.review.submit intent payload: one
// final native review bound to its exact candidate, with an optional
// summary body and zero or more inline comments. The first interface
// covers same-repository PRs only, with commit_id equal to the expected
// head. The host validates authoritatively against the live candidate
// diff; the SDK validation below only catches submitter bugs early.
type ReviewSubmitPayload struct {
	PullRequestNumber int64           `json:"pull_request_number"`
	PRAuthorID        int64           `json:"pr_author_id"`
	HeadRepositoryID  int64           `json:"head_repository_id"`
	HeadRef           string          `json:"head_ref"`
	BaseRef           string          `json:"base_ref"`
	ExpectedHeadOID   string          `json:"expected_head_oid"`
	ExpectedBaseOID   string          `json:"expected_base_oid"`
	CommitID          string          `json:"commit_id"`
	Event             string          `json:"event"`
	Body              string          `json:"body"`
	Comments          []ReviewComment `json:"comments,omitempty"`
}

// ReviewCommentReceipt correlates one committed inline comment back to
// its client key.
type ReviewCommentReceipt struct {
	Key       string `json:"key"`
	CommentID int64  `json:"comment_id"`
}

// ReviewSubmitReceipt is the canonical recorded review-submit effect
// shape: the summary review comment id in comment_id, the per-key
// inline comment ids, the host-derived merge base the anchors were
// validated against, and the exact admitted identity envelope
// (reviewer, PR, issue, number, commit, event, head/base OIDs) the
// receipt certifies. This is the wire both the backend emits and
// consumers decode; no truncated or parallel shape exists.
type ReviewSubmitReceipt struct {
	ReviewID     int64                  `json:"review_id"`
	CommentID    int64                  `json:"comment_id"`
	ReviewerID   int64                  `json:"reviewer_id"`
	PRID         int64                  `json:"pr_id"`
	IssueID      int64                  `json:"issue_id"`
	PRNumber     int64                  `json:"pr_number"`
	CommitID     string                 `json:"commit_id"`
	Event        string                 `json:"event"`
	HeadOID      string                 `json:"head_oid"`
	BaseOID      string                 `json:"base_oid"`
	Comments     []ReviewCommentReceipt `json:"comments,omitempty"`
	MergeBaseOID string                 `json:"merge_base_oid,omitempty"`
}

// ErrInvalidReviewSubmitPayload reports a malformed review-submit payload.
var ErrInvalidReviewSubmitPayload = errors.New("invalid review-submit payload")

func validReviewSubmitPayloadOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func validReviewSubmitPayloadRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || len(ref) > 512 {
		return false
	}
	name := strings.TrimSpace(ref)
	if name != ref || strings.ContainsAny(ref, " ~^:?*\\") || strings.Contains(ref, "..") {
		return false
	}
	return len(strings.TrimPrefix(ref, "refs/heads/")) > 0
}

// validReviewCommentKey reports whether key is a usable opaque client
// correlation key: short, single-line and unambiguous as the first line
// of the stored native comment.
func validReviewCommentKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

// validReviewCommentPath reports whether path is a usable repo-relative
// diff path.
func validReviewCommentPath(path string) bool {
	if path == "" || len(path) > 1024 || path != strings.TrimSpace(path) {
		return false
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\x00") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// ValidateReviewComment checks one inline comment: a usable key and path,
// an old|new side, a one-based line and a non-empty body. Diff membership
// (added/deleted/context/rename lines, binary and duplicate-anchor
// refusal) is validated by the host against the live candidate diff.
func ValidateReviewComment(comment ReviewComment) error {
	if !validReviewCommentKey(comment.Key) {
		return ErrInvalidReviewSubmitPayload
	}
	if !validReviewCommentPath(comment.Path) {
		return ErrInvalidReviewSubmitPayload
	}
	switch comment.Side {
	case ReviewCommentSideOld, ReviewCommentSideNew:
	default:
		return ErrInvalidReviewSubmitPayload
	}
	if comment.Line < 1 {
		return ErrInvalidReviewSubmitPayload
	}
	if strings.TrimSpace(comment.Body) == "" {
		return ErrInvalidReviewSubmitPayload
	}
	return nil
}

// ValidateReviewComments checks the inline comment list: bounded count,
// individually valid comments, unique keys (receipt correlation) and no
// duplicate (path, side, line) anchors in one report.
func ValidateReviewComments(comments []ReviewComment) error {
	if len(comments) > MaxReviewComments {
		return ErrInvalidReviewSubmitPayload
	}
	keys := make(map[string]struct{}, len(comments))
	anchors := make(map[ReviewComment]struct{}, len(comments))
	for _, comment := range comments {
		if err := ValidateReviewComment(comment); err != nil {
			return err
		}
		if _, dup := keys[comment.Key]; dup {
			return ErrInvalidReviewSubmitPayload
		}
		keys[comment.Key] = struct{}{}
		anchor := ReviewComment{Path: comment.Path, Side: comment.Side, Line: comment.Line}
		if _, dup := anchors[anchor]; dup {
			return ErrInvalidReviewSubmitPayload
		}
		anchors[anchor] = struct{}{}
	}
	return nil
}

// EncodedReviewSubmitSize returns the JSON length of the payload as the
// host bounds it.
func EncodedReviewSubmitSize(payload ReviewSubmitPayload) (int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	return len(encoded), nil
}

// ValidateReviewSubmitPayload checks the structural review-submit intent
// rules: a positive PR number and author, a same-repository head, distinct
// full branch refs, valid exact OIDs, a commit equal to the expected head, a
// final event, a body for requested changes, a valid inline comment list
// and a complete encoded size within MaxReviewPayloadBytes. The host
// re-validates everything against live native state.
func ValidateReviewSubmitPayload(repositoryID int64, payload ReviewSubmitPayload) error {
	if payload.PullRequestNumber <= 0 || payload.PRAuthorID <= 0 {
		return ErrInvalidReviewSubmitPayload
	}
	if payload.HeadRepositoryID != repositoryID {
		return ErrInvalidReviewSubmitPayload
	}
	if !validReviewSubmitPayloadRef(payload.HeadRef) || !validReviewSubmitPayloadRef(payload.BaseRef) {
		return ErrInvalidReviewSubmitPayload
	}
	if payload.HeadRef == payload.BaseRef {
		return ErrInvalidReviewSubmitPayload
	}
	if !validReviewSubmitPayloadOID(payload.ExpectedHeadOID) || !validReviewSubmitPayloadOID(payload.ExpectedBaseOID) {
		return ErrInvalidReviewSubmitPayload
	}
	if !validReviewSubmitPayloadOID(payload.CommitID) || !strings.EqualFold(payload.CommitID, payload.ExpectedHeadOID) {
		return ErrInvalidReviewSubmitPayload
	}
	switch payload.Event {
	case ReviewSubmitEventApproved:
	case ReviewSubmitEventComment:
		if strings.TrimSpace(payload.Body) == "" {
			return ErrInvalidReviewSubmitPayload
		}
	case ReviewSubmitEventRequestChanges:
		if strings.TrimSpace(payload.Body) == "" {
			return ErrInvalidReviewSubmitPayload
		}
	default:
		return ErrInvalidReviewSubmitPayload
	}
	if err := ValidateReviewComments(payload.Comments); err != nil {
		return err
	}
	size, err := EncodedReviewSubmitSize(payload)
	if err != nil || size > MaxReviewPayloadBytes {
		return ErrInvalidReviewSubmitPayload
	}
	return nil
}

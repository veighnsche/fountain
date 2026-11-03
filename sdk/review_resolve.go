// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// ThreadResolvePayload is the pull_request.review.thread.resolve intent
// payload: one conditional resolution of an anchored native thread root
// bound to its exact PR/head/base, the expected complete conversation
// digest and resolved=true. Replies are members, never resolution
// targets; only resolution is supported, never unresolve via this
// operation. The host validates authoritatively against live native
// state and the enrolled resolve binding; the SDK validation below only
// catches submitter bugs early. Execution arrives with FW17; until then
// the kind admits but its stage reports unavailable.
type ThreadResolvePayload struct {
	HeadRef            string `json:"head_ref"`
	BaseRef            string `json:"base_ref"`
	ExpectedHeadOID    string `json:"expected_head_oid"`
	ExpectedBaseOID    string `json:"expected_base_oid"`
	ConversationDigest string `json:"conversation_digest"`
	PullRequestNumber  int64  `json:"pull_request_number"`
	HeadRepositoryID   int64  `json:"head_repository_id"`
	RootCommentID      int64  `json:"root_comment_id"`
	Resolved           bool   `json:"resolved"`
}

// ThreadResolveReceipt is the recorded resolve effect: the resolved root
// and the digest it matched. Resolver identity and revision travel in
// review snapshots, not in this receipt.
type ThreadResolveReceipt struct {
	ConversationDigest string `json:"conversation_digest"`
	PullRequestNumber  int64  `json:"pull_request_number"`
	RootCommentID      int64  `json:"root_comment_id"`
	Resolved           bool   `json:"resolved"`
}

// ErrInvalidThreadResolvePayload reports a malformed thread-resolve payload.
var ErrInvalidThreadResolvePayload = errors.New("invalid thread-resolve payload")

func validResolveDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

// ValidateThreadResolvePayload checks the structural thread-resolve
// intent rules: a positive PR number, a same-repository head, distinct
// full branch refs, distinct valid exact OIDs, a positive anchored root
// comment, a complete conversation digest and resolved=true. The host
// re-validates everything against live native state.
func ValidateThreadResolvePayload(repositoryID int64, payload ThreadResolvePayload) error {
	if payload.PullRequestNumber <= 0 || payload.RootCommentID <= 0 {
		return ErrInvalidThreadResolvePayload
	}
	if payload.HeadRepositoryID != repositoryID {
		return ErrInvalidThreadResolvePayload
	}
	if !validReviewSubmitPayloadRef(payload.HeadRef) || !validReviewSubmitPayloadRef(payload.BaseRef) {
		return ErrInvalidThreadResolvePayload
	}
	if payload.HeadRef == payload.BaseRef {
		return ErrInvalidThreadResolvePayload
	}
	if !validReviewSubmitPayloadOID(payload.ExpectedHeadOID) || !validReviewSubmitPayloadOID(payload.ExpectedBaseOID) {
		return ErrInvalidThreadResolvePayload
	}
	if strings.EqualFold(payload.ExpectedHeadOID, payload.ExpectedBaseOID) {
		return ErrInvalidThreadResolvePayload
	}
	if !validResolveDigest(payload.ConversationDigest) {
		return ErrInvalidThreadResolvePayload
	}
	if !payload.Resolved {
		return ErrInvalidThreadResolvePayload
	}
	return nil
}

// ResolveConversationMember is one thread member behind a resolution
// digest, in native (created_unix, id) order with the anchored root
// first. Field order and encoding match the Soda factory twin byte for
// byte; both sides must reproduce ResolveConversationLine exactly.
type ResolveConversationMember struct {
	OriginalAnchor *RepositoryInlineAnchor
	Body           string
	Location       string
	ResolverState  string
	ID             int64
	ContentVer     int64
}

// ResolveConversationLine renders one member in the canonical digest
// encoding shared with the Soda factory twin, followed by NUL and canonical
// original-anchor JSON (null for unknown context). Every member binds its own
// recorded selection rather than inheriting guessed context from the root.
func ResolveConversationLine(m ResolveConversationMember) string {
	anchor, _ := json.Marshal(m.OriginalAnchor)
	return strconv.FormatInt(m.ID, 10) + "\x00" + m.Body + "\x00" +
		strconv.FormatInt(m.ContentVer, 10) + "\x00" + m.Location + "\x00" + m.ResolverState + "\x00" + string(anchor)
}

// ResolveConversationDigest binds the expected complete conversation:
// member count plus the ordered member lines. Any reply, edit, unresolve
// or resolver-state change alters the digest and refuses the stale
// resolution.
func ResolveConversationDigest(lines []string) (string, error) {
	if len(lines) == 0 {
		return "", ErrInvalidThreadResolvePayload
	}
	sum := sha256.Sum256([]byte(strconv.Itoa(len(lines)) + "\n" + strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"errors"
	"strings"

	"forgejo.org/extension-sdk/gitsyntax"
)

// PRCreatePayload is the pull_request.create intent payload: one
// same-repository native PR over exact head/base refs and OIDs with final
// title and body. The first interface has no labels, assignees, milestone,
// attachments, fork source, update or retarget operation. The host validates
// authoritatively; the SDK validation below only catches submitter bugs
// early.
type PRCreatePayload struct {
	HeadRepositoryID    int64  `json:"head_repository_id"`
	HeadRef             string `json:"head_ref"`
	BaseRef             string `json:"base_ref"`
	ExpectedHeadOID     string `json:"expected_head_oid"`
	ExpectedBaseOID     string `json:"expected_base_oid"`
	Title               string `json:"title"`
	Body                string `json:"body"`
	AllowMaintainerEdit bool   `json:"allow_maintainer_edit"`
}

// MaxPRCreateTitle is the native issue-title width enforced by the host.
const MaxPRCreateTitle = 255

// ErrInvalidPRCreatePayload reports a malformed PR-create payload.
var ErrInvalidPRCreatePayload = errors.New("invalid pr-create payload")

func validPRCreatePayloadOID(oid string) bool {
	return gitsyntax.FullOID(oid)
}

func validPRCreatePayloadRef(ref string) bool {
	return gitsyntax.BranchRef(ref)
}

// ValidatePRCreatePayload checks the structural PR-create intent rules:
// same-repository head, distinct full branch refs, distinct exact OIDs, a
// non-blank in-limit title and no maintainer edits. The host re-validates
// everything against live native state.
func ValidatePRCreatePayload(repositoryID int64, payload PRCreatePayload) error {
	if payload.HeadRepositoryID != repositoryID {
		return ErrInvalidPRCreatePayload
	}
	if !validPRCreatePayloadRef(payload.HeadRef) || !validPRCreatePayloadRef(payload.BaseRef) {
		return ErrInvalidPRCreatePayload
	}
	if payload.HeadRef == payload.BaseRef {
		return ErrInvalidPRCreatePayload
	}
	if !validPRCreatePayloadOID(payload.ExpectedHeadOID) || !validPRCreatePayloadOID(payload.ExpectedBaseOID) {
		return ErrInvalidPRCreatePayload
	}
	if strings.EqualFold(payload.ExpectedHeadOID, payload.ExpectedBaseOID) {
		return ErrInvalidPRCreatePayload
	}
	if strings.TrimSpace(payload.Title) == "" || len(strings.TrimSpace(payload.Title)) > MaxPRCreateTitle {
		return ErrInvalidPRCreatePayload
	}
	if payload.AllowMaintainerEdit {
		return ErrInvalidPRCreatePayload
	}
	return nil
}

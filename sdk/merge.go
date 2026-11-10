// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"errors"
	"strings"

	"forgejo.org/extension-sdk/gitsyntax"
)

// MergeMethodFastForwardOnly is the only supported merge method. There is no
// fallback method and no parameter selecting another engine.
const MergeMethodFastForwardOnly = "fast-forward-only"

// ErrInvalidMergePayload reports a malformed merge payload.
var ErrInvalidMergePayload = errors.New("invalid merge payload")

func validMergePayloadOID(oid string) bool {
	return gitsyntax.FullOID(oid)
}

func validMergePayloadRef(ref string) bool {
	return gitsyntax.BranchRef(ref)
}

// ValidateMergePayload checks the structural merge intent rules: a positive
// PR number, a same-repository head, distinct full branch refs, distinct
// valid exact OIDs and the fast-forward-only method. The host re-validates
// everything against live native state.
func ValidateMergePayload(repositoryID int64, payload MergePayload) error {
	if payload.PullRequestNumber <= 0 {
		return ErrInvalidMergePayload
	}
	if payload.HeadRepositoryID != repositoryID {
		return ErrInvalidMergePayload
	}
	if !validMergePayloadRef(payload.HeadRef) || !validMergePayloadRef(payload.BaseRef) {
		return ErrInvalidMergePayload
	}
	if payload.HeadRef == payload.BaseRef {
		return ErrInvalidMergePayload
	}
	if !validMergePayloadOID(payload.ExpectedHeadOID) || !validMergePayloadOID(payload.ExpectedBaseOID) {
		return ErrInvalidMergePayload
	}
	if strings.EqualFold(payload.ExpectedHeadOID, payload.ExpectedBaseOID) {
		return ErrInvalidMergePayload
	}
	if payload.Method != MergeMethodFastForwardOnly {
		return ErrInvalidMergePayload
	}
	return nil
}

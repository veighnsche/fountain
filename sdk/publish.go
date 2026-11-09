// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"errors"
	"fmt"
	"strings"
)

// PublishOperationHeader carries the registered operation ID of a
// conditional publish on Fountain's supported smart-HTTP receive-pack
// route. The installation admission travels in AdmissionHeader alongside
// the native PAT. Unmodified Forgejo implements no such protocol.
const PublishOperationHeader = "X-Forgejo-Operation"

// PublishDesktopGrantHeader selects the public desktop conditional receive.
// It accompanies OAuth bearer authentication, PublishOperationHeader and the
// original X-Factory-Desktop-Proof; appliance admission cannot be mixed with it.
const PublishDesktopGrantHeader = "X-Factory-Operation-Grant"

// PublishExpectedOldAbsent is the explicit expected_old value for branch
// creation. Creation never uses an omitted or wildcard lease.
const PublishExpectedOldAbsent = "absent"

// PublishCorrection binds a correction publish to its existing pull
// request: the PR number within the repository and the expected PR author
// ID.
type PublishCorrection struct {
	Number           int64 `json:"number"`
	ExpectedAuthorID int64 `json:"expected_author_id"`
}

// PublishPayload is the git.ref.publish intent payload: one branch creation
// or fast-forward update over an exact old/new tuple, assessed against an
// exact comparison branch and tip, with an optional correction PR. A nil
// Correction means initial publication. The host validates authoritatively;
// the SDK validation below only catches submitter bugs early.
type PublishPayload struct {
	Ref                   string             `json:"ref"`
	ExpectedOld           string             `json:"expected_old"`
	NewOID                string             `json:"new_oid"`
	ComparisonRef         string             `json:"comparison_ref"`
	ExpectedComparisonOID string             `json:"expected_comparison_oid"`
	Correction            *PublishCorrection `json:"pull_request,omitempty"`
}

// ErrInvalidPublishPayload reports a malformed publish payload.
var ErrInvalidPublishPayload = errors.New("invalid publish payload")

func validPublishPayloadOID(oid string) bool {
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

func validPublishPayloadRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || len(ref) > 512 {
		return false
	}
	name := strings.TrimSpace(ref)
	if name != ref || strings.ContainsAny(ref, " ~^:?*\\") || strings.Contains(ref, "..") {
		return false
	}
	return len(strings.TrimPrefix(ref, "refs/heads/")) > 0
}

// ValidatePublishPayload checks the structural publish intent rules: full
// branch refs with ref distinct from comparison_ref, an explicit absent or
// full OID lease, an exact new commit different from the old tip, and a
// coherent correction binding. The host re-validates everything against
// live native state.
func ValidatePublishPayload(payload PublishPayload) error {
	if !validPublishPayloadRef(payload.Ref) || !validPublishPayloadRef(payload.ComparisonRef) {
		return ErrInvalidPublishPayload
	}
	if payload.Ref == payload.ComparisonRef {
		return ErrInvalidPublishPayload
	}
	if payload.ExpectedOld != PublishExpectedOldAbsent && !validPublishPayloadOID(payload.ExpectedOld) {
		return ErrInvalidPublishPayload
	}
	if !validPublishPayloadOID(payload.NewOID) || !validPublishPayloadOID(payload.ExpectedComparisonOID) {
		return ErrInvalidPublishPayload
	}
	if payload.ExpectedOld != PublishExpectedOldAbsent && strings.EqualFold(payload.NewOID, payload.ExpectedOld) {
		return ErrInvalidPublishPayload
	}
	if payload.Correction != nil {
		if payload.Correction.Number < 1 || payload.Correction.ExpectedAuthorID < 1 {
			return ErrInvalidPublishPayload
		}
		if payload.ExpectedOld == PublishExpectedOldAbsent {
			return ErrInvalidPublishPayload
		}
	}
	return nil
}

// PublishPushEnv returns git configuration environment entries that attach
// the operation binding headers to one smart-HTTP push without placing
// secrets in command arguments: GIT_CONFIG_COUNT/KEY/VALUE entries for two
// http.extraHeader values. The caller combines them with the push
// environment; credentials for the native origin stay in restricted secret
// files outside agent-accessible configuration.
func PublishPushEnv(operationID, admission string) ([]string, error) {
	if !validPublishOperationID(operationID) || !validBackgroundToken(admission) {
		return nil, ErrInvalidPublishPayload
	}
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=" + PublishOperationHeader + ": " + operationID,
		"GIT_CONFIG_KEY_1=http.extraHeader",
		"GIT_CONFIG_VALUE_1=" + AdmissionHeader + ": " + admission,
	}, nil
}

func validPublishOperationID(id string) bool {
	return validOperationID(id)
}

// FormatPublishRefspec returns the exact refspec pushing localRef to the
// authorized publish target. The push updates exactly the registered tuple;
// nothing else may ride along.
func FormatPublishRefspec(localRef, targetRef string) (string, error) {
	if !validPublishPayloadRef(targetRef) || strings.TrimSpace(localRef) == "" {
		return "", fmt.Errorf("%w: refspec", ErrInvalidPublishPayload)
	}
	return localRef + ":" + targetRef, nil
}

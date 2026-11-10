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
	"unicode/utf8"

	"forgejo.org/extension-sdk/operationcontract"
)

const OperationKindHumanMerge = "pull_request.human_merge.apply"
const HumanMergeAuthorizationRevision = "native-human-request"
const MaxHumanMergeMessageBytes = 64 << 10

// Allow JSON escaping of the exact message and its native preparation. Other
// callback operations retain their own small envelope, including lookup/cancel.
const MaxHumanMergeCallbackBytes = 512 << 10

var ErrHumanMergeQuery = errors.New("invalid human merge command capture")

// HumanMergeRequest captures exact preparation and message bytes before dispatch.
// It conveys no authority, computed result or successful merge receipt. A native
// writer must bind the saved command, held generation and live human again.
type HumanMergeRequest struct {
	OperationID string                  `json:"operation_id"`
	Expected    RepositoryMergeSnapshot `json:"expected"`
	Message     string                  `json:"message"`
	NotAfter    int64                   `json:"not_after"`
}

func ValidHumanMergeMessage(method, message string) bool {
	if !ValidRepositoryMergeSnapshotRequest(RepositoryMergeSnapshotRequest{Index: "1", Method: method}) || !utf8.ValidString(message) || len(message) > MaxHumanMergeMessageBytes || strings.ContainsRune(message, 0) {
		return false
	}
	return method == "rebase" || method == "fast-forward-only" || strings.TrimSpace(message) != ""
}

func ValidHumanMergeRequest(q HumanMergeRequest) bool {
	p := q.Expected
	return ValidHumanMergeOperationID(q.OperationID, p.ActorID, p.RepositoryID) &&
		q.NotAfter > 0 && p.NativeRevision >= 1 && !p.IsClosed && !p.HasMerged && p.Action.State == "allowed" &&
		ValidRepositoryMergeSnapshot(&p, p.RepositoryID, p.ActorID, RepositoryMergeSnapshotRequest{Index: p.Index, Method: p.Method}) &&
		utf8.ValidString(p.BaseBranch) && utf8.ValidString(p.Action.Reason) &&
		ValidHumanMergeMessage(p.Method, q.Message)
}

// CanonicalHumanMergeIntent is shared by the journal, gate and recovery. Native
// numeric identity/revision fields never pass through floating-point values.
func CanonicalHumanMergeIntent(q HumanMergeRequest) (string, string, error) {
	if !ValidHumanMergeRequest(q) {
		return "", "", ErrHumanMergeQuery
	}
	p := q.Expected
	actor, _ := strconv.ParseInt(p.ActorID, 10, 64)
	repo, _ := strconv.ParseInt(p.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string            `json:"operation_id"`
		ActorID                int64             `json:"actor_id"`
		RepositoryID           int64             `json:"repository_id"`
		Kind                   string            `json:"kind"`
		AuthorizationRevision  string            `json:"authorization_revision"`
		ExpectedNativeRevision int64             `json:"expected_native_revision"`
		NotAfter               int64             `json:"not_after"`
		Payload                HumanMergeRequest `json:"payload"`
	}{q.OperationID, actor, repo, OperationKindHumanMerge, HumanMergeAuthorizationRevision, p.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

func HumanMergeOperationID(actor, repository, command string) (string, error) {
	return humanOperationID(operationcontract.HumanMergePrefix, actor, repository, command, ErrHumanMergeQuery)
}

func ValidHumanMergeOperationID(id, actor, repository string) bool {
	return validHumanOperationID(operationcontract.HumanMergePrefix, id, actor, repository)
}

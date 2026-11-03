// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import "strconv"

// HumanMergeReceipt proves the primary ref effect, not completed PR metadata or
// notifications. Completion is a separate operation state. Private source fork
// locators, message prose, sessions and execution capabilities stay server-side.
type HumanMergeReceipt struct {
	RepositoryID int64    `json:"repository_id"`
	ActorID      int64    `json:"actor_id"`
	PullID       int64    `json:"pull_id"`
	IssueID      int64    `json:"issue_id"`
	Index        int64    `json:"index"`
	Method       string   `json:"method"`
	BaseBranch   string   `json:"base_branch"`
	HeadOID      string   `json:"head_oid"`
	BaseOID      string   `json:"base_oid"`
	MergeBaseOID string   `json:"merge_base_oid"`
	ResultOID    string   `json:"result_oid"`
	TreeOID      string   `json:"tree_oid"`
	Parents      []string `json:"parents"`
	IntentDigest string   `json:"intent_digest"`
}

// Structural binding is shared by native attribution and future receipt
// consumers. The native owner additionally proves admission, exact Git scope,
// persisted commit/tree/parent objects and immutable operation columns.
func ValidHumanMergeReceipt(r HumanMergeReceipt, q HumanMergeRequest) bool {
	p := q.Expected
	_, digest, err := CanonicalHumanMergeIntent(q)
	return err == nil && r.IntentDigest == digest && strconv.FormatInt(r.RepositoryID, 10) == p.RepositoryID && strconv.FormatInt(r.ActorID, 10) == p.ActorID && strconv.FormatInt(r.PullID, 10) == p.PullID && strconv.FormatInt(r.IssueID, 10) == p.IssueID && strconv.FormatInt(r.Index, 10) == p.Index && r.Method == p.Method && r.BaseBranch == p.BaseBranch && r.HeadOID == p.HeadOID && r.BaseOID == p.BaseOID && r.MergeBaseOID == p.MergeBaseOID && validHumanMergeReceiptShape(r)
}

// Lookup has no caller-supplied preparation. It still requires complete native
// object identity and the method's defining result topology, bound to its digest.
func validHumanMergeReceiptShape(r HumanMergeReceipt) bool {
	if r.RepositoryID < 1 || r.ActorID < 1 || r.PullID < 1 || r.IssueID < 1 || r.Index < 1 || !validReviewSubmitPayloadRef("refs/heads/"+r.BaseBranch) || !ValidRepositoryMergeSnapshotRequest(RepositoryMergeSnapshotRequest{Index: "1", Method: r.Method}) || !validHumanReviewDigest(r.IntentDigest) || !ValidPullDiffCommit(r.HeadOID) || !ValidPullDiffCommit(r.BaseOID) || !ValidPullDiffCommit(r.MergeBaseOID) || !ValidPullDiffCommit(r.ResultOID) || !ValidPullDiffCommit(r.TreeOID) || r.ResultOID == r.BaseOID || len(r.Parents) == 0 || len(r.ResultOID) != len(r.HeadOID) || len(r.BaseOID) != len(r.ResultOID) || len(r.MergeBaseOID) != len(r.ResultOID) || len(r.TreeOID) != len(r.ResultOID) {
		return false
	}
	for _, parent := range r.Parents {
		if !ValidPullDiffCommit(parent) || len(parent) != len(r.ResultOID) {
			return false
		}
	}
	switch r.Method {
	case "merge":
		return len(r.Parents) == 2 && r.Parents[0] == r.BaseOID && r.Parents[1] == r.HeadOID
	case "rebase-merge":
		return len(r.Parents) == 2 && r.Parents[0] == r.BaseOID
	case "squash":
		return len(r.Parents) == 1 && r.Parents[0] == r.BaseOID
	case "fast-forward-only":
		return r.ResultOID == r.HeadOID
	case "rebase":
		return true // The native owner proves the resulting base ancestry.
	default:
		return false
	}
}

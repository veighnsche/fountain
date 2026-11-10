// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"encoding/json" //nolint:depguard // Persisted witness bytes use standard-library JSON semantics.
	"errors"
	"strconv"
	"strings"
)

// Ordinary writer families with outer ownership. Each family records its
// affected resource identities in Scope before effects so offline recovery
// can reconcile that family's actual database/ref effects; a merge-tip
// comparison never reconciles another family.
const (
	// FamilyBranchCreate is FT03's ordinary branch-create writer.
	FamilyBranchCreate = "branch"
	// FamilyBranchDelete is the transaction-plus-direct-Git branch delete.
	FamilyBranchDelete = "branch-delete"
	// FamilyActionsTask is one Actions task/job update with its resulting
	// commit status, owned as a single logical update.
	FamilyActionsTask = "actions-task"
	// FamilyPushCompletion is one deferred push/completion batch.
	FamilyPushCompletion = "push-completion"
)

// ScopedRef is one expected ref effect within a multi-ref ordinary scope.
type ScopedRef struct {
	Ref              string           `json:"ref"`
	OldOID           string           `json:"old_oid,omitempty"`
	NewOID           string           `json:"new_oid,omitempty"`
	RepositoryTarget RepositoryTarget `json:"repository_target,omitempty"`
}

// Scope is the permitted effect recorded on a held reservation. Native hooks
// require the owner's execution proof and effects within this scope; anything
// else refuses.
type Scope struct {
	Kind             string           `json:"kind"`
	RepositoryID     int64            `json:"repository_id"`
	RepositoryTarget RepositoryTarget `json:"repository_target,omitempty"`
	// Ref is the single permitted branch ref: the merge target for a
	// conditional owner, the created or deleted branch for the ordinary
	// branch writers.
	Ref string `json:"ref,omitempty"`
	// OldOID and NewOID are the exact permitted ref tuple (conditional, or
	// the deleted tip and zero OID for a branch delete).
	OldOID string `json:"old_oid,omitempty"`
	NewOID string `json:"new_oid,omitempty"`
	// HeadRef and HeadOID bind the live source the candidate was verified
	// against (conditional).
	// HeadRepositoryID binds an explicit human PR source; zero retains the existing same-repository scope.
	HeadRepositoryID int64  `json:"head_repository_id,omitempty"`
	HeadRef          string `json:"head_ref,omitempty"`
	HeadOID          string `json:"head_oid,omitempty"`
	// PRNumber binds the native PR identity (conditional merge, or the
	// correction PR of a conditional publish).
	PRNumber int64 `json:"pr_number,omitempty"`
	// IssueID binds the exact native issue of a conditional lifecycle or
	// correction publish; LifecycleLabelID and LifecycleAssigneeID bind its bounded
	// label/assignee effect (conditional lifecycle).
	IssueID             int64 `json:"issue_id,omitempty"`
	LifecycleLabelID    int64 `json:"lifecycle_label_id,omitempty"`
	LifecycleAssigneeID int64 `json:"lifecycle_assignee_id,omitempty"`
	// LifecycleReviewerIDs binds the requested human reviewers of one
	// conditional reviewer-request write.
	LifecycleReviewerIDs []int64 `json:"lifecycle_reviewer_ids,omitempty"`
	// CorrectionAuthorID binds the expected correction-PR author
	// (conditional publish with a correction PR).
	CorrectionAuthorID int64 `json:"correction_author_id,omitempty"`
	CorrectionPRID     int64 `json:"correction_pr_id,omitempty"`
	// Family names the ordinary writer family (ordinary).
	Family string `json:"family,omitempty"`
	// TaskID, JobID, RunID and RunnerID identify one Actions logical
	// update (actions-task). JobID and RunID are resolved from the task
	// when the claim only knows the task.
	TaskID   int64 `json:"task_id,omitempty"`
	JobID    int64 `json:"job_id,omitempty"`
	RunID    int64 `json:"run_id,omitempty"`
	RunnerID int64 `json:"runner_id,omitempty"`
	// Refs lists every expected ref effect of a multi-ref batch
	// (push-completion). Single-ref families use Ref/OldOID/NewOID.
	Refs []ScopedRef `json:"refs,omitempty"`
	// ReceiveHasPushOptions fences interrupted receive policy changes: refs
	// alone cannot reconcile their repository setting effects.
	ReceiveHasPushOptions bool `json:"receive_has_push_options,omitempty"`
	// PusherID identifies the pusher whose refs one deferred batch covers.
	PusherID int64 `json:"pusher_id,omitempty"`
	// AuthorityOp names the operation of one ordinary authority writer
	// (user update, member change, key change ...), parsed from its
	// structured resource label. AuthorityID and AuthorityID2 identify
	// the operation's entities. Scopes without an attributable operation
	// (name-based creates, multi-entity batches) stay fenced on recovery.
	AuthorityOp  string `json:"authority_op,omitempty"`
	AuthorityID  int64  `json:"authority_id,omitempty"`
	AuthorityID2 int64  `json:"authority_id2,omitempty"`
	// CompletionRef, CompletionOldOID, CompletionNewOID and
	// CompletionPhase bind the single derived PR-ref write of a
	// conditional PR creation. Human merge uses CompletionPhase alone for its
	// durable one-shot native finishing and push-event claims. The internal ref is allocated at the
	// primary commit, so these persist after it and before derived Git
	// writes; hooks admit exactly this tuple as bounded completion.
	CompletionRef    string         `json:"completion_ref,omitempty"`
	CompletionOldOID string         `json:"completion_old_oid,omitempty"`
	CompletionNewOID string         `json:"completion_new_oid,omitempty"`
	CompletionPhase  string         `json:"completion_phase,omitempty"`
	NativeRename     *RefRenamePlan `json:"native_rename,omitempty"`
}

// PRCreateCompletionPhase is the allowed completion phase a conditional PR
// creation persists with its derived-ref tuple.
const PRCreateCompletionPhase = "prcreate-derived-ref"

const (
	FamilyRefWrite  = "ref-write"
	FamilyAuthority = "authority"
)

type RepositoryTarget string

const (
	TargetCode RepositoryTarget = "code"
	TargetWiki RepositoryTarget = "wiki"
)

var ErrScopeConflict = errors.New("reservation scope conflicts")

func EncodeScope(scope Scope) (string, error) {
	raw, err := json.Marshal(scope)
	return string(raw), err
}

// EncodeWriterScope retains the historical ordinary writer field order while
// sharing the complete Scope definition. Exact stored witnesses are opaque to
// callers, so adding fields must not discard them through a narrow projection.
func EncodeWriterScope(scope Scope) (string, error) {
	raw, err := EncodeScope(scope)
	if err != nil {
		return "", err
	}
	object, err := parseScopeObject(raw)
	if err != nil {
		return "", err
	}
	family, present := object.value("family")
	if !present {
		return raw, nil
	}
	raw = object.without("family")
	object, err = parseScopeObject(raw)
	if err != nil {
		return "", err
	}
	afterKind := object.fields["kind"].end
	return raw[:afterKind] + ",\"family\":" + family + raw[afterKind:], nil
}

func DecodeScope(raw string) (Scope, error) {
	var scope Scope
	if raw == "" {
		return scope, errors.New("reservation scope is missing")
	}
	err := json.Unmarshal([]byte(raw), &scope)
	return scope, err
}

func ConditionalOwner(installationID, operationID string) string {
	return "cond:" + installationID + "/" + operationID
}

func IsConditionalOwner(owner string) bool { return strings.HasPrefix(owner, "cond:") }

type AuthorityTarget struct {
	Operation string
	ID, ID2   int64
}

// ParseAuthorityResource is the retained label codec. New writers supply the
// target explicitly; old ambiguous/name-based labels remain unattributable.
func ParseAuthorityResource(resource string) (AuthorityTarget, bool) {
	segments := strings.Split(resource, "/")
	parts := make([]string, 0, len(segments))
	ids := make([]int64, 0, 2)
	for _, segment := range segments {
		if id, err := strconv.ParseInt(segment, 10, 64); err == nil && id > 0 {
			if len(ids) == 2 {
				return AuthorityTarget{}, false
			}
			ids = append(ids, id)
			continue
		}
		if segment == "" {
			return AuthorityTarget{}, false
		}
		if _, err := strconv.ParseInt(segment, 10, 64); err == nil {
			return AuthorityTarget{}, false
		}
		parts = append(parts, segment)
	}
	if len(parts) == 0 || len(ids) == 0 {
		return AuthorityTarget{}, false
	}
	target := AuthorityTarget{Operation: strings.Join(parts, "/"), ID: ids[0]}
	if len(ids) == 2 {
		target.ID2 = ids[1]
	}
	return target, true
}

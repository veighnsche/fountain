// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

// Package nativeoperation stores the generic conditional-operation ledger:
// durable operation identity with pre-submit cancellation tombstones, and the
// single exclusive native-mutation reservation with its native-state revision.
// It performs no factory policy.
package nativeoperation

import (
	"slices"

	"forgejo.org/extension-sdk/operationcontract"
	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"
)

// Background operation kinds mirror the SDK. Human kinds are separate
// and are not advertised for background enrollment. Authorization for one kind
// never grants another; each executes through its own guarded native boundary.
const (
	KindIssueGraph      = operationcontract.KindIssueGraph
	KindRefPublish      = operationcontract.KindRefPublish
	KindPRCreate        = operationcontract.KindPRCreate
	KindReviewSubmit    = operationcontract.KindReviewSubmit
	KindThreadResolve   = operationcontract.KindThreadResolve
	KindMerge           = operationcontract.KindMerge
	KindLifecycleIssue  = operationcontract.KindLifecycleIssue
	KindLifecycleReview = operationcontract.KindLifecycleReview
	KindLifecycleNote   = operationcontract.KindLifecycleNote
	// Human comments use live request admission, never background enrollment.
	KindHumanComment       = "discussion.comment.create"
	KindHumanEdit          = "discussion.edit"
	KindHumanIssueCreate   = "issue.create"
	KindHumanLock          = "discussion.lock.apply"
	KindHumanState         = "discussion.state.apply"
	KindHumanReview        = "pull_request.human_review.apply"
	KindHumanInline        = "pull_request.human_inline.create"
	KindHumanThread        = "pull_request.human_thread.apply"
	KindHumanReviewRequest = "pull_request.human_review_request.apply"
	KindHumanMerge         = "pull_request.human_merge.apply"
	KindHumanManualMerge   = "pull_request.human_manual_merge.apply"
	KindHumanPullCreate    = "pull_request.human_create.apply"
)

// Effect states for an operation's primary native effect.
const (
	EffectPending       = "pending"
	EffectNotCommitted  = "not_committed"
	EffectCommitted     = "committed"
	EffectIndeterminate = "indeterminate"
)

// Cancellation outcomes reported alongside the effect state.
const (
	CancellationNone          = "none"
	CancellationPending       = "pending"
	CancellationCancelled     = "cancelled"
	CancellationTooLate       = "too_late"
	CancellationIndeterminate = "indeterminate"
)

// Completion states for a committed effect's native bookkeeping.
const (
	CompletionPending           = "pending"
	CompletionComplete          = "complete"
	CompletionNeedsIntervention = "needs_intervention"
)

// Bounded refusal/receipt reasons recorded on operations.
const (
	ReasonCancelledBeforeSubmit    = "cancelled_before_submit"
	ReasonCancelledBeforeAdmission = "cancelled_before_admission"
	ReasonDuplicateAdmission       = "duplicate_admission"
	ReasonStaleNativeRevision      = "stale_native_revision"
	ReasonStaleHead                = "stale_head"
	ReasonStaleBaseOrResult        = "stale_base_or_result"
	ReasonStaleConversation        = "stale_conversation"
	ReasonExpiredBeforeAdmission   = "expired_before_admission"
	ReasonWrongOwner               = "wrong_owner"
	ReasonUnexpectedRefEffects     = "unexpected_ref_effects"
	ReasonNativeRefused            = "native_refused"
	ReasonAuthorityLost            = "authority_lost"
	ReasonIntentConflict           = "intent_conflict"
	ReasonPRMismatch               = "pr_mismatch"
	ReasonRecoveredNoEffect        = "recovered_no_effect"
	ReasonPublishOptionsRejected   = "publish_options_rejected"
	ReasonNotFastForward           = "not_fast_forward"
	ReasonDuplicatePullRequest     = "duplicate_pull_request"
	ReasonPendingReviewExists      = "pending_review_exists"
)

// Operation is one immutable conditional-operation authorization identified
// by (installation_id, operation_id), or a pre-submit cancellation tombstone
// when Submitted is false. CredentialFingerprint is private: it detects
// regeneration of the bound token row and must never appear in receipts.
type Operation struct {
	PackageID              string             `xorm:"VARCHAR(255) NOT NULL DEFAULT ''"`
	ControllerGrantID      string             `xorm:"VARCHAR(32) NOT NULL DEFAULT '' INDEX"`
	ID                     int64              `xorm:"pk autoincr"`
	InstallationID         string             `xorm:"VARCHAR(36) NOT NULL index unique(op)"`
	OperationID            string             `xorm:"VARCHAR(128) NOT NULL index unique(op)"`
	Kind                   string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	ActorID                int64              `xorm:"NOT NULL DEFAULT 0"`
	RepositoryID           int64              `xorm:"NOT NULL DEFAULT 0"`
	TokenID                int64              `xorm:"NOT NULL DEFAULT 0"`
	CredentialFingerprint  string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	AuthRevision           string             `xorm:"TEXT NOT NULL"`
	ExpectedNativeRevision int64              `xorm:"NOT NULL DEFAULT 0"`
	NotAfter               int64              `xorm:"NOT NULL DEFAULT 0"`
	IntentDigest           string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	Intent                 string             `xorm:"TEXT NOT NULL"`
	Submitted              bool               `xorm:"NOT NULL DEFAULT false"`
	Revoked                bool               `xorm:"NOT NULL DEFAULT false"`
	Admitted               bool               `xorm:"NOT NULL DEFAULT false"`
	EffectState            string             `xorm:"VARCHAR(16) NOT NULL DEFAULT 'pending'"`
	Reason                 string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	Cancellation           string             `xorm:"VARCHAR(16) NOT NULL DEFAULT 'none'"`
	Completion             string             `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"`
	Receipt                string             `xorm:"TEXT NOT NULL"`
	CreatedUnix            timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix            timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func init() {
	db.RegisterModel(new(Operation))
	db.RegisterModel(new(Reservation))
}

// supportedKinds is the single source of supported background operation
// kinds, sorted for stable generation digests and handshake responses.
var supportedKinds = operationcontract.BackgroundKinds()

// SupportedKinds returns a copy of the sorted supported operation kinds.
func SupportedKinds() []string {
	out := make([]string, len(supportedKinds))
	copy(out, supportedKinds)
	return out
}

// ValidKind reports whether kind is a supported background operation kind.
func ValidKind(kind string) bool {
	return slices.Contains(supportedKinds, kind)
}

// IsTerminal reports whether the effect state is final. Indeterminate keeps
// its fence and is not terminal: reconciliation may still resolve it.
func (op *Operation) IsTerminal() bool {
	return op.EffectState == EffectCommitted || op.EffectState == EffectNotCommitted
}

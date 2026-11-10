// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

// Direct, exact candidate-ref verification for the conditional publication
// contract (git.ref.publish).
//
// This file implements the intent-vs-state policies of a conditional publish
// in the operation seam. It parses the immutable publish intent,
// verifies it against live native Git/model state, validates the exact
// receive command set and verifies the realized result. It performs no
// receive itself and holds no reservation: the publish flow owns
// registration, admission, receiver launch, hook enforcement and the receipt
// commit. This entry runs under the caller's held ownership and never
// re-acquires a hold.
//
// Layering (what this file does not do):
//   - Native permission, push permission and branch protection stay effective
//     through the unmodified pre-receive hook inside the conditional receive.
//     This entry does not reimplement them and takes no actor.
//   - The prepared-hook exact-tuple enforcement and the reference-transaction
//     admission belong to the seam's hook owners. VerifyPublishCommands is the
//     pure command-set rule that hook calls; it is not a second hook.
//   - Registration, receiver launch, the operation receipt commit,
//     cancellation ordering, completion and offline recovery belong to the
//     publish flow in publish.go. PublishReceipt is the attributable record
//     shape the reconcile step persists; nothing here writes it.
//
// Method availability: publication permits one branch creation or
// fast-forward update only. It never creates a PR, changes another ref,
// deletes a branch or permits non-fast-forward replacement. There is no
// parameter, fallback or alternate path here that selects those.

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"io"
	"strings"

	"forgejo.org/extension-sdk/gitsyntax"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
)

// ExpectedOldAbsent is the explicit expected_old value for branch creation.
// Creation never uses an omitted or wildcard lease.
const ExpectedOldAbsent = "absent"

// PublishCorrection binds a correction publish to its existing PR: the PR
// number within the repository and the expected PR author ID.
type PublishCorrection struct {
	Number           int64 `json:"number"`
	ExpectedAuthorID int64 `json:"expected_author_id"`
	ExpectedPRID     int64 `json:"expected_pr_id,omitempty"`
	ExpectedIssueID  int64 `json:"expected_issue_id,omitempty"`
}

// PublishIntent carries the validated git.ref.publish payload: one branch
// creation or fast-forward update over an exact old/new tuple, assessed
// against an exact comparison branch and tip, with an optional correction PR.
// A nil Correction means initial publication.
type PublishIntent struct {
	Ref                   string             `json:"ref"`
	ExpectedOld           string             `json:"expected_old"`
	NewOID                string             `json:"new_oid"`
	ComparisonRef         string             `json:"comparison_ref"`
	ExpectedComparisonOID string             `json:"expected_comparison_oid"`
	Correction            *PublishCorrection `json:"pull_request"`
}

// IsCreation reports whether the intent creates its branch.
func (intent *PublishIntent) IsCreation() bool {
	return intent != nil && intent.ExpectedOld == ExpectedOldAbsent
}

// ErrInvalidPublishIntent reports a malformed, conflicting or out-of-scope
// publish intent. Intent validity is a submitter bug, not a terminal native
// outcome: the seam maps it to intent rejection before any claim.
var ErrInvalidPublishIntent = errors.New("invalid publish intent")

func validPublishOID(oid string) bool {
	return gitsyntax.FullOID(oid)
}

func validPublishBranchRef(ref string) bool {
	return gitsyntax.BranchRef(ref)
}

// ParsePublishPayload validates the complete immutable publish intent.
// Unknown fields, conflicting values and out-of-scope requests fail
// validation rather than silently broadening the request. OIDs are normalized
// to lowercase; expected_old keeps the explicit "absent" sentinel for
// creation.
func ParsePublishPayload(payload []byte) (*PublishIntent, error) {
	if len(payload) == 0 {
		return nil, ErrInvalidPublishIntent
	}
	var intent PublishIntent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return nil, ErrInvalidPublishIntent
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidPublishIntent
	}
	if !validPublishBranchRef(intent.Ref) || !validPublishBranchRef(intent.ComparisonRef) {
		return nil, ErrInvalidPublishIntent
	}
	if intent.Ref == intent.ComparisonRef {
		return nil, ErrInvalidPublishIntent
	}
	if intent.ExpectedOld != ExpectedOldAbsent {
		if !validPublishOID(intent.ExpectedOld) || isZeroOID(strings.ToLower(intent.ExpectedOld)) {
			return nil, ErrInvalidPublishIntent
		}
		intent.ExpectedOld = strings.ToLower(intent.ExpectedOld)
	}
	if !validPublishOID(intent.NewOID) || isZeroOID(strings.ToLower(intent.NewOID)) {
		return nil, ErrInvalidPublishIntent
	}
	intent.NewOID = strings.ToLower(intent.NewOID)
	if !validPublishOID(intent.ExpectedComparisonOID) || isZeroOID(strings.ToLower(intent.ExpectedComparisonOID)) {
		return nil, ErrInvalidPublishIntent
	}
	intent.ExpectedComparisonOID = strings.ToLower(intent.ExpectedComparisonOID)
	if intent.ExpectedOld != ExpectedOldAbsent && strings.EqualFold(intent.NewOID, intent.ExpectedOld) {
		// The new commit must differ from the old tip; an equal tuple is a
		// self-contradictory no-op intent, never an update.
		return nil, ErrInvalidPublishIntent
	}
	if intent.Correction != nil {
		if intent.Correction.Number < 1 || intent.Correction.ExpectedAuthorID < 1 {
			return nil, ErrInvalidPublishIntent
		}
		if intent.Correction.ExpectedPRID < 0 || intent.Correction.ExpectedIssueID < 0 ||
			(intent.Correction.ExpectedPRID == 0) != (intent.Correction.ExpectedIssueID == 0) {
			return nil, ErrInvalidPublishIntent
		}
		if intent.ExpectedOld == ExpectedOldAbsent {
			// A correction advances the recorded prior candidate, so it is
			// always an update; creation carries no pull request.
			return nil, ErrInvalidPublishIntent
		}
	}
	return &intent, nil
}

// Exact-publish refusal reasons. These strings are the mapping contract for
// the later conditional submit draft: target/state refusals map to a terminal
// not_committed outcome, while result refusals map to indeterminate with the
// fence retained, never to success or not_committed by guesswork.
const (
	ExactPublishRefusedBranchExists   = "branch already exists"
	ExactPublishRefusedMissingBranch  = "branch is missing"
	ExactPublishRefusedStaleOld       = "old tip changed"
	ExactPublishRefusedMissingCommit  = "new commit is missing"
	ExactPublishRefusedNotFastForward = "not a fast-forward from the old tip"
	ExactPublishRefusedMissingCompare = "comparison ref is missing"
	ExactPublishRefusedStaleCompare   = "comparison base changed"
	ExactPublishRefusedMissingPR      = "correction pull request is missing"
	ExactPublishRefusedClosedPR       = "correction pull request is closed or merged"
	ExactPublishRefusedPRMismatch     = "correction pr index/repository/branch/author mismatch"
	ExactPublishRefusedStalePRHead    = "correction pull request head changed"
	ExactPublishRefusedMalformedOID   = "malformed expected object id"
	ExactPublishRefusedExtraCommand   = "push updates more than the authorized ref"
	ExactPublishRefusedCommandRef     = "push ref does not match the authorized ref"
	ExactPublishRefusedCommandTuple   = "push old/new does not match the authorized tuple"
	ExactPublishRefusedResultMismatch = "realized ref does not match the authorized new commit"
	ExactPublishRefusedCompareMoved   = "comparison ref changed during publication"
)

// ErrExactPublishRefused reports an exact-publish refusal with a stable reason.
type ErrExactPublishRefused struct {
	Reason string
}

func (err ErrExactPublishRefused) Error() string {
	return "exact publish refused: " + err.Reason
}

// IsErrExactPublishRefused reports whether err is an exact-publish refusal.
func IsErrExactPublishRefused(err error) bool {
	var refused ErrExactPublishRefused
	return errors.As(err, &refused)
}

func refusePublish(reason string) (*PublishTarget, error) {
	return nil, ErrExactPublishRefused{Reason: reason}
}

// PublishTarget is the verified live target of a publish intent: the exact
// tuple the receiver must enforce and the realized comparison tip. OldOID is
// the repository's zero OID for creation.
type PublishTarget struct {
	Ref           string
	OldOID        string
	NewOID        string
	ComparisonRef string
	ComparisonOID string
	PRID          int64
	IssueID       int64
}

// VerifyPublishLaunchTarget verifies the pre-launch checks of a publish
// intent against current native state: the branch tip matches expected_old
// (absent for creation), the comparison ref still equals its expected tip,
// and a bound correction PR remains open/unmerged in this repository with
// the expected source, base, author and old head. It deliberately performs
// no new-commit check: the candidate objects arrive with the pushed pack
// after the claim, so requiring them here would refuse every genuine
// publication. Fast-forward order is enforced at pre-receive against the
// quarantined pack, and the tuple, comparison and correction are re-verified
// at prepared time. Every check reads live state; nothing here trusts a
// caller cache.
//
// The repository row is reloaded so the object format reads the current row.
// Native permission and branch protection are not checked here: they stay
// effective through the unmodified pre-receive hook inside the conditional
// receive.
func VerifyPublishLaunchTarget(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, intent *PublishIntent) (*PublishTarget, error) {
	return verifyPublishBaseTarget(ctx, repo, gitRepo, intent)
}

// VerifyExactPublishTarget verifies the intent against current native state:
// the branch tip matches expected_old (absent for creation), the new commit
// exists and fast-forwards from that tip for updates, the comparison ref
// still equals its expected tip, and a bound correction PR remains
// open/unmerged in this repository with the expected source, base, author and
// old head. Every check reads live state; nothing here trusts a caller cache.
//
// The repository row is reloaded so the object format reads the current row.
// Native permission and branch protection are not checked here: they stay
// effective through the unmodified pre-receive hook inside the conditional
// receive.
func VerifyExactPublishTarget(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, intent *PublishIntent) (*PublishTarget, error) {
	target, err := verifyPublishBaseTarget(ctx, repo, gitRepo, intent)
	if err != nil {
		return nil, err
	}
	// The new commit must exist and fast-forward from the old tip. Creation
	// seeds from any existing commit; only updates advance a tip.
	newCommit, err := gitRepo.GetCommit(target.NewOID)
	if err != nil {
		if git.IsErrNotExist(err) {
			return refusePublish(ExactPublishRefusedMissingCommit)
		}
		return nil, err
	}
	if !intent.IsCreation() {
		force, err := newCommit.IsForcePush(target.OldOID)
		if err != nil {
			return nil, err
		}
		if force {
			return refusePublish(ExactPublishRefusedNotFastForward)
		}
	}
	return target, nil
}

// verifyPublishBaseTarget verifies the launch-scoped checks shared by the
// pre-launch target verification and the full exact-target predicate: the
// object-format OID shapes, the branch tip against the exact old lease, the
// comparison base and the correction PR binding.
func verifyPublishBaseTarget(ctx context.Context, repo *repo_model.Repository, gitRepo *git.Repository, intent *PublishIntent) (*PublishTarget, error) {
	if repo == nil || gitRepo == nil || intent == nil {
		return refusePublish(ExactPublishRefusedStaleOld)
	}
	fresh, err := repo_model.GetRepositoryByID(ctx, repo.ID)
	if err != nil {
		return nil, err
	}
	objectFormat := git.ObjectFormatFromName(fresh.ObjectFormatName)
	if objectFormat == nil {
		return refusePublish(ExactPublishRefusedMalformedOID)
	}
	full := func(oid string) bool {
		return len(oid) == objectFormat.FullLength() && objectFormat.IsValid(oid)
	}
	if !full(intent.NewOID) || !full(intent.ExpectedComparisonOID) {
		return refusePublish(ExactPublishRefusedMalformedOID)
	}
	if !intent.IsCreation() && !full(intent.ExpectedOld) {
		return refusePublish(ExactPublishRefusedMalformedOID)
	}
	target := &PublishTarget{
		Ref:           intent.Ref,
		NewOID:        intent.NewOID,
		ComparisonRef: intent.ComparisonRef,
		ComparisonOID: intent.ExpectedComparisonOID,
	}
	// Branch tip against the exact old lease. A present branch refuses
	// creation and a missing branch refuses update: neither adopts the other
	// divergence, and matching bytes elsewhere never substitute for this tip.
	tip, err := gitRepo.GetRefCommitID(intent.Ref)
	if err != nil {
		if !git.IsErrNotExist(err) {
			return nil, err
		}
		if !intent.IsCreation() {
			return refusePublish(ExactPublishRefusedMissingBranch)
		}
		target.OldOID = objectFormat.EmptyObjectID().String()
	} else {
		if intent.IsCreation() {
			return refusePublish(ExactPublishRefusedBranchExists)
		}
		if !strings.EqualFold(tip, intent.ExpectedOld) {
			return refusePublish(ExactPublishRefusedStaleOld)
		}
		target.OldOID = strings.ToLower(tip)
	}
	// Comparison base the candidate was assessed against. A missing or
	// changed base rejects publication, including on an otherwise empty
	// repository: a candidate without its base cannot be attributed.
	compareTip, err := gitRepo.GetRefCommitID(intent.ComparisonRef)
	if err != nil {
		if !git.IsErrNotExist(err) {
			return nil, err
		}
		return refusePublish(ExactPublishRefusedMissingCompare)
	}
	if !strings.EqualFold(compareTip, intent.ExpectedComparisonOID) {
		return refusePublish(ExactPublishRefusedStaleCompare)
	}
	if intent.Correction != nil {
		pr, err := issues_model.GetPullRequestByIndex(ctx, fresh.ID, intent.Correction.Number)
		if err != nil {
			if issues_model.IsErrPullRequestNotExist(err) {
				return refusePublish(ExactPublishRefusedMissingPR)
			}
			return nil, err
		}
		if err := pr.LoadIssue(ctx); err != nil {
			return nil, err
		}
		if pr.HasMerged || pr.Issue.IsClosed {
			return refusePublish(ExactPublishRefusedClosedPR)
		}
		if pr.HeadRepoID != fresh.ID || pr.BaseRepoID != fresh.ID ||
			pr.HeadBranch != strings.TrimPrefix(intent.Ref, git.BranchPrefix) ||
			pr.BaseBranch != strings.TrimPrefix(intent.ComparisonRef, git.BranchPrefix) ||
			pr.Issue.PosterID != intent.Correction.ExpectedAuthorID {
			return refusePublish(ExactPublishRefusedPRMismatch)
		}
		if intent.Correction.ExpectedPRID != 0 &&
			(pr.ID != intent.Correction.ExpectedPRID || pr.IssueID != intent.Correction.ExpectedIssueID) {
			return refusePublish(ExactPublishRefusedPRMismatch)
		}
		// The recorded prior candidate is the current PR head: the tip
		// check above already bound it to expected_old.
		if !strings.EqualFold(tip, intent.ExpectedOld) {
			return refusePublish(ExactPublishRefusedStalePRHead)
		}
		target.PRID = pr.ID
		target.IssueID = pr.IssueID
	}
	return target, nil
}

// ReceiveLine is one parsed pre-receive/reference-transaction command line:
// the old and new OIDs with the full ref under update.
type ReceiveLine struct {
	Old string
	New string
	Ref string
}

// VerifyPublishCommands rejects any receive command set other than exactly
// the authorized old/new tuple on the authorized ref. zeroOID is the
// repository's zero OID, which stands for the absent side of a creation. The
// seam's pre-receive hook calls this before any ref can commit; the prepared
// hook re-enforces the same tuple.
func VerifyPublishCommands(intent *PublishIntent, zeroOID string, lines []ReceiveLine) error {
	if intent == nil {
		return ErrExactPublishRefused{Reason: ExactPublishRefusedExtraCommand}
	}
	if len(lines) != 1 {
		return ErrExactPublishRefused{Reason: ExactPublishRefusedExtraCommand}
	}
	line := lines[0]
	if line.Ref != intent.Ref {
		return ErrExactPublishRefused{Reason: ExactPublishRefusedCommandRef}
	}
	wantOld := intent.ExpectedOld
	if intent.IsCreation() {
		wantOld = zeroOID
	}
	if !strings.EqualFold(line.Old, wantOld) || !strings.EqualFold(line.New, intent.NewOID) {
		return ErrExactPublishRefused{Reason: ExactPublishRefusedCommandTuple}
	}
	return nil
}

// VerifyPublishResult verifies the realized pack result plus the comparison
// advertisement before the receive returns: the branch must realize exactly
// the authorized new commit and the comparison ref must still advertise its
// expected tip. The caller reads both back from live refs after receive-pack.
// A mismatch is unattributable: the wiring maps it to indeterminate with the
// fence retained, never to success or not_committed.
func VerifyPublishResult(intent *PublishIntent, realizedNew, realizedComparison string) error {
	if intent == nil {
		return ErrExactPublishRefused{Reason: ExactPublishRefusedResultMismatch}
	}
	if !strings.EqualFold(realizedNew, intent.NewOID) {
		return ErrExactPublishRefused{Reason: ExactPublishRefusedResultMismatch}
	}
	if !strings.EqualFold(realizedComparison, intent.ExpectedComparisonOID) {
		return ErrExactPublishRefused{Reason: ExactPublishRefusedCompareMoved}
	}
	return nil
}

// PublishReceipt attributes one realized branch publication: the exact ref,
// old/new OIDs, comparison snapshot, publishing actor and the optional bound
// correction PR. The seam's reconcile step persists this with the completion
// state; matching bytes or an already matching branch are never attribution.
type PublishReceipt struct {
	RepositoryID  int64  `json:"repository_id"`
	Ref           string `json:"ref"`
	OldOID        string `json:"old_oid"`
	NewOID        string `json:"new_oid"`
	ComparisonRef string `json:"comparison_ref"`
	ComparisonOID string `json:"comparison_oid"`
	ActorID       int64  `json:"actor_id"`
	PRID          int64  `json:"pr_id,omitempty"`
	IssueID       int64  `json:"issue_id,omitempty"`
}

// BuildPublishReceipt builds the attributable receipt for a verified result.
// The caller passes the verified target, the realized advertisement after
// receive-pack and the bound actor; a result mismatch refuses instead of
// recording a lookalike effect.
func BuildPublishReceipt(intent *PublishIntent, target *PublishTarget, repositoryID, actorID int64, realizedNew, realizedComparison string) (*PublishReceipt, error) {
	if intent == nil || target == nil || repositoryID < 1 || actorID < 1 {
		return nil, ErrExactPublishRefused{Reason: ExactPublishRefusedResultMismatch}
	}
	if err := VerifyPublishResult(intent, realizedNew, realizedComparison); err != nil {
		return nil, err
	}
	return &PublishReceipt{
		RepositoryID:  repositoryID,
		Ref:           target.Ref,
		OldOID:        target.OldOID,
		NewOID:        target.NewOID,
		ComparisonRef: target.ComparisonRef,
		ComparisonOID: target.ComparisonOID,
		ActorID:       actorID,
		PRID:          target.PRID,
		IssueID:       target.IssueID,
	}, nil
}

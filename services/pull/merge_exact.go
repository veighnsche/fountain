// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package pull

// Direct, exact fast-forward-only merge for the conditional merge contract.
//
// This entry implements the head/base/style/input policies of a conditional
// merge in the shipping merge owner. It reuses the native merge engine, the
// native permission/protection/review/status/mergeability checks and the
// prepared-admission plumbing owned by the operation seam; it adds no second
// merge engine and no unguarded push path.
//
// Direct path only: this entry executes the merge engine immediately. It never
// schedules delayed auto-merge, never consumes a scheduled auto-merge row and
// never marks a pull request manually merged. Scheduled auto-merge and manual
// marking stay ordinary-only paths.
//
// Transaction/hold discipline:
//   - No database transaction spans the Git prepare-to-push span. Native merge
//     bookkeeping commits in the post-receive hook's own transaction.
//   - The per-PR working-pool guard is held across the whole Merge call.
//   - The conditional reservation hold across prepare, push and reconciliation
//     belongs to the operation seam. This entry runs under the caller's
//     context: an ordinary context today, the seam's owned execution context
//     once the conditional submit wiring lands. It never re-acquires a hold.
//
// Method availability: the merge method is fixed to fast-forward-only by
// construction; this entry takes no style parameter. Whether that method is
// permitted is current native repository policy, never a caller preference.
// Nonstandard modes stay honestly unavailable: there is no parameter, fallback
// or alternate engine that selects them here.

import (
	"context"
	"errors"
	"strings"

	"forgejo.org/extension-sdk/gitsyntax"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
)

// Exact-merge refusal reasons. These strings are the mapping contract for the
// later conditional submit draft: each maps to a bounded SDK refusal reason.
// Native failures from the merge engine and mergeability checks pass through
// unchanged so the draft can map them with the existing error predicates.
const (
	ExactMergeRefusedMalformedRef     = "malformed head or base ref"
	ExactMergeRefusedMalformedOID     = "malformed expected object id"
	ExactMergeRefusedCrossRepo        = "cross-repository pull request is not supported"
	ExactMergeRefusedPRMismatch       = "pr index/head repository/head branch/base branch mismatch"
	ExactMergeRefusedClosedOrMerged   = "pull request is closed or merged"
	ExactMergeRefusedMissingHead      = "head ref is missing"
	ExactMergeRefusedMissingBase      = "base ref is missing"
	ExactMergeRefusedStaleHead        = "head changed"
	ExactMergeRefusedStaleBase        = "base changed"
	ExactMergeRefusedNoOp             = "head equals base; no target change"
	ExactMergeRefusedMethodNotAllowed = "fast-forward-only merge is not allowed by repository policy"
	ExactMergeRefusedResultMismatch   = "merge result does not match the expected head"
)

// ErrExactMergeRefused reports an exact-merge refusal with a stable reason.
type ErrExactMergeRefused struct {
	Reason string
}

func (err ErrExactMergeRefused) Error() string {
	return "exact merge refused: " + err.Reason
}

// IsErrExactMergeRefused reports whether err is an exact-merge refusal.
func IsErrExactMergeRefused(err error) bool {
	var refused ErrExactMergeRefused
	return errors.As(err, &refused)
}

func refuseExactMerge(reason string) (string, error) {
	return "", ErrExactMergeRefused{Reason: reason}
}

// MergeExactFastForward merges pr by fast-forwarding baseRef to the exact
// expected head. headRef and baseRef are full native branch refs; the expected
// OIDs are full object IDs in the repository's object format.
//
// The merge method needs no parameter: only fast-forward-only is exposed, and
// only when current native repository policy permits it. On success it returns
// the realized merge SHA read back from the base ref, which must equal the
// expected head OID: the caller binds that expected merge SHA for its presumed
// state and adopts the realized SHA only on this confirmed effect.
func MergeExactFastForward(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User, baseGitRepo *git.Repository, headRef, baseRef, expectedHeadOID, expectedBaseOID, message string) (string, error) {
	if pr == nil || baseGitRepo == nil {
		return refuseExactMerge(ExactMergeRefusedPRMismatch)
	}
	if !isExactMergeBranchRef(headRef) || !isExactMergeBranchRef(baseRef) {
		return refuseExactMerge(ExactMergeRefusedMalformedRef)
	}
	// Reload the pull request so every policy below reads the current row,
	// never a caller-cached copy.
	fresh, err := issues_model.GetPullRequestByID(ctx, pr.ID)
	if err != nil {
		return refuseExactMerge(ExactMergeRefusedPRMismatch)
	}
	if err := fresh.LoadBaseRepo(ctx); err != nil {
		return refuseExactMerge(ExactMergeRefusedPRMismatch)
	}
	objectFormat := git.ObjectFormatFromName(fresh.BaseRepo.ObjectFormatName)
	if objectFormat == nil || !objectFormat.IsValid(expectedHeadOID) || !objectFormat.IsValid(expectedBaseOID) {
		return refuseExactMerge(ExactMergeRefusedMalformedOID)
	}
	if strings.EqualFold(expectedHeadOID, expectedBaseOID) {
		return refuseExactMerge(ExactMergeRefusedNoOp)
	}
	if fresh.HeadRepoID != fresh.BaseRepoID {
		return refuseExactMerge(ExactMergeRefusedCrossRepo)
	}
	if fresh.HeadBranch != strings.TrimPrefix(headRef, git.BranchPrefix) ||
		fresh.BaseBranch != strings.TrimPrefix(baseRef, git.BranchPrefix) {
		return refuseExactMerge(ExactMergeRefusedPRMismatch)
	}
	if err := fresh.LoadIssue(ctx); err != nil {
		return refuseExactMerge(ExactMergeRefusedPRMismatch)
	}
	if fresh.HasMerged || fresh.Issue.IsClosed {
		return refuseExactMerge(ExactMergeRefusedClosedOrMerged)
	}
	// Head/base existence and equality are the machine's own live Git reads.
	headTip, err := baseGitRepo.GetRefCommitID(headRef)
	if err != nil {
		if git.IsErrNotExist(err) {
			return refuseExactMerge(ExactMergeRefusedMissingHead)
		}
		return "", err
	}
	if !strings.EqualFold(headTip, expectedHeadOID) {
		return refuseExactMerge(ExactMergeRefusedStaleHead)
	}
	baseTip, err := baseGitRepo.GetRefCommitID(baseRef)
	if err != nil {
		if git.IsErrNotExist(err) {
			return refuseExactMerge(ExactMergeRefusedMissingBase)
		}
		return "", err
	}
	if !strings.EqualFold(baseTip, expectedBaseOID) {
		return refuseExactMerge(ExactMergeRefusedStaleBase)
	}
	// Native permission for this method under current repository policy.
	prUnit, err := fresh.BaseRepo.GetUnit(ctx, unit.TypePullRequests)
	if err != nil {
		return "", err
	}
	if !prUnit.PullRequestsConfig().IsMergeStyleAllowed(repo_model.MergeStyleFastForwardOnly) {
		return refuseExactMerge(ExactMergeRefusedMethodNotAllowed)
	}
	// Current authority, protection, review/status and mergeability stay
	// effective through the shared native check.
	permission, err := access_model.GetUserRepoPermission(ctx, fresh.BaseRepo, doer)
	if err != nil {
		return "", err
	}
	if err := CheckPullMergeable(ctx, doer, &permission, fresh, MergeCheckTypeGeneral, false); err != nil {
		return "", err
	}
	// The exact head SHA is the merge input; the engine re-verifies it while
	// preparing the temporary repository.
	if err := Merge(ctx, fresh, doer, baseGitRepo, repo_model.MergeStyleFastForwardOnly, expectedHeadOID, message, false); err != nil {
		return "", err
	}
	// Bind the expected merge SHA: a fast-forward-only merge realizes exactly
	// the expected head. Anything else is an unattributable result.
	realized, err := baseGitRepo.GetRefCommitID(baseRef)
	if err != nil {
		return refuseExactMerge(ExactMergeRefusedResultMismatch)
	}
	if !strings.EqualFold(realized, expectedHeadOID) {
		return refuseExactMerge(ExactMergeRefusedResultMismatch)
	}
	return realized, nil
}

// isExactMergeBranchRef reports whether ref is a full native branch ref.
func isExactMergeBranchRef(ref string) bool {
	return gitsyntax.BranchRef(ref)
}

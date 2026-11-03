// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/git"
	pull_service "forgejo.org/services/pull"
)

// Paged native thread evidence and actor-bound merge-eligibility reads
// (D06/D08, FW17).
//
// Threads partition native location buckets by immutable original context
// through the shared model grouping, so snapshot roots and resolve
// bindings derive the same native root. Outdated unresolved threads are
// retained with Outdated set; hidden or truncated evidence reports
// Complete=false so the consumer blocks rather than authorizing on partial
// conversations. Eligibility reuses the evaluator behind
// CheckPullMergeable and exposes its first typed blocker; the read
// performs no mutation (the evaluator writes no status: conflict refresh
// lives in GetPullRequestMergeability, never in this path) and grants no
// mutation authority.

func (l *snapshotLoader) threads(ctx context.Context, index, number string) (*sdk.SnapshotThreadPage, error) {
	if !l.permission.CanRead(unit.TypePullRequests) {
		return nil, ErrSnapshotNotFound
	}
	var issue *issues_model.Issue
	var pr *issues_model.PullRequest
	if index != "" {
		loaded, err := l.loadIssue(ctx, index)
		if err != nil {
			return nil, err
		}
		issue = loaded
		if issue.IsPull {
			pull, err := issues_model.GetPullRequestByIssueID(ctx, issue.ID)
			if err != nil {
				if issues_model.IsErrPullRequestNotExist(err) {
					return nil, ErrSnapshotUnavailable
				}
				return nil, ErrSnapshotUnavailable
			}
			pr = pull
		}
	} else {
		prNumber, _ := strconv.ParseInt(number, 10, 64)
		pull, err := issues_model.GetPullRequestByIndex(ctx, l.repository.ID, prNumber)
		if err != nil {
			if issues_model.IsErrPullRequestNotExist(err) {
				return nil, ErrSnapshotNotFound
			}
			return nil, ErrSnapshotUnavailable
		}
		pr = pull
		loaded, err := issues_model.GetIssueByID(ctx, pr.IssueID)
		if err != nil {
			if issues_model.IsErrIssueNotExist(err) {
				return nil, ErrSnapshotNotFound
			}
			return nil, ErrSnapshotUnavailable
		}
		issue = loaded
	}
	page := &sdk.SnapshotThreadPage{
		IssueID: snapshotIDString(issue.ID),
	}
	if pr != nil {
		page.PullNumber = snapshotIDString(pr.Index)
	} else {
		// Code conversations exist only on pull requests; any other
		// issue carries a trivially complete empty page.
		validated, err := issues_model.NewThreadPage(issue.ID, nil, "", 0, true)
		if err != nil {
			return nil, ErrSnapshotUnavailable
		}
		page.Items = nil
		page.Total = validated.Total
		page.Complete = validated.Complete
		return page, nil
	}
	headTip, _, err := l.pullHeadTip(ctx, pr)
	if err != nil {
		return nil, err
	}
	comments, _, err := issues_model.FetchThreadComments(ctx, issue)
	if err != nil {
		return nil, ErrSnapshotUnavailable
	}
	grouped := issues_model.GroupCodeThreads(comments)
	// Paging observes ascending root ID so the decimal cursor stays a
	// total exclusive lower bound across pages at one revision; grouping
	// itself keeps native (created, id) order for root derivation.
	sort.Slice(grouped, func(i, j int) bool { return grouped[i].Root().ID < grouped[j].Root().ID })
	// Truncation guard: the fetch is bounded, so a full fetch against a
	// larger total means grouping observed a prefix. Digests over a
	// prefix would lie, so every thread reports incomplete.
	truncated := false
	if total, err := issues_model.CountComments(ctx, &issues_model.FindCommentsOptions{IssueID: issue.ID, Type: issues_model.CommentTypeCode}); err != nil {
		return nil, ErrSnapshotUnavailable
	} else if int64(len(comments)) >= snapshotFetchCap && total > int64(len(comments)) {
		truncated = true
	}
	after := l.cursorAfterID()
	remaining := 0
	for _, thread := range grouped {
		if thread.Root().ID > after {
			remaining++
		}
	}
	kept := make([]issues_model.CodeThread, 0, l.limit)
	for _, thread := range grouped {
		if thread.Root().ID <= after {
			continue
		}
		if len(kept) >= l.limit {
			break
		}
		kept = append(kept, thread)
	}
	converted := make([]issues_model.ThreadSnapshot, 0, len(kept))
	complete := !truncated
	for _, thread := range kept {
		snapshot, ok := l.threadSnapshot(issue, thread, headTip, truncated)
		if !ok {
			complete = false
			continue
		}
		converted = append(converted, snapshot)
		if !snapshot.Complete || !snapshot.Visible {
			complete = false
		}
	}
	// One extra row past the page determines terminal status without
	// returning it: a full page against remaining threads is
	// nonterminal with NextCursor at the final returned root, otherwise
	// the terminal page carries Complete with an empty cursor. Dropped
	// anchors and hidden threads keep the page incomplete either way.
	terminal := len(kept) == remaining && len(converted) == len(kept)
	nextCursor := ""
	if !terminal {
		if len(converted) == 0 {
			return nil, ErrSnapshotUnavailable
		}
		complete = false
		nextCursor = snapshotIDString(converted[len(converted)-1].RootID)
	}
	validated, err := issues_model.NewThreadPage(issue.ID, converted, nextCursor, remaining, complete && terminal)
	if err != nil {
		return nil, ErrSnapshotUnavailable
	}
	for _, item := range validated.Items {
		page.Items = append(page.Items, l.threadDTO(item))
	}
	page.NextCursor = validated.NextCursor
	page.Total = validated.Total
	page.Complete = validated.Complete
	return page, nil
}

// threadSnapshot converts one grouped conversation. It reports ok=false
// for threads with malformed recorded coordinates/context; the caller marks the page incomplete
// rather than guessing an anchor. Hidden threads redact to the root
// locator with no digest.
func (l *snapshotLoader) threadSnapshot(issue *issues_model.Issue, thread issues_model.CodeThread, headTip string, truncated bool) (issues_model.ThreadSnapshot, bool) {
	root := thread.Root()
	visible := true
	for _, member := range thread {
		if member == nil {
			continue
		}
		if member.Review != nil && member.Review.Type == issues_model.ReviewTypePending && member.Review.ReviewerID != l.user.ID {
			visible = false
			break
		}
	}
	if !visible {
		converted, err := issues_model.NewThreadSnapshot(root, issue.ID, root.ReviewID, "", "", 0, "", "", "", nil, 0, 0, false, false, false, issues_model.HiddenReasonRedacted, true)
		if err != nil {
			return issues_model.ThreadSnapshot{}, false
		}
		return converted, true
	}
	side, unsigned, ok := threadRecordedLocation(thread)
	if !ok {
		return issues_model.ThreadSnapshot{}, false
	}
	location := sdk.RenderThreadLocation(root.TreePath, side, unsigned)
	lines := make([]string, 0, len(thread))
	members := make([]issues_model.ThreadMemberSnapshot, 0, len(thread))
	for _, member := range thread {
		if member == nil {
			continue
		}
		converted, err := issues_model.NewThreadMemberSnapshot(member, true, "", true)
		if err != nil {
			return issues_model.ThreadSnapshot{}, false
		}
		members = append(members, converted)
		lines = append(lines, sdk.ResolveConversationLine(sdk.ResolveConversationMember{
			OriginalAnchor: threadOriginalAnchor(member.OriginalAnchor),
			ID:             member.ID,
			Body:           member.Content,
			ContentVer:     int64(member.ContentVersion),
			Location:       location,
			ResolverState:  sdk.ThreadResolverState(member.ResolveDoerID),
		}))
	}
	digest, err := sdk.ResolveConversationDigest(lines)
	if err != nil {
		return issues_model.ThreadSnapshot{}, false
	}
	outdated := root.Invalidated || headTip == ""
	if !outdated {
		if root.OriginalAnchor != nil {
			// An old-side file records the base commit in CommitSHA. That
			// does not make its original diff outdated at the same head.
			outdated = !strings.EqualFold(root.OriginalAnchor.Head, headTip)
		} else if root.CommitSHA != "" {
			outdated = !strings.EqualFold(root.CommitSHA, headTip)
		}
	}
	complete := !truncated
	converted, err := issues_model.NewThreadSnapshot(root, issue.ID, root.ReviewID, root.TreePath, side, unsigned,
		strings.ToLower(root.CommitSHA), strings.ToLower(headTip), digest, members, len(members),
		root.ResolveDoerID, root.ResolveDoerID != 0, outdated, true, "", complete)
	if err != nil {
		return issues_model.ThreadSnapshot{}, false
	}
	return converted, true
}

func (l *snapshotLoader) threadDTO(item issues_model.ThreadSnapshot) sdk.SnapshotThread {
	out := sdk.SnapshotThread{
		OriginalAnchor: threadOriginalAnchor(item.OriginalAnchor),
		RootID:         snapshotIDString(item.RootID),
		IssueID:        snapshotIDString(item.IssueID),
		ReviewID:       snapshotIDString(item.ReviewID),
		Path:           item.Path,
		Side:           item.Side,
		Line:           item.Line,
		OriginalCommit: strings.ToLower(item.OriginalCommit),
		HeadCommit:     strings.ToLower(item.HeadCommit),
		Outdated:       item.Outdated,
		ResolverID:     snapshotIDString(item.ResolverID),
		Resolved:       item.Resolved,
		Digest:         item.Digest,
		MemberTotal:    item.MemberTotal,
		Visible:        item.Visible,
		HiddenReason:   item.HiddenReason,
		Complete:       item.Complete,
	}
	for _, member := range item.Members {
		out.Members = append(out.Members, sdk.SnapshotThreadMember{
			OriginalAnchor: threadOriginalAnchor(member.OriginalAnchor),
			Resolved:       member.Resolved,
			ID:             snapshotIDString(member.CommentID),
			IssueID:        snapshotIDString(member.IssueID),
			ContentDigest:  member.ContentDigest,
			ContentVersion: member.ContentVersion,
			CreatedUnix:    member.CreatedUnix,
			UpdatedUnix:    member.UpdatedUnix,
			Visible:        member.Visible,
			HiddenReason:   member.HiddenReason,
			Complete:       member.Complete,
		})
	}
	return out
}

// eligibility reads the actor-bound merge readiness of one pull request.
// The verdict reuses the evaluator behind CheckPullMergeable with the
// loader's actor and permission; only typed refusals attribute a blocker,
// anything else fails the read as unavailable rather than guessing.
func (l *snapshotLoader) eligibility(ctx context.Context, number string) (*sdk.SnapshotMergeEligibility, error) {
	prNumber, _ := strconv.ParseInt(number, 10, 64)
	pr, err := issues_model.GetPullRequestByIndex(ctx, l.repository.ID, prNumber)
	if err != nil {
		if issues_model.IsErrPullRequestNotExist(err) {
			return nil, ErrSnapshotNotFound
		}
		return nil, ErrSnapshotUnavailable
	}
	if !l.permission.CanRead(unit.TypePullRequests) {
		return nil, ErrSnapshotNotFound
	}
	headTip, _, err := l.pullHeadTip(ctx, pr)
	if err != nil {
		return nil, err
	}
	baseTip, err := l.baseTip(ctx, pr)
	if err != nil {
		return nil, err
	}
	permission := l.permission
	evalErr := pull_service.CheckPullMergeable(ctx, l.user, &permission, pr, pull_service.MergeCheckTypeGeneral, false)
	if evalErr == nil {
		converted, err := issues_model.NewMergeEligibilitySnapshot(pr, strings.ToLower(headTip), strings.ToLower(baseTip), true, "", nil, true, "", true)
		if err != nil {
			return nil, ErrSnapshotUnavailable
		}
		return l.eligibilityDTO(converted), nil
	}
	blocker, reviews, ok := l.attributeBlocker(ctx, pr, evalErr)
	if !ok {
		return nil, ErrSnapshotUnavailable
	}
	converted, err := issues_model.NewMergeEligibilitySnapshot(pr, strings.ToLower(headTip), strings.ToLower(baseTip), false, blocker, reviews, true, "", true)
	if err != nil {
		return nil, ErrSnapshotUnavailable
	}
	return l.eligibilityDTO(converted), nil
}

// baseTip resolves the current base-branch tip from live ref state. A
// gone base reports empty; only repository access failures are
// unavailable.
func (l *snapshotLoader) baseTip(ctx context.Context, pr *issues_model.PullRequest) (string, error) {
	baseRepo, err := repo_model.GetRepositoryByID(ctx, pr.BaseRepoID)
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return "", nil
		}
		return "", ErrSnapshotUnavailable
	}
	tip, err := l.service.readRef(ctx, baseRepo.RepoPath(), git.BranchPrefix+pr.BaseBranch)
	if err != nil {
		if git.IsErrNotExist(err) {
			return "", nil
		}
		return "", ErrSnapshotUnavailable
	}
	return tip, nil
}

// attributeBlocker maps the evaluator's first typed refusal to its bounded
// blocker in evaluator order without parsing human messages. Protection
// refusals attribute through the same predicates the evaluator runs, in
// the same order. It reports ok=false for untyped failures, which fail
// the read as unavailable.
func (l *snapshotLoader) attributeBlocker(ctx context.Context, pr *issues_model.PullRequest, evalErr error) (string, []int64, bool) {
	switch {
	case errors.Is(evalErr, pull_service.ErrHasMerged):
		return issues_model.MergeBlockedMerged, nil, true
	case errors.Is(evalErr, pull_service.ErrIsClosed):
		return issues_model.MergeBlockedClosed, nil, true
	case errors.Is(evalErr, pull_service.ErrUserNotAllowedToMerge):
		return issues_model.MergeBlockedPermission, nil, true
	case errors.Is(evalErr, pull_service.ErrIsWorkInProgress):
		return issues_model.MergeBlockedWorkInProgress, nil, true
	case errors.Is(evalErr, pull_service.ErrNotMergeableState):
		return issues_model.MergeBlockedNotMergeable, nil, true
	case errors.Is(evalErr, pull_service.ErrIsChecking):
		return issues_model.MergeBlockedChecking, nil, true
	case errors.Is(evalErr, pull_service.ErrDependenciesLeft):
		return issues_model.MergeBlockedDependency, nil, true
	case models.IsErrDisallowedToMerge(evalErr):
		return l.attributeProtection(ctx, pr)
	default:
		return "", nil, false
	}
}

// attributeProtection replays the branch-protection predicates the
// evaluator runs, in evaluator order, to name the first blocking rule.
// Signature requirements carry no typed evaluator signal (a failed sign
// reports a raw error), so signature-step failures stay unavailable.
func (l *snapshotLoader) attributeProtection(ctx context.Context, pr *issues_model.PullRequest) (string, []int64, bool) {
	pb, err := git_model.GetFirstMatchProtectedBranchRule(ctx, pr.BaseRepoID, pr.BaseBranch)
	if err != nil || pb == nil {
		return issues_model.MergeBlockedBranchProtection, nil, true
	}
	pass, err := pull_service.IsPullCommitStatusPass(ctx, pr)
	if err != nil {
		return "", nil, false
	}
	if !pass {
		return issues_model.MergeBlockedStatusChecks, nil, true
	}
	if !issues_model.HasEnoughApprovals(ctx, pb, pr) {
		return issues_model.MergeBlockedApprovals, nil, true
	}
	if issues_model.MergeBlockedByRejectedReview(ctx, pb, pr) {
		ids, ok := l.rejectingReviewIDs(ctx, pr)
		if !ok {
			return "", nil, false
		}
		return issues_model.MergeBlockedRequestedChanges, ids, true
	}
	if issues_model.MergeBlockedByOfficialReviewRequests(ctx, pb, pr) {
		return issues_model.MergeBlockedReviewRequests, nil, true
	}
	if issues_model.MergeBlockedByOutdatedBranch(pb, pr) {
		return issues_model.MergeBlockedOutdatedBranch, nil, true
	}
	if pb.MergeBlockedByProtectedFiles(pr.ChangedProtectedFiles) {
		return issues_model.MergeBlockedProtectedFiles, nil, true
	}
	return issues_model.MergeBlockedBranchProtection, nil, true
}

// rejectingReviewIDs lists the official non-dismissed rejecting review IDs
// behind a requested-changes blocker, bounded to the snapshot page.
func (l *snapshotLoader) rejectingReviewIDs(ctx context.Context, pr *issues_model.PullRequest) ([]int64, bool) {
	var reviews []*issues_model.Review
	if err := db.GetEngine(ctx).Where("issue_id = ?", pr.IssueID).
		And("type = ?", issues_model.ReviewTypeReject).
		And("official = ?", true).
		And("dismissed = ?", false).
		Asc("id").Limit(sdk.SnapshotPageLimit).
		Find(&reviews); err != nil {
		return nil, false
	}
	ids := make([]int64, 0, len(reviews))
	for _, review := range reviews {
		ids = append(ids, review.ID)
	}
	return ids, true
}

func (l *snapshotLoader) eligibilityDTO(item issues_model.MergeEligibilitySnapshot) *sdk.SnapshotMergeEligibility {
	out := &sdk.SnapshotMergeEligibility{
		PullNumber: snapshotIDString(item.Index),
		HeadTip:    strings.ToLower(item.HeadTip),
		BaseTip:    strings.ToLower(item.BaseTip),
		Eligible:   item.Eligible,
		Blocker:    item.Blocker,
		Visible:    item.Visible,
		Complete:   item.Complete,
	}
	for _, id := range item.BlockingReviewIDs {
		out.BlockingReviewIDs = append(out.BlockingReviewIDs, snapshotIDString(id))
	}
	return out
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"context"
	"strings"
	"unicode/utf8"

	"forgejo.org/models/db"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
)

const MaxPreparedHumanReviewBodyBytes = 64 << 10

// PreparedHumanReviewTarget pins native SQL facts and the exact candidate.
// Current*OID is trusted Git evidence read by the caller under its held native
// ownership, never browser input. The caller owns live request/session admission,
// native revision claim and the operation receipt in its enclosing transaction.
// This primary performs no Git I/O, notification or operation replay.
type PreparedHumanReviewTarget struct {
	RepositoryID, ReviewerID, PullID, IssueID, Index    int64
	BaseBranch                                          string
	IsClosed, HasMerged, IsAuthor                       bool
	HeadOID, BaseOID, MergeBaseOID                      string
	CurrentHeadOID, CurrentBaseOID, CurrentMergeBaseOID string
	PendingReviewID                                     int64
	PendingCommitID                                     string
}

// SubmitPreparedHumanReview joins the caller's transaction. Drafts create/update
// only the current human's pending row; final events use the ordinary native
// SubmitReview primary, retaining its approval/request/team/code-comment rules.
// Review/history and the caller's receipt can therefore commit or roll back
// together. New inline comment creation remains its own producer; existing own
// draft comments are submitted with their original immutable context.
func SubmitPreparedHumanReview(ctx context.Context, reviewType ReviewType, content string, want PreparedHumanReviewTarget) (*Review, *Comment, error) {
	if err := validatePreparedHumanReviewTarget(ctx, want); err != nil {
		return nil, nil, err
	}
	refuse := func(reason string) (*Review, *Comment, error) { return nil, nil, ErrExactReviewRefused{Reason: reason} }
	if len(content) > MaxPreparedHumanReviewBodyBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return refuse("invalid human review body")
	}
	switch reviewType {
	case ReviewTypeReject:
		if strings.TrimSpace(content) == "" {
			return nil, nil, ContentEmptyErr{}
		}
	case ReviewTypePending, ReviewTypeApprove, ReviewTypeComment:
	default:
		return refuse("invalid human review event")
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer committer.Close()
	actor, issue, pending, err := loadPreparedHumanReview(ctx, want)
	if err != nil {
		return nil, nil, err
	}
	if want.IsAuthor && (reviewType == ReviewTypeApprove || reviewType == ReviewTypeReject) {
		return refuse("cannot review your own pull request")
	}
	var review *Review
	var comment *Comment
	if reviewType == ReviewTypePending {
		if pending == nil {
			if strings.TrimSpace(content) == "" {
				return nil, nil, ContentEmptyErr{}
			}
			review, err = CreateReview(ctx, CreateReviewOptions{Type: ReviewTypePending, Issue: issue, Reviewer: actor, Content: content, CommitID: want.HeadOID})
		} else {
			// Body-only draft editing preserves original commit and inline context.
			pending.Content = content
			_, err = db.GetEngine(ctx).ID(pending.ID).Cols("content").Update(pending)
			review = pending
		}
	} else {
		review, comment, err = SubmitReview(ctx, actor, issue, reviewType, content, want.HeadOID, false, nil)
	}
	if err != nil {
		return nil, nil, err
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if err := committer.Commit(); err != nil {
		return nil, nil, err
	}
	return review, comment, nil
}

// Shared preparation checks used by human review and inline SQL primaries.
func validatePreparedHumanReviewTarget(ctx context.Context, want PreparedHumanReviewTarget) error {
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return err
	}
	execution := execcontext.FromContext(ctx)
	if execution == nil || held.Owner == "" || execution.Owner != held.Owner || execution.Generation != held.Generation {
		return nativeoperation_model.ErrBusy
	}
	refuse := func(reason string) error { return ErrExactReviewRefused{Reason: reason} }
	if want.RepositoryID <= 0 || want.ReviewerID <= 0 || want.PullID <= 0 || want.IssueID <= 0 || want.Index <= 0 || want.BaseBranch == "" || want.PendingReviewID < 0 || want.PendingReviewID == 0 && want.PendingCommitID != "" {
		return refuse("invalid human review target")
	}
	for _, oid := range []string{want.HeadOID, want.BaseOID, want.MergeBaseOID, want.CurrentHeadOID, want.CurrentBaseOID, want.CurrentMergeBaseOID} {
		if !validFullOID(oid) || oid != strings.ToLower(oid) {
			return refuse("invalid object id")
		}
	}
	if want.HeadOID != want.CurrentHeadOID {
		return refuse("head changed")
	}
	if want.BaseOID != want.CurrentBaseOID {
		return refuse("base changed")
	}
	if want.MergeBaseOID != want.CurrentMergeBaseOID {
		return refuse("merge base changed")
	}
	return nil
}

// Caller already owns a joined transaction and exact native ownership.
func loadPreparedHumanReview(ctx context.Context, want PreparedHumanReviewTarget) (*user_model.User, *Issue, *Review, error) {
	refuse := func(reason string) (*user_model.User, *Issue, *Review, error) {
		return nil, nil, nil, ErrExactReviewRefused{Reason: reason}
	}
	actor, err := user_model.GetUserByID(ctx, want.ReviewerID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !actor.IsAccessAllowed(ctx) {
		return refuse("reviewer is inactive")
	}
	issue, err := GetIssueByID(ctx, want.IssueID)
	if err != nil {
		return nil, nil, nil, err
	}
	if issue.RepoID != want.RepositoryID || issue.Index != want.Index || !issue.IsPull || issue.IsClosed != want.IsClosed || issue.IsPoster(actor.ID) != want.IsAuthor {
		return refuse("human review parent changed")
	}
	if err := issue.LoadPullRequest(ctx); err != nil {
		return nil, nil, nil, err
	}
	pr := issue.PullRequest
	if pr == nil || pr.ID != want.PullID || pr.IssueID != issue.ID || pr.BaseRepoID != want.RepositoryID || pr.Index != want.Index || pr.BaseBranch != want.BaseBranch || pr.HasMerged != want.HasMerged {
		return refuse("human review pull changed")
	}
	if err := issue.LoadRepo(ctx); err != nil {
		return nil, nil, nil, err
	}
	repo := issue.Repo
	if err := repo.LoadOwner(ctx); err != nil {
		return nil, nil, nil, err
	}
	if repo.IsArchived || !repo.AllowsPulls(ctx) {
		return refuse("review workflow unavailable")
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, actor)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
		if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
			return refuse("reviewer cannot read this pull request")
		}
	}
	if user_model.IsBlockedMultiple(ctx, []int64{repo.OwnerID, issue.PosterID}, actor.ID) {
		return refuse("reviewer is blocked")
	}
	pending, err := GetCurrentReview(ctx, actor, issue)
	if err != nil && !IsErrReviewNotExist(err) {
		return nil, nil, nil, err
	}
	if pending == nil && want.PendingReviewID != 0 || pending != nil && (pending.ID != want.PendingReviewID || pending.CommitID != want.PendingCommitID) {
		return refuse("pending draft changed")
	}
	if pending != nil {
		if pending.ReviewerID != actor.ID || pending.ReviewerTeamID != 0 || pending.OriginalAuthorID != 0 || pending.Official || pending.Dismissed {
			return refuse("pending draft identity differs")
		}
		if err := pending.LoadCodeComments(ctx); err != nil {
			return nil, nil, nil, err
		}
		for _, comment := range pending.CodeComments.SortedList() {
			if comment.IssueID != issue.ID || comment.ReviewID != pending.ID || comment.PosterID != actor.ID || comment.OriginalAuthorID != 0 {
				return refuse("pending draft comment identity differs")
			}
		}
	}
	return actor, issue, pending, nil
}

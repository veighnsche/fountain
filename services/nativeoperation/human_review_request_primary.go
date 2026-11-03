// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"reflect"
	"strconv"
	"unicode/utf8"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
)

// PreparedHumanReviewRequestResult is primary SQL evidence, not an operation
// receipt. History IDs identify native request-change comments. Review digests
// also bind the selected subject's historical/pending records and official flags;
// their prose remains private to the native host.
type PreparedHumanReviewRequestResult struct {
	Before, After                           sdk.HumanReviewRequestEvidence
	Changed                                 bool
	HistoryIDs                              []string
	ReviewsBeforeDigest, ReviewsAfterDigest string
}

// ApplyPreparedHumanReviewRequest joins the caller's transaction under the exact
// held native revision. The trusted actor and admission callback come from the
// current human boundary, never request JSON. Desired-state no-ops preserve all
// rows; removal consumes the complete selected request set. Native model writers
// retain their official-review and history semantics. This primary sends no
// notifications, claims no owner and activates no journal/transport.
func ApplyPreparedHumanReviewRequest(ctx context.Context, actorID int64, want sdk.RepositoryReviewRequestSnapshot, requested bool, admit HumanReviewAdmission) (*PreparedHumanReviewRequestResult, error) {
	p := want.Review
	refuse := func(reason string) (*PreparedHumanReviewRequestResult, error) {
		return nil, issues_model.ErrExactReviewRefused{Reason: reason}
	}
	q := sdk.RepositoryReviewRequestSnapshotRequest{Index: p.Index, ReviewerKind: want.ReviewerKind, ReviewerID: want.ReviewerID}
	if actorID <= 0 || actorID != nativeCommentID(p.ActorID) || admit == nil || !sdk.ValidRepositoryReviewRequestSnapshot(&want, p.RepositoryID, p.ActorID, q) {
		return refuse("invalid human reviewer target")
	}
	action := want.RemoveAction
	if requested {
		action = want.AddAction
	}
	if action.State != "allowed" {
		return refuse("reviewer action was not admitted")
	}
	if err := requireHumanPrimaryOwner(ctx, p.NativeRevision); err != nil {
		return nil, err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, err
	}
	defer committer.Close()
	if err := admitHumanPrimary(ctx, admit); err != nil {
		return nil, err
	}
	actor, issue, selected, err := loadHumanReviewRequestPrimary(ctx, actorID, want, requested)
	if err != nil {
		return nil, err
	}
	beforeRows, err := humanReviewRequestHistory(ctx, issue.ID, want.ReviewerKind, nativeCommentID(want.ReviewerID))
	if err != nil {
		return nil, err
	}
	if err := admitHumanPrimary(ctx, admit); err != nil {
		return nil, err
	}
	actor, issue, selected, err = loadHumanReviewRequestPrimary(ctx, actorID, want, requested)
	if err != nil {
		return nil, err
	}
	freshRows, err := humanReviewRequestHistory(ctx, issue.ID, want.ReviewerKind, nativeCommentID(want.ReviewerID))
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(beforeRows, freshRows) {
		return refuse("reviewer history changed")
	}
	if err := requireHumanPrimaryOwner(ctx, p.NativeRevision); err != nil {
		return nil, err
	}
	result := &PreparedHumanReviewRequestResult{Before: selected.Requests, HistoryIDs: []string{}}
	result.Changed = requested != (len(result.Before.RequestIDs) > 0)
	var official bool
	if selected.User != nil {
		official, err = issues_model.IsOfficialReviewer(ctx, issue, selected.User)
	} else {
		official, err = issues_model.IsOfficialReviewerTeam(ctx, issue, selected.Team)
	}
	if err != nil {
		return nil, err
	}
	var created *issues_model.Review
	if result.Changed {
		steps := 1
		if !requested {
			steps = len(result.Before.RequestIDs)
		}
		for range steps {
			var comment *issues_model.Comment
			if selected.User != nil {
				if requested {
					comment, err = issues_model.AddReviewRequest(ctx, issue, selected.User, actor)
				} else {
					comment, err = issues_model.RemoveReviewRequest(ctx, issue, selected.User, actor)
				}
			} else if requested {
				comment, err = issues_model.AddTeamReviewRequest(ctx, issue, selected.Team, actor)
			} else {
				comment, err = issues_model.RemoveTeamReviewRequest(ctx, issue, selected.Team, actor)
			}
			if err != nil {
				return nil, err
			}
			if comment == nil || comment.ID <= 0 {
				return refuse("reviewer primary did not record expected history")
			}
			stored, err := issues_model.GetCommentByID(ctx, comment.ID)
			if err != nil {
				return nil, err
			}
			userID, teamID := int64(0), int64(0)
			if selected.User != nil {
				userID = selected.User.ID
			} else {
				teamID = selected.Team.ID
			}
			if stored.Type != issues_model.CommentTypeReviewRequest || stored.IssueID != issue.ID || stored.PosterID != actor.ID || stored.AssigneeID != userID || stored.AssigneeTeamID != teamID || stored.RemovedAssignee == requested || stored.Content != "" || stored.OriginalAuthorID != 0 || stored.OriginalAuthor != "" {
				return refuse("reviewer history differs from native effect")
			}
			if requested {
				created, err = issues_model.GetReviewByID(ctx, stored.ReviewID)
				if err != nil {
					return nil, err
				}
				if created.Type != issues_model.ReviewTypeRequest || created.IssueID != issue.ID || created.ReviewerID != userID || created.ReviewerTeamID != teamID || created.Official != official || created.Content != "" || created.CommitID != "" || created.Stale || created.Dismissed || created.OriginalAuthorID != 0 || created.OriginalAuthor != "" {
					return refuse("created reviewer request differs")
				}
			} else if stored.ReviewID != 0 {
				return refuse("removed reviewer history differs")
			}
			result.HistoryIDs = append(result.HistoryIDs, strconv.FormatInt(stored.ID, 10))
		}
	}
	result.After, err = ReadHumanReviewRequestEvidence(ctx, issue.ID, want.ReviewerKind, nativeCommentID(want.ReviewerID))
	if err != nil {
		return nil, err
	}
	expected := make([]humanReviewRequestRow, 0, len(beforeRows)+1)
	removed := map[int64]bool{}
	if result.Changed && !requested {
		for _, id := range result.Before.RequestIDs {
			removed[nativeCommentID(id)] = true
		}
	}
	for _, row := range beforeRows {
		if removed[row.ID] {
			continue
		}
		if result.Changed && requested && selected.User != nil && official {
			row.Official = false
		}
		expected = append(expected, row)
	}
	if result.Changed && !requested && official {
		// The native remover restores the latest eligible review after removing
		// requests. Team history uses the native negative reviewer convention
		// internally; that convention never crosses the SDK selection boundary.
		id := nativeCommentID(want.ReviewerID)
		if selected.Team != nil {
			id = -id
		}
		for i := len(expected) - 1; i >= 0; i-- {
			r := &expected[i]
			if r.ReviewerID == id && r.OriginalAuthorID == 0 && (r.Type == issues_model.ReviewTypeApprove || r.Type == issues_model.ReviewTypeReject || r.Type == issues_model.ReviewTypeRequest) {
				r.Official = true
				break
			}
		}
	}
	if created != nil {
		expected = append(expected, humanReviewRequestStored(created))
	}
	afterRows, err := humanReviewRequestHistory(ctx, issue.ID, want.ReviewerKind, nativeCommentID(want.ReviewerID))
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(expected, afterRows) || requested != (len(result.After.RequestIDs) > 0) || requested && result.Changed && (len(result.After.RequestIDs) != 1 || result.After.RequestIDs[0] != strconv.FormatInt(created.ID, 10)) || !result.Changed && !reflect.DeepEqual(result.Before, result.After) {
		return refuse("reviewer primary readback differs")
	}
	result.ReviewsBeforeDigest, err = humanReviewRequestDigest(beforeRows)
	if err != nil {
		return nil, err
	}
	result.ReviewsAfterDigest, err = humanReviewRequestDigest(afterRows)
	if err != nil {
		return nil, err
	}
	if err := admitHumanPrimary(ctx, admit); err != nil {
		return nil, err
	}
	if err := requireHumanPrimaryOwner(ctx, p.NativeRevision); err != nil {
		return nil, err
	}
	if err := committer.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func loadHumanReviewRequestPrimary(ctx context.Context, actorID int64, want sdk.RepositoryReviewRequestSnapshot, requested bool) (*user_model.User, *issues_model.Issue, *HumanReviewRequestSelection, error) {
	p := want.Review
	refuse := func(reason string) (*user_model.User, *issues_model.Issue, *HumanReviewRequestSelection, error) {
		return nil, nil, nil, issues_model.ErrExactReviewRefused{Reason: reason}
	}
	actor, err := user_model.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !actor.IsAccessAllowed(ctx) {
		return refuse("reviewer actor is inactive")
	}
	issue, err := issues_model.GetIssueByID(ctx, nativeCommentID(p.IssueID))
	if err != nil {
		return nil, nil, nil, err
	}
	if issue.RepoID != nativeCommentID(p.RepositoryID) || issue.Index != nativeCommentID(p.Index) || !issue.IsPull || issue.IsClosed != p.IsClosed || issue.IsPoster(actorID) != p.IsAuthor {
		return refuse("reviewer parent changed")
	}
	if err := issue.LoadPullRequest(ctx); err != nil {
		return nil, nil, nil, err
	}
	pr := issue.PullRequest
	if pr == nil || pr.ID != nativeCommentID(p.PullID) || pr.BaseRepoID != issue.RepoID || pr.IssueID != issue.ID || pr.Index != issue.Index || pr.BaseBranch != p.BaseBranch || pr.HasMerged != p.HasMerged {
		return refuse("reviewer pull changed")
	}
	if err := issue.LoadRepo(ctx); err != nil {
		return nil, nil, nil, err
	}
	repo := issue.Repo
	if repo.IsArchived || !repo.AllowsPulls(ctx) {
		return refuse("reviewer workflow unavailable")
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, actor)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
		if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
			return refuse("reviewer actor cannot read pull")
		}
	}
	native, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		return nil, nil, nil, err
	}
	defer native.Close()
	head, err := native.GetRefCommitID(pr.GetGitRefName())
	if err != nil || head != p.HeadOID {
		return refuse("reviewer head changed")
	}
	base, err := native.GetRefCommitID(git.BranchPrefix + p.BaseBranch)
	if err != nil || base != p.BaseOID {
		return refuse("reviewer base changed")
	}
	mergeBase, err := native.GetMergeBaseSimple(base, head)
	if err != nil || mergeBase != p.MergeBaseOID {
		return refuse("reviewer merge base changed")
	}
	selected, err := ReadHumanReviewRequestSelection(ctx, actor, issue, want.ReviewerKind, nativeCommentID(want.ReviewerID))
	if err != nil {
		return nil, nil, nil, err
	}
	action := selected.RemoveAction
	if requested {
		action = selected.AddAction
	}
	if action.State != "allowed" || !reflect.DeepEqual(selected.Requests, want.Requests) {
		return refuse("reviewer policy or selected requests changed")
	}
	return actor, issue, selected, nil
}

// Official-request mutations can update historical flags. Bound and prove that
// entire selected history, including the native team historical identity, while
// keeping its prose private. Other subjects' reviews are outside this primary.
func humanReviewRequestHistory(ctx context.Context, issueID int64, kind string, id int64) ([]humanReviewRequestRow, error) {
	query := db.GetEngine(ctx).Where("issue_id=?", issueID)
	if kind == "user" {
		query = query.And("reviewer_id=?", id)
	} else {
		query = query.And("(reviewer_team_id=? OR reviewer_id=?)", id, -id)
	}
	var reviews []*issues_model.Review
	if err := query.Asc("id").Limit(10001).Find(&reviews); err != nil {
		return nil, err
	}
	if len(reviews) > 10000 {
		return nil, ErrHumanReviewRequestTooLarge
	}
	rows, total := make([]humanReviewRequestRow, 0, len(reviews)), 0
	for _, r := range reviews {
		total += len(r.Content) + len(r.OriginalAuthor) + len(r.CommitID)
		if total > sdk.MaxReviewRequestSnapshotBodyBytes {
			return nil, ErrHumanReviewRequestTooLarge
		}
		if !utf8.ValidString(r.Content) || !utf8.ValidString(r.OriginalAuthor) || !utf8.ValidString(r.CommitID) {
			return nil, issues_model.ErrExactReviewRefused{Reason: "invalid persisted reviewer history"}
		}
		rows = append(rows, humanReviewRequestStored(r))
	}
	return rows, nil
}

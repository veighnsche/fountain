// Copyright 2018 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/organization"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/structs"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// ErrReviewNotExist represents a "ReviewNotExist" kind of error.
type ErrReviewNotExist struct {
	ID int64
}

// IsErrReviewNotExist checks if an error is a ErrReviewNotExist.
func IsErrReviewNotExist(err error) bool {
	_, ok := err.(ErrReviewNotExist)
	return ok
}

func (err ErrReviewNotExist) Error() string {
	return fmt.Sprintf("review does not exist [id: %d]", err.ID)
}

func (err ErrReviewNotExist) Unwrap() error {
	return util.ErrNotExist
}

// ErrNotValidReviewRequest an not allowed review request modify
type ErrNotValidReviewRequest struct {
	Reason string
	UserID int64
	RepoID int64
}

// IsErrNotValidReviewRequest checks if an error is a ErrNotValidReviewRequest.
func IsErrNotValidReviewRequest(err error) bool {
	_, ok := err.(ErrNotValidReviewRequest)
	return ok
}

func (err ErrNotValidReviewRequest) Error() string {
	return fmt.Sprintf("%s [user_id: %d, repo_id: %d]",
		err.Reason,
		err.UserID,
		err.RepoID)
}

func (err ErrNotValidReviewRequest) Unwrap() error {
	return util.ErrInvalidArgument
}

// ErrReviewRequestOnClosedPR represents an error when an user tries to request a re-review on a closed or merged PR.
type ErrReviewRequestOnClosedPR struct{}

// IsErrReviewRequestOnClosedPR checks if an error is an ErrReviewRequestOnClosedPR.
func IsErrReviewRequestOnClosedPR(err error) bool {
	_, ok := err.(ErrReviewRequestOnClosedPR)
	return ok
}

func (err ErrReviewRequestOnClosedPR) Error() string {
	return "cannot request a re-review on a closed or merged PR"
}

func (err ErrReviewRequestOnClosedPR) Unwrap() error {
	return util.ErrPermissionDenied
}

// ReviewType defines the sort of feedback a review gives
type ReviewType int

// ReviewTypeUnknown unknown review type
const ReviewTypeUnknown ReviewType = -1

const (
	// ReviewTypePending is a review which is not published yet
	ReviewTypePending ReviewType = iota
	// ReviewTypeApprove approves changes
	ReviewTypeApprove
	// ReviewTypeComment gives general feedback
	ReviewTypeComment
	// ReviewTypeReject gives feedback blocking merge
	ReviewTypeReject
	// ReviewTypeRequest request review from others
	ReviewTypeRequest
)

// Icon returns the corresponding icon for the review type
func (rt ReviewType) Icon() string {
	switch rt {
	case ReviewTypeApprove:
		return "check"
	case ReviewTypeReject:
		return "diff"
	case ReviewTypeComment:
		return "comment"
	case ReviewTypeRequest:
		return "dot-fill"
	default:
		return "comment"
	}
}

// Review represents collection of code comments giving feedback for a PR
type Review struct {
	ID               int64 `xorm:"pk autoincr"`
	Type             ReviewType
	Reviewer         *user_model.User   `xorm:"-"`
	ReviewerID       int64              `xorm:"index"`
	ReviewerTeamID   int64              `xorm:"NOT NULL DEFAULT 0"`
	ReviewerTeam     *organization.Team `xorm:"-"`
	OriginalAuthor   string
	OriginalAuthorID int64
	Issue            *Issue `xorm:"-"`
	IssueID          int64  `xorm:"index"`
	Content          string `xorm:"TEXT"`
	// Official is a review made by an assigned approver (counts towards approval)
	Official  bool   `xorm:"NOT NULL DEFAULT false"`
	CommitID  string `xorm:"VARCHAR(64)"`
	Stale     bool   `xorm:"NOT NULL DEFAULT false"`
	Dismissed bool   `xorm:"NOT NULL DEFAULT false"`

	CreatedUnix timeutil.TimeStamp `xorm:"INDEX created"`
	UpdatedUnix timeutil.TimeStamp `xorm:"INDEX updated"`

	// CodeComments are the initial code comments of the review
	CodeComments CodeComments `xorm:"-"`

	Comments []*Comment `xorm:"-"`
}

func init() {
	db.RegisterModel(new(Review))
}

// LoadCodeComments loads CodeComments
func (r *Review) LoadCodeComments(ctx context.Context) (err error) {
	if r.CodeComments != nil {
		return err
	}
	if err = r.LoadIssue(ctx); err != nil {
		return err
	}
	r.CodeComments, err = fetchCodeCommentsByReview(ctx, r.Issue, nil, r, false)
	return err
}

func (r *Review) LoadIssue(ctx context.Context) (err error) {
	if r.Issue != nil {
		return err
	}
	r.Issue, err = GetIssueByID(ctx, r.IssueID)
	return err
}

// LoadReviewer loads reviewer
func (r *Review) LoadReviewer(ctx context.Context) (err error) {
	if r.ReviewerID == 0 || r.Reviewer != nil {
		return err
	}
	r.Reviewer, err = user_model.GetPossibleUserByID(ctx, r.ReviewerID)
	if err != nil {
		if !user_model.IsErrUserNotExist(err) {
			return fmt.Errorf("GetPossibleUserByID [%d]: %w", r.ReviewerID, err)
		}
		r.ReviewerID = user_model.GhostUserID
		r.Reviewer = user_model.NewGhostUser()
		return nil
	}
	return err
}

// LoadReviewerTeam loads reviewer team
func (r *Review) LoadReviewerTeam(ctx context.Context) (err error) {
	if r.ReviewerTeamID == 0 || r.ReviewerTeam != nil {
		return nil
	}

	r.ReviewerTeam, err = organization.GetTeamByID(ctx, r.ReviewerTeamID)
	return err
}

// LoadAttributes loads all attributes except CodeComments
func (r *Review) LoadAttributes(ctx context.Context) (err error) {
	if err = r.LoadIssue(ctx); err != nil {
		return err
	}
	if err = r.LoadCodeComments(ctx); err != nil {
		return err
	}
	if err = r.LoadReviewer(ctx); err != nil {
		return err
	}
	if err = r.LoadReviewerTeam(ctx); err != nil {
		return err
	}
	return err
}

func (r *Review) HTMLTypeColorName() string {
	switch r.Type {
	case ReviewTypeApprove:
		if r.Stale {
			return "yellow"
		}
		return "green"
	case ReviewTypeComment:
		return "grey"
	case ReviewTypeReject:
		return "red"
	case ReviewTypeRequest:
		return "yellow"
	}
	return "grey"
}

// GetReviewByID returns the review by the given ID
func GetReviewByID(ctx context.Context, id int64) (*Review, error) {
	review := new(Review)
	if has, err := db.GetEngine(ctx).ID(id).Get(review); err != nil {
		return nil, err
	} else if !has {
		return nil, ErrReviewNotExist{ID: id}
	}
	return review, nil
}

// CreateReviewOptions represent the options to create a review. Type, Issue and Reviewer are required.
type CreateReviewOptions struct {
	Content      string
	Type         ReviewType
	Issue        *Issue
	Reviewer     *user_model.User
	ReviewerTeam *organization.Team
	Official     bool
	CommitID     string
	Stale        bool
}

// IsOfficialReviewer check if at least one of the provided reviewers can make official reviews in issue (counts towards required approvals)
func IsOfficialReviewer(ctx context.Context, issue *Issue, reviewer *user_model.User) (bool, error) {
	if err := issue.LoadPullRequest(ctx); err != nil {
		return false, err
	}

	pr := issue.PullRequest
	rule, err := git_model.GetFirstMatchProtectedBranchRule(ctx, pr.BaseRepoID, pr.BaseBranch)
	if err != nil {
		return false, err
	}
	if rule == nil {
		// if no rule is found, then user with write access can make official reviews
		err := pr.LoadBaseRepo(ctx)
		if err != nil {
			return false, err
		}
		writeAccess, err := access_model.HasAccessUnit(ctx, reviewer, pr.BaseRepo, unit.TypeCode, perm.AccessModeWrite)
		if err != nil {
			return false, err
		}
		return writeAccess, nil
	}

	official, err := git_model.IsUserOfficialReviewer(ctx, rule, reviewer)
	if official || err != nil {
		return official, err
	}

	return false, nil
}

// IsOfficialReviewerTeam check if reviewer in this team can make official reviews in issue (counts towards required approvals)
func IsOfficialReviewerTeam(ctx context.Context, issue *Issue, team *organization.Team) (bool, error) {
	if err := issue.LoadPullRequest(ctx); err != nil {
		return false, err
	}
	pb, err := git_model.GetFirstMatchProtectedBranchRule(ctx, issue.PullRequest.BaseRepoID, issue.PullRequest.BaseBranch)
	if err != nil {
		return false, err
	}
	if pb == nil {
		return false, nil
	}

	if !pb.EnableApprovalsWhitelist {
		return team.UnitAccessMode(ctx, unit.TypeCode) >= perm.AccessModeWrite, nil
	}

	return slices.Contains(pb.ApprovalsWhitelistTeamIDs, team.ID), nil
}

// CreateReview creates a new review based on opts
func CreateReview(ctx context.Context, opts CreateReviewOptions) (*Review, error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return nil, err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, err
	}
	defer committer.Close()
	sess := db.GetEngine(ctx)

	review := &Review{
		Issue:        opts.Issue,
		IssueID:      opts.Issue.ID,
		Reviewer:     opts.Reviewer,
		ReviewerTeam: opts.ReviewerTeam,
		Content:      opts.Content,
		Official:     opts.Official,
		CommitID:     opts.CommitID,
		Stale:        opts.Stale,
	}

	if opts.Reviewer != nil {
		review.Type = opts.Type
		review.ReviewerID = opts.Reviewer.ID

		reviewCond := builder.Eq{"reviewer_id": opts.Reviewer.ID, "issue_id": opts.Issue.ID}
		// make sure user review requests are cleared
		if opts.Type != ReviewTypePending {
			if _, err := sess.Where(reviewCond.And(builder.Eq{"type": ReviewTypeRequest})).Delete(new(Review)); err != nil {
				return nil, err
			}
		}
		// make sure if the created review gets dismissed no old review surface
		// other types can be ignored, as they don't affect branch protection
		if opts.Type == ReviewTypeApprove || opts.Type == ReviewTypeReject {
			if _, err := sess.Where(reviewCond.And(builder.In("type", ReviewTypeApprove, ReviewTypeReject))).
				Cols("dismissed").Update(&Review{Dismissed: true}); err != nil {
				return nil, err
			}
		}
	} else if opts.ReviewerTeam != nil {
		review.Type = ReviewTypeRequest
		review.ReviewerTeamID = opts.ReviewerTeam.ID
	} else {
		return nil, errors.New("provide either reviewer or reviewer team")
	}

	if _, err := sess.Insert(review); err != nil {
		return nil, err
	}
	return review, committer.Commit()
}

// GetCurrentReview returns the current pending review of reviewer for given issue
func GetCurrentReview(ctx context.Context, reviewer *user_model.User, issue *Issue) (*Review, error) {
	if reviewer == nil {
		return nil, nil
	}
	reviews, err := FindReviews(ctx, FindReviewOptions{
		Types:      []ReviewType{ReviewTypePending},
		IssueID:    issue.ID,
		ReviewerID: reviewer.ID,
	})
	if err != nil {
		return nil, err
	}
	if len(reviews) == 0 {
		return nil, ErrReviewNotExist{}
	}
	reviews[0].Reviewer = reviewer
	reviews[0].Issue = issue
	return reviews[0], nil
}

// ReviewExists returns whether a review exists for a particular line of code in the PR
func ReviewExists(ctx context.Context, issue *Issue, treePath string, line int64) (bool, error) {
	return db.GetEngine(ctx).Cols("id").Exist(&Comment{IssueID: issue.ID, TreePath: treePath, Line: line, Type: CommentTypeCode})
}

// ContentEmptyErr represents an content empty error
type ContentEmptyErr struct{}

func (ContentEmptyErr) Error() string {
	return "Review content is empty"
}

// IsContentEmptyErr returns true if err is a ContentEmptyErr
func IsContentEmptyErr(err error) bool {
	_, ok := err.(ContentEmptyErr)
	return ok
}

// ErrExactReviewRefused reports a deterministic refusal of an exact-candidate
// review submission. Reason is a stable short string the conditional-review
// operation maps to its refusal reasons; callers must not retry with the same
// target while the reason still holds.
type ErrExactReviewRefused struct {
	Reason string
}

// IsErrExactReviewRefused checks if an error is a ErrExactReviewRefused.
func IsErrExactReviewRefused(err error) bool {
	_, ok := err.(ErrExactReviewRefused)
	return ok
}

func (err ErrExactReviewRefused) Error() string {
	return "exact review refused: " + err.Reason
}

func (err ErrExactReviewRefused) Unwrap() error {
	return util.ErrInvalidArgument
}

// ExactReviewTarget binds one final body-only review to its exact candidate.
// Branch names are short native branch names; OIDs are full object IDs in the
// repository's object format. CurrentHeadOID and CurrentBaseOID carry the
// Git-observed current tips; the conditional caller reads them under its
// reservation and this check compares them without touching Git.
type ExactReviewTarget struct {
	PRIndex        int64
	AuthorID       int64
	HeadRepoID     int64
	HeadBranch     string
	BaseBranch     string
	HeadOID        string
	BaseOID        string
	CommitID       string
	CurrentHeadOID string
	CurrentBaseOID string
	DiffBaseOID    string // Actual old-side merge base; distinct from BaseOID's branch tip.
}

// checkExactReviewTarget verifies the bound PR identity, source, target and
// exact candidate against current state. It performs no Git reads and no
// diff-equivalence substitution: a changed head or base refuses even when the
// resulting diff would be identical.
func checkExactReviewTarget(issue *Issue, pr *PullRequest, want ExactReviewTarget) error {
	if issue.IsClosed || pr.HasMerged {
		return ErrExactReviewRefused{Reason: "pull request is closed or merged"}
	}
	if pr.HeadRepoID != pr.BaseRepoID {
		return ErrExactReviewRefused{Reason: "cross-repository pull request is not supported"}
	}
	if want.PRIndex != issue.Index {
		return ErrExactReviewRefused{Reason: "pr index mismatch"}
	}
	if want.AuthorID != issue.PosterID {
		return ErrExactReviewRefused{Reason: "pr author mismatch"}
	}
	if want.HeadRepoID != pr.HeadRepoID {
		return ErrExactReviewRefused{Reason: "head repository mismatch"}
	}
	if want.HeadBranch != pr.HeadBranch {
		return ErrExactReviewRefused{Reason: "head branch mismatch"}
	}
	if want.BaseBranch != pr.BaseBranch {
		return ErrExactReviewRefused{Reason: "base branch mismatch"}
	}
	for _, oid := range []string{want.HeadOID, want.BaseOID, want.CommitID, want.CurrentHeadOID, want.CurrentBaseOID} {
		if !validFullOID(oid) {
			return ErrExactReviewRefused{Reason: "invalid object id"}
		}
	}
	if !strings.EqualFold(want.CommitID, want.HeadOID) {
		return ErrExactReviewRefused{Reason: "commit must equal the expected head"}
	}
	if !strings.EqualFold(want.CurrentHeadOID, want.HeadOID) {
		return ErrExactReviewRefused{Reason: "head changed"}
	}
	if !strings.EqualFold(want.CurrentBaseOID, want.BaseOID) {
		return ErrExactReviewRefused{Reason: "base changed"}
	}
	return nil
}

// Inline comment sides use native PR diff semantics: old is the merge-base
// tree, new is the exact head.
const (
	ExactReviewCommentSideOld = "old"
	ExactReviewCommentSideNew = "new"
)

// MaxExactReviewComments bounds the inline comments in one exact review
// submission, mirroring the factory findings-per-report cap.
const MaxExactReviewComments = 64

// ExactReviewCommentKeyPrefix starts the stored native code-comment
// content so the opaque client key reads back deterministically: the
// first line is "key:<Key>", the remainder is the prose body. Keys carry
// no factory severity, disposition or classifier state.
const ExactReviewCommentKeyPrefix = "key:"

// ExactReviewComment is one keyed inline finding anchored to the
// candidate diff. Patch is the host-cut hunk excerpt for native
// rendering, empty when the host could not cut one; anchor validity was
// already proven against the live candidate diff before insert.
type ExactReviewComment struct {
	Key   string
	Path  string
	Side  string
	Line  int64
	Body  string
	Patch string
}

// validExactReviewCommentKey reports whether key is a usable opaque
// client correlation key: short, single-line and unambiguous as the
// first line of the stored native comment.
func validExactReviewCommentKey(key string) bool {
	if key == "" || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c <= ' ' || c == 0x7f {
			return false
		}
	}
	return true
}

// validExactReviewCommentPath reports whether path is a usable
// repo-relative diff path.
func validExactReviewCommentPath(path string) bool {
	if path == "" || len(path) > 1024 || path != strings.TrimSpace(path) {
		return false
	}
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\x00") {
		return false
	}
	if slices.Contains(strings.Split(path, "/"), "..") {
		return false
	}
	return true
}

// ValidateExactReviewComments checks the structural inline comment rules:
// bounded count, usable keys/paths, old|new sides, one-based lines,
// non-empty bodies, unique keys (receipt correlation) and no duplicate
// (path, side, line) anchors in one report. Diff membership
// (added/deleted/context/rename lines with binary refusal) is proven by
// the conditional caller against the live candidate diff before insert.
func ValidateExactReviewComments(comments []ExactReviewComment) error {
	if len(comments) > MaxExactReviewComments {
		return ErrExactReviewRefused{Reason: "too many inline comments"}
	}
	keys := make(map[string]struct{}, len(comments))
	anchors := make(map[ExactReviewComment]struct{}, len(comments))
	for _, comment := range comments {
		if !validExactReviewCommentKey(comment.Key) {
			return ErrExactReviewRefused{Reason: "invalid inline comment"}
		}
		if !validExactReviewCommentPath(comment.Path) {
			return ErrExactReviewRefused{Reason: "invalid inline comment"}
		}
		switch comment.Side {
		case ExactReviewCommentSideOld, ExactReviewCommentSideNew:
		default:
			return ErrExactReviewRefused{Reason: "invalid inline comment"}
		}
		if comment.Line < 1 {
			return ErrExactReviewRefused{Reason: "invalid inline comment"}
		}
		if strings.TrimSpace(comment.Body) == "" {
			return ErrExactReviewRefused{Reason: "invalid inline comment"}
		}
		if _, dup := keys[comment.Key]; dup {
			return ErrExactReviewRefused{Reason: "duplicate inline comment key"}
		}
		keys[comment.Key] = struct{}{}
		anchor := ExactReviewComment{Path: comment.Path, Side: comment.Side, Line: comment.Line}
		if _, dup := anchors[anchor]; dup {
			return ErrExactReviewRefused{Reason: "duplicate inline anchor"}
		}
		anchors[anchor] = struct{}{}
	}
	return nil
}

// exactReviewCommentContent renders the stored native content: the key
// line plus the prose body.
func exactReviewCommentContent(comment ExactReviewComment) string {
	return ExactReviewCommentKeyPrefix + comment.Key + "\n" + comment.Body
}

// exactReviewCommentSignedLine maps the diff side to the native signed
// line convention: negative lines are the previous (old) side, positive
// lines the proposed (new) side.
func exactReviewCommentSignedLine(comment ExactReviewComment) int64 {
	if comment.Side == ExactReviewCommentSideOld {
		return -comment.Line
	}
	return comment.Line
}

// SubmitExactReview submits one final body-only comment/approve/reject review bound to
// its exact candidate. Unlike SubmitReview it never adopts an existing pending
// draft: a pending review for this actor refuses and stays untouched. The
// review, its comment and the official-state/request changes commit in one SQL
// transaction; mentions and notifications are bounded completion owned by the
// service caller, not part of this transaction.
func SubmitExactReview(ctx context.Context, doer *user_model.User, issue *Issue, reviewType ReviewType, content string, want ExactReviewTarget) (*Review, *Comment, error) {
	review, comm, _, err := SubmitExactReviewInline(ctx, doer, issue, reviewType, content, want, nil)
	return review, comm, err
}

// SubmitExactReviewInline submits one final comment/approve/reject review bound to
// its exact candidate together with zero or more keyed inline comments.
// The summary review, its review comment and every code comment commit in
// one SQL transaction that joins the caller's transaction: any refusal or
// outer rollback leaves no partial rows. Inline comments must already be
// structurally valid and anchor-proven against the live candidate diff;
// this call re-checks the structural rules and refuses otherwise. Thread
// resolution is a separate conditional operation and never happens here.
func SubmitExactReviewInline(ctx context.Context, doer *user_model.User, issue *Issue, reviewType ReviewType, content string, want ExactReviewTarget, comments []ExactReviewComment) (*Review, *Comment, []*Comment, error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary or conditional owner carries
	// the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return nil, nil, nil, err
	}
	if reviewType != ReviewTypeApprove && reviewType != ReviewTypeReject && reviewType != ReviewTypeComment {
		return nil, nil, nil, ErrExactReviewRefused{Reason: "unsupported final review type"}
	}
	if err := ValidateExactReviewComments(comments); err != nil {
		return nil, nil, nil, err
	}
	if len(comments) > 0 && (!validFullOID(want.DiffBaseOID) || want.DiffBaseOID != strings.ToLower(want.DiffBaseOID)) {
		return nil, nil, nil, ErrExactReviewRefused{Reason: "original diff base unavailable"}
	}
	if err := issue.LoadPullRequest(ctx); err != nil {
		return nil, nil, nil, err
	}
	if err := issue.LoadRepo(ctx); err != nil {
		return nil, nil, nil, err
	}
	pr := issue.PullRequest
	if err := checkExactReviewTarget(issue, pr, want); err != nil {
		return nil, nil, nil, err
	}
	if reviewType != ReviewTypeComment && doer.ID == issue.PosterID && issue.OriginalAuthorID == 0 {
		return nil, nil, nil, ErrExactReviewRefused{Reason: "cannot review your own pull request"}
	}

	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	defer committer.Close()
	sess := db.GetEngine(ctx)

	if pending, err := GetCurrentReview(ctx, doer, issue); err != nil {
		if !IsErrReviewNotExist(err) {
			return nil, nil, nil, err
		}
	} else if pending != nil {
		return nil, nil, nil, ErrExactReviewRefused{Reason: "pending draft exists"}
	}

	if err := issue.Repo.LoadOwner(ctx); err != nil {
		return nil, nil, nil, err
	}
	if user_model.IsBlocked(ctx, issue.Repo.OwnerID, doer.ID) || user_model.IsBlocked(ctx, issue.PosterID, doer.ID) {
		return nil, nil, nil, ErrExactReviewRefused{Reason: "reviewer is blocked"}
	}
	permission, err := access_model.GetUserRepoPermission(ctx, issue.Repo, doer)
	if err != nil {
		return nil, nil, nil, err
	}
	if !permission.CanReadIssuesOrPulls(true) {
		return nil, nil, nil, ErrExactReviewRefused{Reason: "reviewer cannot read this pull request"}
	}

	if reviewType != ReviewTypeApprove && len(strings.TrimSpace(content)) == 0 {
		return nil, nil, nil, ContentEmptyErr{}
	}

	// Commentary carries no vote and cannot supersede an existing human vote.
	official := false
	if reviewType != ReviewTypeComment {
		if _, err := db.Exec(ctx, "UPDATE `review` SET official=? WHERE issue_id=? AND reviewer_id=?", false, issue.ID, doer.ID); err != nil {
			return nil, nil, nil, err
		}
		official, err = IsOfficialReviewer(ctx, issue, doer)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	// No adoption: a new review row always. Votes supersede prior votes;
	// commentary leaves those votes and their official state intact.
	review, err := CreateReview(ctx, CreateReviewOptions{
		Type:     reviewType,
		Issue:    issue,
		Reviewer: doer,
		Content:  content,
		Official: official,
		CommitID: want.CommitID,
		Stale:    false,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	comm, err := CreateComment(ctx, &CreateCommentOptions{
		Type:     CommentTypeReview,
		Doer:     doer,
		Content:  review.Content,
		Issue:    issue,
		Repo:     issue.Repo,
		ReviewID: review.ID,
	})
	if err != nil || comm == nil {
		return nil, nil, nil, err
	}

	// The code comments join the same SQL transaction: the summary review
	// and every inline finding commit atomically, and any refusal or
	// outer rollback leaves no partial rows. Each nested writer joins
	// the caller's transaction instead of committing early.
	inline := make([]*Comment, 0, len(comments))
	for _, comment := range comments {
		code, err := CreateComment(ctx, &CreateCommentOptions{
			Type:           CommentTypeCode,
			Doer:           doer,
			Content:        exactReviewCommentContent(comment),
			Issue:          issue,
			Repo:           issue.Repo,
			ReviewID:       review.ID,
			TreePath:       comment.Path,
			OriginalAnchor: &CodeCommentAnchor{Base: want.DiffBaseOID, Head: strings.ToLower(want.HeadOID), Path: comment.Path, Side: comment.Side, Scope: "lines", StartLine: comment.Line, EndLine: comment.Line},
			LineNum:        exactReviewCommentSignedLine(comment),
			CommitSHA:      want.CommitID,
			Patch:          comment.Patch,
		})
		if err != nil {
			return nil, nil, nil, err
		}
		inline = append(inline, code)
	}

	if err := clearTeamReviewRequests(ctx, sess, issue, doer); err != nil {
		return nil, nil, nil, err
	}

	comm.Review = review
	return review, comm, inline, committer.Commit()
}

// SubmitReview creates a review out of the existing pending review or creates a new one if no pending review exist
func SubmitReview(ctx context.Context, doer *user_model.User, issue *Issue, reviewType ReviewType, content, commitID string, stale bool, attachmentUUIDs []string) (*Review, *Comment, error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return nil, nil, err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer committer.Close()
	sess := db.GetEngine(ctx)

	official := false

	review, err := GetCurrentReview(ctx, doer, issue)
	if err != nil {
		if !IsErrReviewNotExist(err) {
			return nil, nil, err
		}

		if reviewType != ReviewTypeApprove && len(strings.TrimSpace(content)) == 0 {
			return nil, nil, ContentEmptyErr{}
		}

		if reviewType == ReviewTypeApprove || reviewType == ReviewTypeReject {
			// Only reviewers latest review of type approve and reject shall count as "official", so existing reviews needs to be cleared
			if _, err := db.Exec(ctx, "UPDATE `review` SET official=? WHERE issue_id=? AND reviewer_id=?", false, issue.ID, doer.ID); err != nil {
				return nil, nil, err
			}
			if official, err = IsOfficialReviewer(ctx, issue, doer); err != nil {
				return nil, nil, err
			}
		}

		// No current review. Create a new one!
		if review, err = CreateReview(ctx, CreateReviewOptions{
			Type:     reviewType,
			Issue:    issue,
			Reviewer: doer,
			Content:  content,
			Official: official,
			CommitID: commitID,
			Stale:    stale,
		}); err != nil {
			return nil, nil, err
		}
	} else {
		if err := review.LoadCodeComments(ctx); err != nil {
			return nil, nil, err
		}
		if reviewType != ReviewTypeApprove && len(review.CodeComments) == 0 && len(strings.TrimSpace(content)) == 0 {
			return nil, nil, ContentEmptyErr{}
		}

		if reviewType == ReviewTypeApprove || reviewType == ReviewTypeReject {
			// Only reviewers latest review of type approve and reject shall count as "official", so existing reviews needs to be cleared
			if _, err := db.Exec(ctx, "UPDATE `review` SET official=? WHERE issue_id=? AND reviewer_id=?", false, issue.ID, doer.ID); err != nil {
				return nil, nil, err
			}
			if official, err = IsOfficialReviewer(ctx, issue, doer); err != nil {
				return nil, nil, err
			}
			// delete previous review requests from the same user
			reviewCond := builder.Eq{"reviewer_id": doer.ID, "issue_id": issue.ID}
			if _, err := sess.Where(reviewCond.And(builder.Eq{"type": ReviewTypeRequest})).Delete(new(Review)); err != nil {
				return nil, nil, err
			}
		}

		review.Official = official
		review.Issue = issue
		review.Content = content
		review.Type = reviewType
		review.CommitID = commitID
		review.Stale = stale

		if _, err := sess.ID(review.ID).Cols("content, type, official, commit_id, stale").Update(review); err != nil {
			return nil, nil, err
		}
	}

	comm, err := CreateComment(ctx, &CreateCommentOptions{
		Type:        CommentTypeReview,
		Doer:        doer,
		Content:     review.Content,
		Issue:       issue,
		Repo:        issue.Repo,
		ReviewID:    review.ID,
		Attachments: attachmentUUIDs,
	})
	if err != nil || comm == nil {
		return nil, nil, err
	}

	if reviewType == ReviewTypeApprove || reviewType == ReviewTypeReject {
		if err := clearTeamReviewRequests(ctx, sess, issue, doer); err != nil {
			return nil, nil, err
		}
	}

	comm.Review = review
	return review, comm, committer.Commit()
}

// clearTeamReviewRequests removes team review requests the doer satisfies as a
// member. The caller must have loaded issue.Repo.Owner. It runs inside the
// caller's SQL transaction together with the submitted review.
func clearTeamReviewRequests(ctx context.Context, sess db.Engine, issue *Issue, doer *user_model.User) error {
	// try to remove team review request if need
	if !issue.Repo.Owner.IsOrganization() {
		return nil
	}
	teamReviewRequests := make([]*Review, 0, 10)
	if err := sess.SQL("SELECT * FROM review WHERE issue_id = ? AND reviewer_team_id > 0 AND type = ?", issue.ID, ReviewTypeRequest).Find(&teamReviewRequests); err != nil {
		return err
	}

	for _, teamReviewRequest := range teamReviewRequests {
		ok, err := organization.IsTeamMember(ctx, issue.Repo.OwnerID, teamReviewRequest.ReviewerTeamID, doer.ID)
		if err != nil {
			return err
		} else if !ok {
			continue
		}

		if _, err := db.DeleteByID[Review](ctx, teamReviewRequest.ID); err != nil {
			return err
		}
	}
	return nil
}

// GetReviewByIssueIDAndUserID get the latest review of reviewer for a pull request
func GetReviewByIssueIDAndUserID(ctx context.Context, issueID, userID int64) (*Review, error) {
	return GetReviewByIssueIDUserIDAndTypes(ctx, issueID, userID, []ReviewType{ReviewTypeApprove, ReviewTypeReject, ReviewTypeRequest})
}

func GetReviewByIssueIDUserIDAndTypes(ctx context.Context, issueID, userID int64, types []ReviewType) (*Review, error) {
	review := new(Review)

	has, err := db.GetEngine(ctx).Where(
		builder.In("type", types).
			And(builder.Eq{"issue_id": issueID, "reviewer_id": userID, "original_author_id": 0})).
		Desc("id").
		Get(review)
	if err != nil {
		return nil, err
	}

	if !has {
		return nil, ErrReviewNotExist{}
	}

	return review, nil
}

// GetTeamReviewerByIssueIDAndTeamID get the latest review request of reviewer team for a pull request
func GetTeamReviewerByIssueIDAndTeamID(ctx context.Context, issueID, teamID int64) (*Review, error) {
	review := new(Review)

	has, err := db.GetEngine(ctx).Where(builder.Eq{"issue_id": issueID, "reviewer_team_id": teamID}).
		Desc("id").
		Get(review)
	if err != nil {
		return nil, err
	}

	if !has {
		return nil, ErrReviewNotExist{0}
	}

	return review, err
}

// MarkReviewsAsStale marks existing reviews as stale
func MarkReviewsAsStale(ctx context.Context, issueID int64) (err error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return err
	}
	_, err = db.GetEngine(ctx).Exec("UPDATE `review` SET stale=? WHERE issue_id=?", true, issueID)

	return err
}

// MarkReviewsAsNotStale marks existing reviews as not stale for a giving commit SHA
func MarkReviewsAsNotStale(ctx context.Context, issueID int64, commitID string) (err error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return err
	}
	_, err = db.GetEngine(ctx).Exec("UPDATE `review` SET stale=? WHERE issue_id=? AND commit_id=?", false, issueID, commitID)

	return err
}

// DismissReview change the dismiss status of a review
func DismissReview(ctx context.Context, review *Review, isDismiss bool) (err error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return err
	}
	if review.Dismissed == isDismiss || (review.Type != ReviewTypeApprove && review.Type != ReviewTypeReject) {
		return nil
	}

	review.Dismissed = isDismiss

	if review.ID == 0 {
		return ErrReviewNotExist{}
	}

	_, err = db.GetEngine(ctx).ID(review.ID).Cols("dismissed").Update(review)

	return err
}

// InsertReviews inserts review and review comments
func InsertReviews(ctx context.Context, reviews []*Review) error {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return err
	}
	defer committer.Close()
	sess := db.GetEngine(ctx)

	for _, review := range reviews {
		if _, err := sess.NoAutoTime().Insert(review); err != nil {
			return err
		}

		if _, err := sess.NoAutoTime().Insert(&Comment{
			Type:             CommentTypeReview,
			Content:          review.Content,
			PosterID:         review.ReviewerID,
			OriginalAuthor:   review.OriginalAuthor,
			OriginalAuthorID: review.OriginalAuthorID,
			IssueID:          review.IssueID,
			ReviewID:         review.ID,
			CreatedUnix:      review.CreatedUnix,
			UpdatedUnix:      review.UpdatedUnix,
		}); err != nil {
			return err
		}

		for _, c := range review.Comments {
			c.ReviewID = review.ID
		}

		if len(review.Comments) > 0 {
			if _, err := sess.NoAutoTime().Insert(review.Comments); err != nil {
				return err
			}
		}

		if err := UpdateIssueNumComments(ctx, review.IssueID); err != nil {
			return err
		}
	}

	return committer.Commit()
}

// AddReviewRequest add a review request from one reviewer
func AddReviewRequest(ctx context.Context, issue *Issue, reviewer, doer *user_model.User) (*Comment, error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return nil, err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, err
	}
	defer committer.Close()
	sess := db.GetEngine(ctx)

	review, err := GetReviewByIssueIDAndUserID(ctx, issue.ID, reviewer.ID)
	if err != nil && !IsErrReviewNotExist(err) {
		return nil, err
	}

	if review != nil {
		// skip it when reviewer hase been request to review
		if review.Type == ReviewTypeRequest {
			return nil, committer.Commit() // still commit the transaction, or committer.Close() will rollback it, even if it's a reused transaction.
		}

		if issue.IsClosed {
			return nil, ErrReviewRequestOnClosedPR{}
		}

		if issue.IsPull {
			if err := issue.LoadPullRequest(ctx); err != nil {
				return nil, err
			}
			if issue.PullRequest.HasMerged {
				return nil, ErrReviewRequestOnClosedPR{}
			}
		}
	}

	// if the reviewer is an official reviewer,
	// remove the official flag in the all previous reviews
	official, err := IsOfficialReviewer(ctx, issue, reviewer)
	if err != nil {
		return nil, err
	} else if official {
		if _, err := sess.Exec("UPDATE `review` SET official=? WHERE issue_id=? AND reviewer_id=?", false, issue.ID, reviewer.ID); err != nil {
			return nil, err
		}
	}

	review, err = CreateReview(ctx, CreateReviewOptions{
		Type:     ReviewTypeRequest,
		Issue:    issue,
		Reviewer: reviewer,
		Official: official,
		Stale:    false,
	})
	if err != nil {
		return nil, err
	}

	comment, err := CreateComment(ctx, &CreateCommentOptions{
		Type:            CommentTypeReviewRequest,
		Doer:            doer,
		Repo:            issue.Repo,
		Issue:           issue,
		RemovedAssignee: false,       // Use RemovedAssignee as !isRequest
		AssigneeID:      reviewer.ID, // Use AssigneeID as reviewer ID
		ReviewID:        review.ID,
	})
	if err != nil {
		return nil, err
	}

	// func caller use the created comment to retrieve created review too.
	comment.Review = review

	return comment, committer.Commit()
}

// RemoveReviewRequest remove a review request from one reviewer
func RemoveReviewRequest(ctx context.Context, issue *Issue, reviewer, doer *user_model.User) (*Comment, error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return nil, err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, err
	}
	defer committer.Close()

	review, err := GetReviewByIssueIDUserIDAndTypes(ctx, issue.ID, reviewer.ID, []ReviewType{ReviewTypeRequest})
	if err != nil && !IsErrReviewNotExist(err) {
		return nil, err
	}

	if review == nil {
		return nil, nil
	}

	if _, err = db.DeleteByBean(ctx, review); err != nil {
		return nil, err
	}

	official, err := IsOfficialReviewer(ctx, issue, reviewer)
	if err != nil {
		return nil, err
	} else if official {
		if err := restoreLatestOfficialReview(ctx, issue.ID, reviewer.ID); err != nil {
			return nil, err
		}
	}

	comment, err := CreateComment(ctx, &CreateCommentOptions{
		Type:            CommentTypeReviewRequest,
		Doer:            doer,
		Repo:            issue.Repo,
		Issue:           issue,
		RemovedAssignee: true,        // Use RemovedAssignee as !isRequest
		AssigneeID:      reviewer.ID, // Use AssigneeID as reviewer ID
	})
	if err != nil {
		return nil, err
	}

	return comment, committer.Commit()
}

// Recalculate the latest official review for reviewer
func restoreLatestOfficialReview(ctx context.Context, issueID, reviewerID int64) error {
	review, err := GetReviewByIssueIDAndUserID(ctx, issueID, reviewerID)
	if err != nil && !IsErrReviewNotExist(err) {
		return err
	}

	if review != nil {
		if _, err := db.Exec(ctx, "UPDATE `review` SET official=? WHERE id=?", true, review.ID); err != nil {
			return err
		}
	}

	return nil
}

// AddTeamReviewRequest add a review request from one team
func AddTeamReviewRequest(ctx context.Context, issue *Issue, reviewer *organization.Team, doer *user_model.User) (*Comment, error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return nil, err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, err
	}
	defer committer.Close()

	review, err := GetTeamReviewerByIssueIDAndTeamID(ctx, issue.ID, reviewer.ID)
	if err != nil && !IsErrReviewNotExist(err) {
		return nil, err
	}

	// This team already has been requested to review - therefore skip this.
	if review != nil {
		return nil, nil
	}

	official, err := IsOfficialReviewerTeam(ctx, issue, reviewer)
	if err != nil {
		return nil, fmt.Errorf("isOfficialReviewerTeam(): %w", err)
	}

	if review, err = CreateReview(ctx, CreateReviewOptions{
		Type:         ReviewTypeRequest,
		Issue:        issue,
		ReviewerTeam: reviewer,
		Official:     official,
		Stale:        false,
	}); err != nil {
		return nil, err
	}

	comment, err := CreateComment(ctx, &CreateCommentOptions{
		Type:            CommentTypeReviewRequest,
		Doer:            doer,
		Repo:            issue.Repo,
		Issue:           issue,
		RemovedAssignee: false,       // Use RemovedAssignee as !isRequest
		AssigneeTeamID:  reviewer.ID, // Use AssigneeTeamID as reviewer team ID
		ReviewID:        review.ID,
	})
	if err != nil {
		return nil, fmt.Errorf("CreateComment(): %w", err)
	}

	return comment, committer.Commit()
}

// RemoveTeamReviewRequest remove a review request from one team
func RemoveTeamReviewRequest(ctx context.Context, issue *Issue, reviewer *organization.Team, doer *user_model.User) (*Comment, error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return nil, err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, err
	}
	defer committer.Close()

	review, err := GetTeamReviewerByIssueIDAndTeamID(ctx, issue.ID, reviewer.ID)
	if err != nil && !IsErrReviewNotExist(err) {
		return nil, err
	}

	if review == nil {
		return nil, nil
	}

	if _, err = db.DeleteByBean(ctx, review); err != nil {
		return nil, err
	}

	official, err := IsOfficialReviewerTeam(ctx, issue, reviewer)
	if err != nil {
		return nil, fmt.Errorf("isOfficialReviewerTeam(): %w", err)
	}

	if official {
		// recalculate which is the latest official review from that team
		review, err := GetReviewByIssueIDAndUserID(ctx, issue.ID, -reviewer.ID)
		if err != nil && !IsErrReviewNotExist(err) {
			return nil, err
		}

		if review != nil {
			if _, err := db.Exec(ctx, "UPDATE `review` SET official=? WHERE id=?", true, review.ID); err != nil {
				return nil, err
			}
		}
	}

	if doer == nil {
		return nil, committer.Commit()
	}

	comment, err := CreateComment(ctx, &CreateCommentOptions{
		Type:            CommentTypeReviewRequest,
		Doer:            doer,
		Repo:            issue.Repo,
		Issue:           issue,
		RemovedAssignee: true,        // Use RemovedAssignee as !isRequest
		AssigneeTeamID:  reviewer.ID, // Use AssigneeTeamID as reviewer team ID
	})
	if err != nil {
		return nil, fmt.Errorf("CreateComment(): %w", err)
	}

	return comment, committer.Commit()
}

// MarkConversation Add or remove Conversation mark for a code comment
func MarkConversation(ctx context.Context, comment *Comment, doer *user_model.User, isResolve bool) (err error) {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return err
	}
	if comment.Type != CommentTypeCode {
		return nil
	}

	if isResolve {
		if comment.ResolveDoerID != 0 {
			return nil
		}

		if _, err = db.GetEngine(ctx).Exec("UPDATE `comment` SET resolve_doer_id=? WHERE id=?", doer.ID, comment.ID); err != nil {
			return err
		}
	} else {
		if comment.ResolveDoerID == 0 {
			return nil
		}

		if _, err = db.GetEngine(ctx).Exec("UPDATE `comment` SET resolve_doer_id=? WHERE id=?", 0, comment.ID); err != nil {
			return err
		}
	}

	return nil
}

// CanMarkConversation  Add or remove Conversation mark for a code comment permission check
// the PR writer , offfcial reviewer and poster can do it
func CanMarkConversation(ctx context.Context, issue *Issue, doer *user_model.User) (permResult bool, err error) {
	if doer == nil || issue == nil {
		return false, errors.New("issue or doer is nil")
	}

	if doer.ID != issue.PosterID {
		if err = issue.LoadRepo(ctx); err != nil {
			return false, err
		}

		p, err := access_model.GetUserRepoPermission(ctx, issue.Repo, doer)
		if err != nil {
			return false, err
		}

		permResult = p.CanAccess(perm.AccessModeWrite, unit.TypePullRequests)
		if !permResult {
			if permResult, err = IsOfficialReviewer(ctx, issue, doer); err != nil {
				return false, err
			}
		}

		if !permResult {
			return false, nil
		}
	}

	return true, nil
}

// DeleteReview delete a review and it's code comments
func DeleteReview(ctx context.Context, r *Review) error {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return err
	}
	defer committer.Close()

	if r.ID == 0 {
		return errors.New("review is not allowed to be 0")
	}

	if r.Type == ReviewTypeRequest {
		return errors.New("review request can not be deleted using this method")
	}

	opts := FindCommentsOptions{
		Type:     CommentTypeCode,
		IssueID:  r.IssueID,
		ReviewID: r.ID,
	}

	if _, err := db.Delete[Comment](ctx, opts); err != nil {
		return err
	}

	opts = FindCommentsOptions{
		Type:     CommentTypeReview,
		IssueID:  r.IssueID,
		ReviewID: r.ID,
	}

	if _, err := db.Delete[Comment](ctx, opts); err != nil {
		return err
	}

	opts = FindCommentsOptions{
		Type:     CommentTypeDismissReview,
		IssueID:  r.IssueID,
		ReviewID: r.ID,
	}

	if _, err := db.Delete[Comment](ctx, opts); err != nil {
		return err
	}

	if _, err := db.DeleteByID[Review](ctx, r.ID); err != nil {
		return err
	}

	if r.Official {
		if err := restoreLatestOfficialReview(ctx, r.IssueID, r.ReviewerID); err != nil {
			return err
		}
	}

	return committer.Commit()
}

// GetCodeCommentsCount return count of CodeComments a Review has
func (r *Review) GetCodeCommentsCount(ctx context.Context) int {
	opts := FindCommentsOptions{
		Type:     CommentTypeCode,
		IssueID:  r.IssueID,
		ReviewID: r.ID,
	}
	conds := opts.ToConds()
	if r.ID == 0 {
		conds = conds.And(builder.Eq{"invalidated": false})
	}

	count, err := db.GetEngine(ctx).Where(conds).Count(new(Comment))
	if err != nil {
		return 0
	}
	return int(count)
}

// HTMLURL formats a URL-string to the related review issue-comment
func (r *Review) HTMLURL(ctx context.Context) string {
	opts := FindCommentsOptions{
		Type:     CommentTypeReview,
		IssueID:  r.IssueID,
		ReviewID: r.ID,
	}
	comment := new(Comment)
	has, err := db.GetEngine(ctx).Where(opts.ToConds()).Get(comment)
	if err != nil || !has {
		return ""
	}
	return comment.HTMLURL(ctx)
}

// RemapExternalUser ExternalUserRemappable interface
func (r *Review) RemapExternalUser(externalName string, externalID, userID int64) error {
	r.OriginalAuthor = externalName
	r.OriginalAuthorID = externalID
	r.ReviewerID = userID
	return nil
}

// GetUserID ExternalUserRemappable interface
func (r *Review) GetUserID() int64 { return r.ReviewerID }

// GetExternalName ExternalUserRemappable interface
func (r *Review) GetExternalName() string { return r.OriginalAuthor }

// GetExternalID ExternalUserRemappable interface
func (r *Review) GetExternalID() int64 { return r.OriginalAuthorID }

// UpdateReviewsMigrationsByType updates reviews' migrations information via given git service type and original id and poster id
func UpdateReviewsMigrationsByType(ctx context.Context, tp structs.GitServiceType, originalAuthorID string, posterID int64) error {
	// Nested collaboration writer: refuse while another owner holds the
	// reservation; the enclosing ordinary owner carries the execution.
	if err := nativeoperation_model.RequireHeldOwnership(ctx); err != nil {
		return err
	}
	_, err := db.GetEngine(ctx).Table("review").
		Where("original_author_id = ?", originalAuthorID).
		And(migratedIssueCond(tp)).
		Update(map[string]any{
			"reviewer_id":        posterID,
			"original_author":    "",
			"original_author_id": 0,
		})
	return err
}

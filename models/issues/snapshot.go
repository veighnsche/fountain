// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"forgejo.org/extension-sdk/gitsyntax"
)

// Snapshot conversion for authoritative native reads (FT10, early start).
//
// These types convert already-loaded native records into permission-checked
// snapshots carrying exact versions, creation/title/lifecycle evidence, edge
// occurrences, visibility and completeness. They perform no database reads:
// the caller supplies current rows read from live database/ref state (never
// stale replicas or caches) with the actor's visibility decision, and the
// conversion enforces redaction for hidden records.
//
// Hidden snapshots preserve only the structural locator IDs the caller
// already supplied, with Visible=false and a bounded HiddenReason. Text,
// digests, user attribution, versions and lifecycle state are zeroed so an
// inaccessible record leaks nothing. The consumer treats a hidden required
// record as blocking, never as authorization.
//
// Completeness is explicit: single-item snapshots set Complete only when
// every required joined input (history flag, lifecycle events) was read;
// pages set Complete only when all Total items are Returned. A changed/busy
// native revision, missing page or incomplete read cannot authorize work;
// that bracketing is owned by the operation revision observation and the
// Soda caller, not by this conversion.
//
// No factory policy is interpreted here: no acceptance, readiness, budget
// or approval fields. Authoritative guarantees wait for FT09 (complete
// writer domain) plus the FT10 revision-bracket proof; until then these
// snapshots are conversion/DTO work only, not F-read.

// ErrSnapshotInvalid rejects malformed snapshot inputs rather than silently
// broadening or defaulting them.
var ErrSnapshotInvalid = errors.New("invalid native snapshot input")

// Hidden reasons are bounded so callers cannot smuggle native internals.
const (
	HiddenReasonNotFound  = "not_found"
	HiddenReasonNoAccess  = "no_access"
	HiddenReasonRedacted  = "redacted"
	HiddenReasonWithdrawn = "withdrawn"
)

func validHiddenReason(reason string) bool {
	switch reason {
	case HiddenReasonNotFound, HiddenReasonNoAccess, HiddenReasonRedacted, HiddenReasonWithdrawn:
		return true
	default:
		return false
	}
}

// ContentDigest binds exact native text bytes. The digest of empty text is
// the digest of the empty string; hidden text has no digest at all.
func ContentDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Lifecycle event kinds for title/lifecycle evidence.
const (
	LifecycleCreated  = "created"
	LifecycleRetitled = "retitled"
	LifecycleClosed   = "closed"
	LifecycleReopened = "reopened"
	LifecycleEdited   = "edited"
)

func validLifecycleKind(kind string) bool {
	switch kind {
	case LifecycleCreated, LifecycleRetitled, LifecycleClosed, LifecycleReopened, LifecycleEdited:
		return true
	default:
		return false
	}
}

// TitleLifecycleEvent is one title/lifecycle transition with its actor and
// timestamp and immutable native comment identity. OldTitle/NewTitle are set
// only for retitle events.
type TitleLifecycleEvent struct {
	OccurrenceID int64
	Kind         string
	AtUnix       int64
	ActorID      int64
	OldTitle     string
	NewTitle     string
}

func validLifecycleEvent(event TitleLifecycleEvent) bool {
	if event.OccurrenceID <= 0 || !validLifecycleKind(event.Kind) || event.AtUnix <= 0 || event.ActorID < 0 {
		return false
	}
	if event.Kind == LifecycleRetitled {
		return event.OldTitle != event.NewTitle
	}
	return event.OldTitle == "" && event.NewTitle == ""
}

// LifecycleEventFromComment maps title/lifecycle comments to events. It
// reports false for comment types that carry no title/lifecycle transition.
// The caller supplies already-loaded comments; no database access happens.
func LifecycleEventFromComment(comment *Comment) (TitleLifecycleEvent, bool) {
	if comment == nil {
		return TitleLifecycleEvent{}, false
	}
	event := TitleLifecycleEvent{OccurrenceID: comment.ID, AtUnix: int64(comment.CreatedUnix), ActorID: comment.PosterID}
	switch comment.Type {
	case CommentTypeChangeTitle:
		event.Kind = LifecycleRetitled
		event.OldTitle = comment.OldTitle
		event.NewTitle = comment.NewTitle
	case CommentTypeClose:
		event.Kind = LifecycleClosed
	case CommentTypeReopen:
		event.Kind = LifecycleReopened
	default:
		return TitleLifecycleEvent{}, false
	}
	if !validLifecycleEvent(event) {
		return TitleLifecycleEvent{}, false
	}
	return event, true
}

// CreationProvenance binds verified creation evidence. Verified requires
// visibility plus the loader-observed first-created history flag; cached
// text or author names alone never verify provenance.
type CreationProvenance struct {
	PosterID     int64
	CreatedUnix  int64
	FirstCreated bool
	Verified     bool
}

// IssueSnapshot is the permission-checked view of one native issue.
type IssueSnapshot struct {
	RepositoryID   int64
	IssueID        int64
	Index          int64
	Title          string
	Content        string
	TitleDigest    string
	ContentDigest  string
	ContentVersion int
	NumComments    int
	IsClosed       bool
	IsLocked       bool
	IsPull         bool
	Provenance     CreationProvenance
	Lifecycle      []TitleLifecycleEvent
	CreatedUnix    int64
	UpdatedUnix    int64
	ClosedUnix     int64
	Visible        bool
	HiddenReason   string
	Complete       bool
}

// NewIssueSnapshot converts one current issue row. historyFirstCreated is
// the loader-observed ContentHistory first-created flag for the issue body.
// lifecycle holds the loader-read title/lifecycle transitions in time order.
// When visible is false the issue is redacted to its locator IDs.
func NewIssueSnapshot(issue *Issue, historyFirstCreated bool, lifecycle []TitleLifecycleEvent, visible bool, hiddenReason string, complete bool) (IssueSnapshot, error) {
	if issue == nil || issue.ID <= 0 || issue.RepoID <= 0 || issue.Index <= 0 {
		return IssueSnapshot{}, ErrSnapshotInvalid
	}
	if !visible {
		if !validHiddenReason(hiddenReason) {
			return IssueSnapshot{}, ErrSnapshotInvalid
		}
		return IssueSnapshot{
			RepositoryID: issue.RepoID,
			IssueID:      issue.ID,
			Index:        issue.Index,
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return IssueSnapshot{}, ErrSnapshotInvalid
	}
	if issue.ContentVersion < 0 || issue.NumComments < 0 {
		return IssueSnapshot{}, ErrSnapshotInvalid
	}
	created := int64(issue.CreatedUnix)
	updated := int64(issue.UpdatedUnix)
	closed := int64(issue.ClosedUnix)
	if created <= 0 || updated < created {
		return IssueSnapshot{}, ErrSnapshotInvalid
	}
	if issue.IsClosed != (closed > 0) {
		return IssueSnapshot{}, ErrSnapshotInvalid
	}
	if closed > 0 && closed < created {
		return IssueSnapshot{}, ErrSnapshotInvalid
	}
	for _, event := range lifecycle {
		if !validLifecycleEvent(event) {
			return IssueSnapshot{}, ErrSnapshotInvalid
		}
	}
	return IssueSnapshot{
		RepositoryID:   issue.RepoID,
		IssueID:        issue.ID,
		Index:          issue.Index,
		Title:          issue.Title,
		Content:        issue.Content,
		TitleDigest:    ContentDigest(issue.Title),
		ContentDigest:  ContentDigest(issue.Content),
		ContentVersion: issue.ContentVersion,
		NumComments:    issue.NumComments,
		IsClosed:       issue.IsClosed,
		IsLocked:       issue.IsLocked,
		IsPull:         issue.IsPull,
		Provenance: CreationProvenance{
			PosterID:     issue.PosterID,
			CreatedUnix:  created,
			FirstCreated: historyFirstCreated,
			Verified:     historyFirstCreated && issue.PosterID > 0,
		},
		Lifecycle:   append([]TitleLifecycleEvent(nil), lifecycle...),
		CreatedUnix: created,
		UpdatedUnix: updated,
		ClosedUnix:  closed,
		Visible:     true,
		Complete:    complete,
	}, nil
}

// CommentSnapshot is the permission-checked view of one native comment.
type CommentSnapshot struct {
	CommentID      int64
	IssueID        int64
	Type           string
	PosterID       int64
	Content        string
	ContentDigest  string
	ContentVersion int
	ReviewID       int64
	Invalidated    bool
	CreatedUnix    int64
	UpdatedUnix    int64
	Visible        bool
	HiddenReason   string
	Complete       bool
}

// NewCommentSnapshot converts one current comment row. When visible is
// false the comment is redacted to its locator IDs.
func NewCommentSnapshot(comment *Comment, visible bool, hiddenReason string, complete bool) (CommentSnapshot, error) {
	if comment == nil || comment.ID <= 0 || comment.IssueID <= 0 {
		return CommentSnapshot{}, ErrSnapshotInvalid
	}
	if !visible {
		if !validHiddenReason(hiddenReason) {
			return CommentSnapshot{}, ErrSnapshotInvalid
		}
		return CommentSnapshot{
			CommentID:    comment.ID,
			IssueID:      comment.IssueID,
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return CommentSnapshot{}, ErrSnapshotInvalid
	}
	if comment.ContentVersion < 0 || comment.PosterID < 0 || comment.ReviewID < 0 {
		return CommentSnapshot{}, ErrSnapshotInvalid
	}
	created := int64(comment.CreatedUnix)
	updated := int64(comment.UpdatedUnix)
	if created <= 0 || updated < created {
		return CommentSnapshot{}, ErrSnapshotInvalid
	}
	commentType := ""
	if comment.Type >= 0 {
		commentType = comment.Type.String()
	}
	if commentType == "" {
		return CommentSnapshot{}, ErrSnapshotInvalid
	}
	return CommentSnapshot{
		CommentID:      comment.ID,
		IssueID:        comment.IssueID,
		Type:           commentType,
		PosterID:       comment.PosterID,
		Content:        comment.Content,
		ContentDigest:  ContentDigest(comment.Content),
		ContentVersion: comment.ContentVersion,
		ReviewID:       comment.ReviewID,
		Invalidated:    comment.Invalidated,
		CreatedUnix:    created,
		UpdatedUnix:    updated,
		Visible:        true,
		Complete:       complete,
	}, nil
}

// CommentPage carries one bounded comment list with completeness evidence.
type CommentPage struct {
	IssueID  int64
	Items    []CommentSnapshot
	Total    int
	Complete bool
}

// NewCommentPage validates one loader-read comment list. Complete requires
// every Total item Returned with each item complete.
func NewCommentPage(issueID int64, items []CommentSnapshot, total int, complete bool) (CommentPage, error) {
	if issueID <= 0 || total < 0 || len(items) > total {
		return CommentPage{}, ErrSnapshotInvalid
	}
	for _, item := range items {
		if item.IssueID != issueID || !item.Complete {
			return CommentPage{}, ErrSnapshotInvalid
		}
	}
	if complete && len(items) != total {
		return CommentPage{}, ErrSnapshotInvalid
	}
	return CommentPage{
		IssueID:  issueID,
		Items:    append([]CommentSnapshot(nil), items...),
		Total:    total,
		Complete: complete,
	}, nil
}

// DependencySnapshot is one edge occurrence. OccurrenceID is the native
// dependency row ID: removal followed by readdition yields a new row, so
// consumers detect replacement by ID change, never by text comparison.
// A hidden occurrence preserves only the source-side locators; the target
// DependencyID is redacted so an inaccessible prerequisite blocks work
// without leaking which issue it is.
type DependencySnapshot struct {
	OccurrenceID int64
	IssueID      int64
	DependencyID int64
	CreatedUnix  int64
	UpdatedUnix  int64
	Visible      bool
	HiddenReason string
	Complete     bool
}

// NewDependencySnapshot converts one current dependency row.
func NewDependencySnapshot(dep *IssueDependency, visible bool, hiddenReason string, complete bool) (DependencySnapshot, error) {
	if dep == nil || dep.ID <= 0 || dep.IssueID <= 0 || dep.DependencyID <= 0 {
		return DependencySnapshot{}, ErrSnapshotInvalid
	}
	if !visible {
		if !validHiddenReason(hiddenReason) {
			return DependencySnapshot{}, ErrSnapshotInvalid
		}
		return DependencySnapshot{
			OccurrenceID: dep.ID,
			IssueID:      dep.IssueID,
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return DependencySnapshot{}, ErrSnapshotInvalid
	}
	created := int64(dep.CreatedUnix)
	updated := int64(dep.UpdatedUnix)
	if created <= 0 || updated < created {
		return DependencySnapshot{}, ErrSnapshotInvalid
	}
	return DependencySnapshot{
		OccurrenceID: dep.ID,
		IssueID:      dep.IssueID,
		DependencyID: dep.DependencyID,
		CreatedUnix:  created,
		UpdatedUnix:  updated,
		Visible:      true,
		Complete:     complete,
	}, nil
}

// DependencyPage carries one bounded edge-occurrence list with completeness.
type DependencyPage struct {
	IssueID  int64
	Items    []DependencySnapshot
	Total    int
	Complete bool
}

// NewDependencyPage validates one loader-read dependency list.
func NewDependencyPage(issueID int64, items []DependencySnapshot, total int, complete bool) (DependencyPage, error) {
	if issueID <= 0 || total < 0 || len(items) > total {
		return DependencyPage{}, ErrSnapshotInvalid
	}
	for _, item := range items {
		if item.IssueID != issueID || !item.Complete {
			return DependencyPage{}, ErrSnapshotInvalid
		}
	}
	if complete && len(items) != total {
		return DependencyPage{}, ErrSnapshotInvalid
	}
	return DependencyPage{
		IssueID:  issueID,
		Items:    append([]DependencySnapshot(nil), items...),
		Total:    total,
		Complete: complete,
	}, nil
}

func validFullOID(oid string) bool {
	return gitsyntax.FullOID(oid)
}

// PullSnapshot is the permission-checked view of one native pull request.
// HeadTip is the loader-resolved current head tip from live ref state;
// empty is allowed only for merged PRs whose branch is gone.
type PullSnapshot struct {
	PullID            int64
	IssueID           int64
	Index             int64
	HeadRepositoryID  int64
	HeadBranch        string
	HeadTip           string
	BaseBranch        string
	MergeBase         string
	HasMerged         bool
	MergedCommitID    string
	MergerID          int64
	MergedUnix        int64
	AllowMaintainerEd bool
	Flow              string
	Status            string
	Visible           bool
	HiddenReason      string
	Complete          bool
}

// NewPullSnapshot converts one current pull-request row with its head tip.
func NewPullSnapshot(pr *PullRequest, headTip string, visible bool, hiddenReason string, complete bool) (PullSnapshot, error) {
	if pr == nil || pr.ID <= 0 || pr.IssueID <= 0 || pr.Index <= 0 {
		return PullSnapshot{}, ErrSnapshotInvalid
	}
	if !visible {
		if !validHiddenReason(hiddenReason) {
			return PullSnapshot{}, ErrSnapshotInvalid
		}
		return PullSnapshot{
			PullID:       pr.ID,
			IssueID:      pr.IssueID,
			Index:        pr.Index,
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return PullSnapshot{}, ErrSnapshotInvalid
	}
	if pr.HeadRepoID <= 0 || pr.BaseRepoID <= 0 || pr.HeadBranch == "" || pr.BaseBranch == "" {
		return PullSnapshot{}, ErrSnapshotInvalid
	}
	if headTip != "" && !validFullOID(headTip) {
		return PullSnapshot{}, ErrSnapshotInvalid
	}
	if !pr.HasMerged && headTip == "" {
		return PullSnapshot{}, ErrSnapshotInvalid
	}
	if pr.MergeBase != "" && !validFullOID(pr.MergeBase) {
		return PullSnapshot{}, ErrSnapshotInvalid
	}
	if pr.MergedCommitID != "" && !validFullOID(pr.MergedCommitID) {
		return PullSnapshot{}, ErrSnapshotInvalid
	}
	// MergedUnix is xorm-maintained last-update time (the column carries the
	// `updated` tag: stamped on insert and every update), not merge evidence.
	// Merge consistency binds only HasMerged to the recorded merge commit,
	// and unmerged PRs report no merge time.
	if !pr.HasMerged && pr.MergedCommitID != "" {
		return PullSnapshot{}, ErrSnapshotInvalid
	}
	merged := int64(pr.MergedUnix)
	if !pr.HasMerged {
		merged = 0
	}
	var flow string
	switch pr.Flow {
	case PullRequestFlowGithub:
		flow = "github"
	case PullRequestFlowAGit:
		flow = "agit"
	default:
		return PullSnapshot{}, ErrSnapshotInvalid
	}
	return PullSnapshot{
		PullID:            pr.ID,
		IssueID:           pr.IssueID,
		Index:             pr.Index,
		HeadRepositoryID:  pr.HeadRepoID,
		HeadBranch:        pr.HeadBranch,
		HeadTip:           strings.ToLower(headTip),
		BaseBranch:        pr.BaseBranch,
		MergeBase:         strings.ToLower(pr.MergeBase),
		HasMerged:         pr.HasMerged,
		MergedCommitID:    strings.ToLower(pr.MergedCommitID),
		MergerID:          pr.MergerID,
		MergedUnix:        merged,
		AllowMaintainerEd: pr.AllowMaintainerEdit,
		Flow:              flow,
		Status:            pr.Status.String(),
		Visible:           true,
		Complete:          complete,
	}, nil
}

// ReviewSnapshot is the permission-checked view of one native review.
type ReviewSnapshot struct {
	ReviewID      int64
	IssueID       int64
	Type          string
	ReviewerID    int64
	CommitID      string
	Official      bool
	Stale         bool
	Dismissed     bool
	ContentDigest string
	CreatedUnix   int64
	UpdatedUnix   int64
	Visible       bool
	HiddenReason  string
	Complete      bool
}

func reviewTypeName(reviewType ReviewType) string {
	switch reviewType {
	case ReviewTypePending:
		return "PENDING"
	case ReviewTypeApprove:
		return "APPROVED"
	case ReviewTypeComment:
		return "COMMENT"
	case ReviewTypeReject:
		return "REQUEST_CHANGES"
	case ReviewTypeRequest:
		return "REQUEST_REVIEW"
	default:
		return ""
	}
}

// NewReviewSnapshot converts one current review row.
func NewReviewSnapshot(review *Review, visible bool, hiddenReason string, complete bool) (ReviewSnapshot, error) {
	if review == nil || review.ID <= 0 || review.IssueID <= 0 {
		return ReviewSnapshot{}, ErrSnapshotInvalid
	}
	if !visible {
		if !validHiddenReason(hiddenReason) {
			return ReviewSnapshot{}, ErrSnapshotInvalid
		}
		return ReviewSnapshot{
			ReviewID:     review.ID,
			IssueID:      review.IssueID,
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return ReviewSnapshot{}, ErrSnapshotInvalid
	}
	typeName := reviewTypeName(review.Type)
	if typeName == "" || review.ReviewerID < 0 {
		return ReviewSnapshot{}, ErrSnapshotInvalid
	}
	if !validFullOID(review.CommitID) {
		return ReviewSnapshot{}, ErrSnapshotInvalid
	}
	created := int64(review.CreatedUnix)
	updated := int64(review.UpdatedUnix)
	if created <= 0 || updated < created {
		return ReviewSnapshot{}, ErrSnapshotInvalid
	}
	return ReviewSnapshot{
		ReviewID:      review.ID,
		IssueID:       review.IssueID,
		Type:          typeName,
		ReviewerID:    review.ReviewerID,
		CommitID:      strings.ToLower(review.CommitID),
		Official:      review.Official,
		Stale:         review.Stale,
		Dismissed:     review.Dismissed,
		ContentDigest: ContentDigest(review.Content),
		CreatedUnix:   created,
		UpdatedUnix:   updated,
		Visible:       true,
		Complete:      complete,
	}, nil
}

// ReviewPage carries one bounded review list with completeness evidence.
type ReviewPage struct {
	IssueID  int64
	Items    []ReviewSnapshot
	Total    int
	Complete bool
}

// RepositoryIssuesItem is one enumerated open non-PR issue locator. The
// enumeration carries no title, body or other content: exact bodies travel
// through the individual issue/dependency snapshots under the same stable
// revision. Enumeration itself requires issue-read permission, so the loader
// emits only visible locators.
type RepositoryIssuesItem struct {
	IssueID int64
	Index   int64
	Visible bool
}

// RepositoryIssuesPage is one bounded open-issue enumeration page in
// ascending index order. NextCursor is the exclusive lower bound for the
// following page: empty on a terminal page, otherwise the final returned
// index. Complete means fully enumerated through the final page.
type RepositoryIssuesPage struct {
	Items      []RepositoryIssuesItem
	NextCursor string
	Complete   bool
}

// NewRepositoryIssuesPage validates one loader-read enumeration page.
// Indexes must ascend strictly; a terminal page carries an empty cursor
// while a nonterminal page carries the final returned index and nonempty
// items. An empty incomplete page is invalid: unavailable or hidden inputs
// can never imply an empty or complete set.
func NewRepositoryIssuesPage(items []RepositoryIssuesItem, nextCursor string, complete bool) (RepositoryIssuesPage, error) {
	for i, item := range items {
		if item.IssueID <= 0 || item.Index <= 0 || !item.Visible {
			return RepositoryIssuesPage{}, ErrSnapshotInvalid
		}
		if i > 0 && item.Index <= items[i-1].Index {
			return RepositoryIssuesPage{}, ErrSnapshotInvalid
		}
	}
	if nextCursor != "" && !validRepositoryIssuesCursor(nextCursor) {
		return RepositoryIssuesPage{}, ErrSnapshotInvalid
	}
	if complete {
		if nextCursor != "" {
			return RepositoryIssuesPage{}, ErrSnapshotInvalid
		}
	} else {
		if len(items) == 0 {
			return RepositoryIssuesPage{}, ErrSnapshotInvalid
		}
		last := items[len(items)-1]
		if nextCursor == "" || nextCursor != strconv.FormatInt(last.Index, 10) {
			return RepositoryIssuesPage{}, ErrSnapshotInvalid
		}
	}
	return RepositoryIssuesPage{
		Items:      append([]RepositoryIssuesItem(nil), items...),
		NextCursor: nextCursor,
		Complete:   complete,
	}, nil
}

// validRepositoryIssuesCursor accepts canonical positive int64 decimals:
// no leading zero, no sign, no whitespace.
func validRepositoryIssuesCursor(cursor string) bool {
	if cursor == "" || cursor[0] == '0' || len(cursor) > 20 {
		return false
	}
	value, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || value <= 0 {
		return false
	}
	return strconv.FormatInt(value, 10) == cursor
}

// ThreadMemberSnapshot is one permission-checked conversation member.
type ThreadMemberSnapshot struct {
	OriginalAnchor *CodeCommentAnchor
	Resolved       bool
	CommentID      int64
	IssueID        int64
	ContentDigest  string
	ContentVersion int
	CreatedUnix    int64
	UpdatedUnix    int64
	Visible        bool
	HiddenReason   string
	Complete       bool
}

// ThreadSnapshot is the permission-checked view of one native code-review
// conversation. Location preserves native path/side/unsigned line (zero has
// no recorded side); OriginalAnchor separately retains exact immutable context.
// OriginalCommit is the root's recorded commit;
// HeadCommit the current head tip bound at read time (empty when the head
// ref is gone). Digest binds the ordered member lines; hidden threads
// carry no digest, location, commits or members.
type ThreadSnapshot struct {
	OriginalAnchor *CodeCommentAnchor
	RootID         int64
	IssueID        int64
	ReviewID       int64
	Path           string
	Side           string
	Line           int64
	OriginalCommit string
	HeadCommit     string
	Outdated       bool
	ResolverID     int64
	Resolved       bool
	Digest         string
	Members        []ThreadMemberSnapshot
	MemberTotal    int
	Visible        bool
	HiddenReason   string
	Complete       bool
}

// validThreadBlockerDigest checks the 64-hex conversation digest shape.
func validThreadDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

// NewThreadMemberSnapshot converts one current member row. Hidden members
// redact to locator IDs.
func NewThreadMemberSnapshot(comment *Comment, visible bool, hiddenReason string, complete bool) (ThreadMemberSnapshot, error) {
	if comment == nil || comment.ID <= 0 {
		return ThreadMemberSnapshot{}, ErrSnapshotInvalid
	}
	if !visible {
		if !validHiddenReason(hiddenReason) {
			return ThreadMemberSnapshot{}, ErrSnapshotInvalid
		}
		return ThreadMemberSnapshot{
			CommentID:    comment.ID,
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return ThreadMemberSnapshot{}, ErrSnapshotInvalid
	}
	if comment.ContentVersion < 0 {
		return ThreadMemberSnapshot{}, ErrSnapshotInvalid
	}
	if comment.OriginalAnchor != nil && comment.OriginalAnchor.Validate() != nil {
		return ThreadMemberSnapshot{}, ErrSnapshotInvalid
	}
	created := int64(comment.CreatedUnix)
	updated := int64(comment.UpdatedUnix)
	if created <= 0 || updated < created {
		return ThreadMemberSnapshot{}, ErrSnapshotInvalid
	}
	return ThreadMemberSnapshot{
		OriginalAnchor: comment.OriginalAnchor.Copy(),
		Resolved:       comment.ResolveDoerID != 0,
		CommentID:      comment.ID,
		IssueID:        comment.IssueID,
		ContentDigest:  ContentDigest(comment.Content),
		ContentVersion: comment.ContentVersion,
		CreatedUnix:    created,
		UpdatedUnix:    updated,
		Visible:        true,
		Complete:       complete,
	}, nil
}

// NewThreadSnapshot converts one grouped conversation. root is the native
// root (first in created/id order); digest binds the ordered member
// lines. A hidden thread redacts to its root locator. Resolver
// consistency binds Resolved to a set ResolverID. Outdated retains native
// invalidation and original-head drift, with recorded-commit fallback only
// when original context is unknown.
func NewThreadSnapshot(root *Comment, issueID, reviewID int64, path, side string, line int64, originalCommit, headCommit, digest string, members []ThreadMemberSnapshot, memberTotal int, resolverID int64, resolved, outdated, visible bool, hiddenReason string, complete bool) (ThreadSnapshot, error) {
	if root == nil || root.ID <= 0 || issueID <= 0 || reviewID <= 0 {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	if !visible {
		if !validHiddenReason(hiddenReason) {
			return ThreadSnapshot{}, ErrSnapshotInvalid
		}
		return ThreadSnapshot{
			RootID:       root.ID,
			IssueID:      issueID,
			ReviewID:     reviewID,
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	if path == "" || line < 0 || line == 0 && side != "" || line > 0 && side != "old" && side != "new" {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	if root.OriginalAnchor != nil && root.OriginalAnchor.Validate() != nil {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	if originalCommit != "" && !validFullOID(originalCommit) {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	if headCommit != "" && !validFullOID(headCommit) {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	if !validThreadDigest(digest) {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	if memberTotal < 0 || len(members) > memberTotal {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	for _, member := range members {
		if !member.Complete {
			return ThreadSnapshot{}, ErrSnapshotInvalid
		}
	}
	if complete && len(members) != memberTotal {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	if resolved != (resolverID != 0) {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	if resolverID < 0 {
		return ThreadSnapshot{}, ErrSnapshotInvalid
	}
	return ThreadSnapshot{
		OriginalAnchor: root.OriginalAnchor.Copy(),
		RootID:         root.ID,
		IssueID:        issueID,
		ReviewID:       reviewID,
		Path:           path,
		Side:           side,
		Line:           line,
		OriginalCommit: originalCommit,
		HeadCommit:     headCommit,
		Outdated:       outdated,
		ResolverID:     resolverID,
		Resolved:       resolved,
		Digest:         digest,
		Members:        append([]ThreadMemberSnapshot(nil), members...),
		MemberTotal:    memberTotal,
		Visible:        true,
		Complete:       complete,
	}, nil
}

// ThreadPage carries one bounded conversation list with completeness.
// Roots ascend strictly in root-ID order; NextCursor is the exclusive
// lower bound for the following page (empty on a terminal page,
// otherwise the final returned root ID). Complete means fully enumerated
// through the final page with every thread fully evidenced.
type ThreadPage struct {
	IssueID    int64
	Items      []ThreadSnapshot
	NextCursor string
	Total      int
	Complete   bool
}

// NewThreadPage validates one loader-read conversation list. A complete
// page carries every remaining thread fully evidenced with an empty
// cursor. An incomplete page carries incomplete items, which the consumer
// must refuse: either a nonterminal page with nonempty items and the
// cursor at the final returned root, or a terminal-incomplete page with
// an empty cursor when everything remaining was observed but blocked
// (hidden, truncated or dropped evidence). An empty incomplete page is
// invalid.
func NewThreadPage(issueID int64, items []ThreadSnapshot, nextCursor string, total int, complete bool) (ThreadPage, error) {
	if issueID <= 0 || total < 0 || len(items) > total {
		return ThreadPage{}, ErrSnapshotInvalid
	}
	for i, item := range items {
		if item.IssueID != issueID || item.RootID <= 0 {
			return ThreadPage{}, ErrSnapshotInvalid
		}
		if i > 0 && item.RootID <= items[i-1].RootID {
			return ThreadPage{}, ErrSnapshotInvalid
		}
		if complete && !item.Complete {
			return ThreadPage{}, ErrSnapshotInvalid
		}
	}
	if nextCursor != "" && !validRepositoryIssuesCursor(nextCursor) {
		return ThreadPage{}, ErrSnapshotInvalid
	}
	if complete {
		if nextCursor != "" || len(items) != total {
			return ThreadPage{}, ErrSnapshotInvalid
		}
		return ThreadPage{
			IssueID:    issueID,
			Items:      append([]ThreadSnapshot(nil), items...),
			NextCursor: nextCursor,
			Total:      total,
			Complete:   complete,
		}, nil
	}
	if len(items) == 0 {
		return ThreadPage{}, ErrSnapshotInvalid
	}
	if nextCursor != "" {
		last := items[len(items)-1]
		if nextCursor != strconv.FormatInt(last.RootID, 10) {
			return ThreadPage{}, ErrSnapshotInvalid
		}
	}
	return ThreadPage{
		IssueID:    issueID,
		Items:      append([]ThreadSnapshot(nil), items...),
		NextCursor: nextCursor,
		Total:      total,
		Complete:   complete,
	}, nil
}

// Merge blockers name the first typed native merge refusal in evaluator
// order. Empty means eligible.
const (
	MergeBlockedMerged           = "merged"
	MergeBlockedClosed           = "closed"
	MergeBlockedPermission       = "permission"
	MergeBlockedWorkInProgress   = "work_in_progress"
	MergeBlockedNotMergeable     = "not_mergeable"
	MergeBlockedChecking         = "checking"
	MergeBlockedStatusChecks     = "status_checks"
	MergeBlockedApprovals        = "approvals"
	MergeBlockedRequestedChanges = "requested_changes"
	MergeBlockedReviewRequests   = "review_requests"
	MergeBlockedOutdatedBranch   = "outdated_branch"
	MergeBlockedProtectedFiles   = "protected_files"
	MergeBlockedSignature        = "signature"
	MergeBlockedDependency       = "dependency"
	MergeBlockedBranchProtection = "branch_protection"
)

func validMergeBlocker(blocker string) bool {
	switch blocker {
	case MergeBlockedMerged, MergeBlockedClosed, MergeBlockedPermission,
		MergeBlockedWorkInProgress, MergeBlockedNotMergeable, MergeBlockedChecking,
		MergeBlockedStatusChecks, MergeBlockedApprovals, MergeBlockedRequestedChanges,
		MergeBlockedReviewRequests, MergeBlockedOutdatedBranch, MergeBlockedProtectedFiles,
		MergeBlockedSignature, MergeBlockedDependency, MergeBlockedBranchProtection:
		return true
	default:
		return false
	}
}

// MergeEligibilitySnapshot is the permission-checked actor-bound merge
// readiness of one pull request: the bound tips, the eligibility verdict
// with its first typed blocker, and the official rejecting review IDs
// behind a requested-changes blocker. Hidden eligibility redacts to the
// pull locator.
type MergeEligibilitySnapshot struct {
	PullID            int64
	IssueID           int64
	Index             int64
	HeadTip           string
	BaseTip           string
	Eligible          bool
	Blocker           string
	BlockingReviewIDs []int64
	Visible           bool
	HiddenReason      string
	Complete          bool
}

// NewMergeEligibilitySnapshot converts one evaluated pull request. An
// eligible verdict carries no blocker or review IDs; a blocked verdict
// carries exactly one valid blocker. Blocking review IDs stay within the
// snapshot page bound.
func NewMergeEligibilitySnapshot(pr *PullRequest, headTip, baseTip string, eligible bool, blocker string, blockingReviewIDs []int64, visible bool, hiddenReason string, complete bool) (MergeEligibilitySnapshot, error) {
	if pr == nil || pr.ID <= 0 || pr.IssueID <= 0 || pr.Index <= 0 {
		return MergeEligibilitySnapshot{}, ErrSnapshotInvalid
	}
	if !visible {
		if !validHiddenReason(hiddenReason) {
			return MergeEligibilitySnapshot{}, ErrSnapshotInvalid
		}
		return MergeEligibilitySnapshot{
			PullID:       pr.ID,
			IssueID:      pr.IssueID,
			Index:        pr.Index,
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return MergeEligibilitySnapshot{}, ErrSnapshotInvalid
	}
	if headTip != "" && !validFullOID(headTip) {
		return MergeEligibilitySnapshot{}, ErrSnapshotInvalid
	}
	if baseTip != "" && !validFullOID(baseTip) {
		return MergeEligibilitySnapshot{}, ErrSnapshotInvalid
	}
	if eligible {
		if blocker != "" || len(blockingReviewIDs) > 0 {
			return MergeEligibilitySnapshot{}, ErrSnapshotInvalid
		}
	} else if !validMergeBlocker(blocker) {
		return MergeEligibilitySnapshot{}, ErrSnapshotInvalid
	}
	if len(blockingReviewIDs) > 50 {
		return MergeEligibilitySnapshot{}, ErrSnapshotInvalid
	}
	seen := make(map[int64]bool, len(blockingReviewIDs))
	for _, id := range blockingReviewIDs {
		if id <= 0 || seen[id] {
			return MergeEligibilitySnapshot{}, ErrSnapshotInvalid
		}
		seen[id] = true
	}
	return MergeEligibilitySnapshot{
		PullID:            pr.ID,
		IssueID:           pr.IssueID,
		Index:             pr.Index,
		HeadTip:           headTip,
		BaseTip:           baseTip,
		Eligible:          eligible,
		Blocker:           blocker,
		BlockingReviewIDs: append([]int64(nil), blockingReviewIDs...),
		Visible:           true,
		Complete:          complete,
	}, nil
}

// NewReviewPage validates one loader-read review list.
func NewReviewPage(issueID int64, items []ReviewSnapshot, total int, complete bool) (ReviewPage, error) {
	if issueID <= 0 || total < 0 || len(items) > total {
		return ReviewPage{}, ErrSnapshotInvalid
	}
	for _, item := range items {
		if item.IssueID != issueID || !item.Complete {
			return ReviewPage{}, ErrSnapshotInvalid
		}
	}
	if complete && len(items) != total {
		return ReviewPage{}, ErrSnapshotInvalid
	}
	return ReviewPage{
		IssueID:  issueID,
		Items:    append([]ReviewSnapshot(nil), items...),
		Total:    total,
		Complete: complete,
	}, nil
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"errors"
	"strconv"
	"strings"
)

// Permission-checked native snapshot reads (FT10, F-read).
//
// A SnapshotRequest selects bounded native families; the host answers with a
// NativeSnapshot carrying exact versions, creation/title/lifecycle evidence,
// edge occurrences, visibility and completeness. Hidden records are redacted
// to their locator keys with Visible=false so the consumer blocks work
// without leaking inaccessible content.
//
// The snapshot carries no revision: the caller brackets the read between two
// equal idle ReadNativeRevision observations and binds that revision itself.
// A changed/busy observation, a missing page or an incomplete section cannot
// authorize work. No factory policy is interpreted here: no acceptance,
// readiness, budget or approval fields.

// Snapshot families select bounded native evidence. Unknown families refuse.
const (
	SnapshotFamilyRepositoryWriters = "repository_writers"
	SnapshotFamilyRepositoryIssues  = "repository_issues"
	SnapshotFamilyIssue             = "issue"
	SnapshotFamilyComments          = "comments"
	SnapshotFamilyDependencies      = "dependencies"
	SnapshotFamilyPull              = "pull"
	SnapshotFamilyReviews           = "reviews"
	SnapshotFamilyReviewThreads     = "review_threads"
	SnapshotFamilyMergeEligibility  = "merge_eligibility"
	SnapshotFamilyChecks            = "checks"
	SnapshotFamilyRefs              = "refs"
	SnapshotFamilyAncestry          = "ancestry"
)

// Snapshot bounds keep reads bounded and attributable.
const (
	SnapshotPageLimit   = 50
	SnapshotIDListLimit = 50
	SnapshotRefLimit    = 8
	SnapshotCursorLimit = 4096
)

// ErrInvalidSnapshotRequest rejects malformed requests rather than silently
// broadening or defaulting them.
var ErrInvalidSnapshotRequest = errors.New("invalid native snapshot request")

// SnapshotAncestryRequest selects a scalar relation between two exact Git commits.
type SnapshotAncestryRequest struct {
	// AncestorOID is the exact candidate/result whose reachability is checked.
	AncestorOID string `json:"ancestor_oid"`
	// DescendantOID is the exact currently observed target tip.
	DescendantOID string `json:"descendant_oid"`
}

// SnapshotRequest selects bounded native evidence. Native integer IDs use
// decimal strings; refs are full branch refs; SHAs are full object IDs.
// ActorID names the bound native actor whose credential is presented
// privately alongside the request; the host verifies the credential belongs
// to that actor. Cursor is an opaque decimal after-ID for list families:
// items carry IDs strictly greater than the cursor, ordered ascending.
type SnapshotRequest struct {
	RepositoryID string                   `json:"repository_id"`
	WriterIDs    []string                 `json:"writer_ids,omitempty"`
	ActorID      string                   `json:"actor_id"`
	Families     []string                 `json:"families"`
	IssueIndex   string                   `json:"issue_index,omitempty"`
	PullNumber   string                   `json:"pull_number,omitempty"`
	CommentIDs   []string                 `json:"comment_ids,omitempty"`
	SHA          string                   `json:"sha,omitempty"`
	Refs         []string                 `json:"refs,omitempty"`
	Limit        int                      `json:"limit,omitempty"`
	Cursor       string                   `json:"cursor,omitempty"`
	Ancestry     *SnapshotAncestryRequest `json:"ancestry,omitempty"`
}

func validSnapshotFamily(family string) bool {
	switch family {
	case SnapshotFamilyRepositoryWriters, SnapshotFamilyRepositoryIssues, SnapshotFamilyIssue, SnapshotFamilyComments,
		SnapshotFamilyDependencies, SnapshotFamilyPull, SnapshotFamilyReviews, SnapshotFamilyReviewThreads,
		SnapshotFamilyMergeEligibility, SnapshotFamilyChecks,
		SnapshotFamilyRefs, SnapshotFamilyAncestry:
		return true
	default:
		return false
	}
}

func snapshotDecimalID(id string) bool {
	if id == "" || id[0] == '0' || len(id) > 20 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	_, err := strconv.ParseInt(id, 10, 64)
	return err == nil
}

func snapshotFullOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func snapshotFullBranchRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/heads/") || len(ref) > 512 {
		return false
	}
	name := strings.TrimSpace(ref)
	if name != ref || strings.ContainsAny(ref, " ~^:?*\\") || strings.Contains(ref, "..") {
		return false
	}
	return len(strings.TrimPrefix(ref, "refs/heads/")) > 0
}

// ValidateSnapshotRequest bounds the requested families and locators. Each
// family requires its selector so the host never guesses which records the
// caller means: issue/comments/dependencies need the issue (or explicit
// comment IDs), pull/reviews need the pull or issue, checks need the commit,
// refs need the ref list. Comment selection is either an issue list or an
// explicit ID list, never both; review selection is either an issue or a
// pull, never both.
func ValidateSnapshotRequest(req SnapshotRequest) error {
	if !snapshotDecimalID(req.RepositoryID) || !snapshotDecimalID(req.ActorID) {
		return ErrInvalidSnapshotRequest
	}
	if len(req.WriterIDs) > 8 {
		return ErrInvalidSnapshotRequest
	}
	writerSeen := map[string]bool{}
	for _, id := range req.WriterIDs {
		if !snapshotDecimalID(id) || writerSeen[id] {
			return ErrInvalidSnapshotRequest
		}
		writerSeen[id] = true
	}
	if len(req.Families) == 0 || len(req.Families) > 8 {
		return ErrInvalidSnapshotRequest
	}
	seen := make(map[string]bool, len(req.Families))
	for _, family := range req.Families {
		if !validSnapshotFamily(family) || seen[family] {
			return ErrInvalidSnapshotRequest
		}
		seen[family] = true
	}
	if req.IssueIndex != "" && !snapshotDecimalID(req.IssueIndex) {
		return ErrInvalidSnapshotRequest
	}
	if req.PullNumber != "" && !snapshotDecimalID(req.PullNumber) {
		return ErrInvalidSnapshotRequest
	}
	if len(req.CommentIDs) > SnapshotIDListLimit {
		return ErrInvalidSnapshotRequest
	}
	for _, id := range req.CommentIDs {
		if !snapshotDecimalID(id) {
			return ErrInvalidSnapshotRequest
		}
	}
	if req.SHA != "" && !snapshotFullOID(req.SHA) {
		return ErrInvalidSnapshotRequest
	}
	if len(req.Refs) > SnapshotRefLimit {
		return ErrInvalidSnapshotRequest
	}
	for _, ref := range req.Refs {
		if !snapshotFullBranchRef(ref) {
			return ErrInvalidSnapshotRequest
		}
	}
	if req.Limit < 0 || req.Limit > SnapshotPageLimit || len(req.Cursor) > SnapshotCursorLimit {
		return ErrInvalidSnapshotRequest
	}
	if req.Cursor != "" && !snapshotDecimalID(req.Cursor) {
		return ErrInvalidSnapshotRequest
	}
	if seen[SnapshotFamilyRepositoryWriters] != (len(req.WriterIDs) > 0) {
		return ErrInvalidSnapshotRequest
	}
	if seen[SnapshotFamilyRefs] && len(req.Refs) == 0 {
		return ErrInvalidSnapshotRequest
	}
	// repository_issues enumerates open non-PR issues of exactly
	// RepositoryID under the bound actor's issue-read permission. It is
	// requested alone with only Limit/Cursor selectors: the cursor is an
	// exclusive issue-index lower bound (empty starts enumeration), and
	// the limit stays within the shared page bound with zero meaning the
	// default page. No issue/pull/ref selector may accompany it.
	if seen[SnapshotFamilyRepositoryIssues] {
		if len(req.Families) != 1 || len(req.WriterIDs) > 0 || req.IssueIndex != "" ||
			req.PullNumber != "" || len(req.CommentIDs) > 0 || req.SHA != "" ||
			len(req.Refs) > 0 || req.Ancestry != nil {
			return ErrInvalidSnapshotRequest
		}
	}
	if seen[SnapshotFamilyAncestry] != (req.Ancestry != nil) {
		return ErrInvalidSnapshotRequest
	}
	if req.Ancestry != nil && (!seen[SnapshotFamilyRefs] ||
		!snapshotFullOID(req.Ancestry.AncestorOID) || !snapshotFullOID(req.Ancestry.DescendantOID)) {
		return ErrInvalidSnapshotRequest
	}
	if seen[SnapshotFamilyChecks] && req.SHA == "" {
		return ErrInvalidSnapshotRequest
	}
	if seen[SnapshotFamilyIssue] && req.IssueIndex == "" {
		return ErrInvalidSnapshotRequest
	}
	if seen[SnapshotFamilyDependencies] && req.IssueIndex == "" {
		return ErrInvalidSnapshotRequest
	}
	if seen[SnapshotFamilyPull] && req.PullNumber == "" {
		return ErrInvalidSnapshotRequest
	}
	if seen[SnapshotFamilyComments] {
		byIssue := req.IssueIndex != ""
		byIDs := len(req.CommentIDs) > 0
		if byIssue == byIDs {
			return ErrInvalidSnapshotRequest
		}
	}
	if seen[SnapshotFamilyReviews] {
		byIssue := req.IssueIndex != ""
		byPull := req.PullNumber != ""
		if byIssue == byPull {
			return ErrInvalidSnapshotRequest
		}
	}
	// review_threads enumerates native code-review conversations on one
	// pull request, selected either by its issue or by its pull number,
	// never both and never neither. merge_eligibility reads the
	// actor-bound merge readiness of one pull request, so it requires
	// the pull selector; it combines with other families under the same
	// revision bracket.
	if seen[SnapshotFamilyReviewThreads] {
		byIssue := req.IssueIndex != ""
		byPull := req.PullNumber != ""
		if byIssue == byPull {
			return ErrInvalidSnapshotRequest
		}
	}
	if seen[SnapshotFamilyMergeEligibility] && req.PullNumber == "" {
		return ErrInvalidSnapshotRequest
	}
	return nil
}

// SnapshotLifecycleEvent is one title/lifecycle transition.
type SnapshotLifecycleEvent struct {
	OccurrenceID string `json:"occurrence_id"`
	Kind         string `json:"kind"`
	AtUnix       int64  `json:"at_unix"`
	ActorID      string `json:"actor_id"`
	OldTitle     string `json:"old_title,omitempty"`
	NewTitle     string `json:"new_title,omitempty"`
}

// SnapshotCreationProvenance binds verified creation evidence. Verified
// requires visibility plus the native first-created flag; cached text alone
// never verifies provenance.
type SnapshotCreationProvenance struct {
	PosterID     string `json:"poster_id,omitempty"`
	CreatedUnix  int64  `json:"created_unix,omitempty"`
	FirstCreated bool   `json:"first_created,omitempty"`
	Verified     bool   `json:"verified,omitempty"`
}

// SnapshotIssue is the permission-checked view of one native issue. Hidden
// issues preserve only their locator IDs with Visible=false.
type SnapshotIssue struct {
	ID             string                     `json:"id"`
	Index          string                     `json:"index"`
	Title          string                     `json:"title,omitempty"`
	Content        string                     `json:"content,omitempty"`
	TitleDigest    string                     `json:"title_digest,omitempty"`
	ContentDigest  string                     `json:"content_digest,omitempty"`
	ContentVersion int                        `json:"content_version,omitempty"`
	NumComments    int                        `json:"num_comments,omitempty"`
	IsClosed       bool                       `json:"is_closed,omitempty"`
	IsLocked       bool                       `json:"is_locked,omitempty"`
	IsPull         bool                       `json:"is_pull,omitempty"`
	Provenance     SnapshotCreationProvenance `json:"provenance"`
	Lifecycle      []SnapshotLifecycleEvent   `json:"lifecycle,omitempty"`
	CreatedUnix    int64                      `json:"created_unix,omitempty"`
	UpdatedUnix    int64                      `json:"updated_unix,omitempty"`
	ClosedUnix     int64                      `json:"closed_unix,omitempty"`
	Visible        bool                       `json:"visible"`
	HiddenReason   string                     `json:"hidden_reason,omitempty"`
	Complete       bool                       `json:"complete"`
}

// SnapshotComment is the permission-checked view of one native comment.
type SnapshotComment struct {
	ID             string `json:"id"`
	IssueID        string `json:"issue_id"`
	Type           string `json:"type,omitempty"`
	PosterID       string `json:"poster_id,omitempty"`
	Content        string `json:"content,omitempty"`
	ContentDigest  string `json:"content_digest,omitempty"`
	ContentVersion int    `json:"content_version,omitempty"`
	ReviewID       string `json:"review_id,omitempty"`
	Invalidated    bool   `json:"invalidated,omitempty"`
	CreatedUnix    int64  `json:"created_unix,omitempty"`
	UpdatedUnix    int64  `json:"updated_unix,omitempty"`
	Visible        bool   `json:"visible"`
	HiddenReason   string `json:"hidden_reason,omitempty"`
	Complete       bool   `json:"complete"`
}

// SnapshotCommentPage carries one bounded comment list with completeness
// evidence.
type SnapshotCommentPage struct {
	IssueID  string            `json:"issue_id"`
	Items    []SnapshotComment `json:"items"`
	Total    int               `json:"total"`
	Complete bool              `json:"complete"`
}

// SnapshotDependency is one edge occurrence. OccurrenceID distinguishes
// removal plus readdition of the same edge; hidden occurrences redact the
// target so an inaccessible prerequisite blocks without leaking it.
type SnapshotDependency struct {
	OccurrenceID string `json:"occurrence_id"`
	IssueID      string `json:"issue_id"`
	DependencyID string `json:"dependency_id,omitempty"`
	CreatedUnix  int64  `json:"created_unix,omitempty"`
	UpdatedUnix  int64  `json:"updated_unix,omitempty"`
	Visible      bool   `json:"visible"`
	HiddenReason string `json:"hidden_reason,omitempty"`
	Complete     bool   `json:"complete"`
}

// SnapshotDependencyPage carries one bounded edge list with completeness
// evidence.
type SnapshotDependencyPage struct {
	IssueID  string               `json:"issue_id"`
	Items    []SnapshotDependency `json:"items"`
	Total    int                  `json:"total"`
	Complete bool                 `json:"complete"`
}

// SnapshotRepositoryIssueItem is one enumerated open non-PR issue locator.
// The page carries no title, body or other content: exact bodies travel
// through the existing individual issue/dependency families under the same
// stable revision. Every returned item is visible to the bound actor;
// enumeration itself requires issue-read permission, so hidden records
// never appear here with or without content.
type SnapshotRepositoryIssueItem struct {
	IssueID    string `json:"issue_id"`
	IssueIndex string `json:"issue_index"`
	Visible    bool   `json:"visible"`
}

// SnapshotRepositoryIssuesPage is one bounded open-issue enumeration page
// in ascending issue-index order. Cursor is an exclusive issue-index lower
// bound: a nonterminal page carries nonempty items with NextCursor equal to
// the final returned index, strictly greater than the request cursor, and
// Complete=false. A terminal page carries Complete=true with empty
// NextCursor. Complete means fully enumerated through the final page, not
// merely a successful query; an empty incomplete page is invalid.
type SnapshotRepositoryIssuesPage struct {
	Items      []SnapshotRepositoryIssueItem `json:"items"`
	NextCursor string                        `json:"next_cursor,omitempty"`
	Complete   bool                          `json:"complete"`
}

// SnapshotPull is the permission-checked view of one native pull request.
//
// MergeBase boundary: for a visible UNMERGED pull request this is the
// genuinely derived CURRENT merge-base of the live HeadTip and the
// current named BaseBranch tip, observed under the caller's idle
// equal-revision bracket. It is never the stored pull-row field, never a
// cached history value, and never an ancestry boolean standing in for the
// greatest common ancestor. When no current unmerged pair exists (merged
// pull, deleted head branch) the field is omitted rather than certifying
// history as current. Historical review receipts stay bound to their own
// admitted operation OIDs and never substitute for this observation.
type SnapshotPull struct {
	ID           string `json:"id"`
	IssueID      string `json:"issue_id"`
	Number       string `json:"number"`
	HeadRepoID   string `json:"head_repo_id,omitempty"`
	HeadBranch   string `json:"head_branch,omitempty"`
	HeadTip      string `json:"head_tip,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"`
	MergeBase    string `json:"merge_base,omitempty"`
	HasMerged    bool   `json:"has_merged,omitempty"`
	MergedCommit string `json:"merged_commit,omitempty"`
	MergerID     string `json:"merger_id,omitempty"`
	MergedUnix   int64  `json:"merged_unix,omitempty"`
	MaintainerEd bool   `json:"allow_maintainer_edit,omitempty"`
	Flow         string `json:"flow,omitempty"`
	Status       string `json:"status,omitempty"`
	Visible      bool   `json:"visible"`
	HiddenReason string `json:"hidden_reason,omitempty"`
	Complete     bool   `json:"complete"`
}

// SnapshotReview is the permission-checked view of one native review.
type SnapshotReview struct {
	ID            string `json:"id"`
	IssueID       string `json:"issue_id"`
	Type          string `json:"type,omitempty"`
	ReviewerID    string `json:"reviewer_id,omitempty"`
	CommitID      string `json:"commit_id,omitempty"`
	Official      bool   `json:"official,omitempty"`
	Stale         bool   `json:"stale,omitempty"`
	Dismissed     bool   `json:"dismissed,omitempty"`
	ContentDigest string `json:"content_digest,omitempty"`
	CreatedUnix   int64  `json:"created_unix,omitempty"`
	UpdatedUnix   int64  `json:"updated_unix,omitempty"`
	Visible       bool   `json:"visible"`
	HiddenReason  string `json:"hidden_reason,omitempty"`
	Complete      bool   `json:"complete"`
}

// SnapshotReviewPage carries one bounded review list with completeness
// evidence.
type SnapshotReviewPage struct {
	IssueID  string           `json:"issue_id"`
	Items    []SnapshotReview `json:"items"`
	Total    int              `json:"total"`
	Complete bool             `json:"complete"`
}

// Thread sides name the anchored diff side: old is the native merge-base
// tree, new the exact head.
const (
	ThreadSideOld = "old"
	ThreadSideNew = "new"
)

// Thread resolver states name the conversation resolution behind the
// digest: open, or resolved by the recorded native actor.
const (
	ThreadResolverOpen     = "open"
	ThreadResolverResolved = "resolved"
)

// RenderThreadLocation renders one thread member location in the canonical
// digest encoding shared with the Soda factory twin: path, side and
// unsigned line joined by vertical bars. Both sides must reproduce these
// bytes exactly; the digest comparison refuses any divergence as changed
// content.
func RenderThreadLocation(path, side string, line int64) string {
	return path + "|" + side + "|" + strconv.FormatInt(line, 10)
}

// ThreadResolverState renders one member resolver state for the digest:
// resolved when the native resolve-doer is set, open otherwise. The
// identity behind a resolution travels in the thread ResolverID field,
// not in the digest.
func ThreadResolverState(resolveDoerID int64) string {
	if resolveDoerID != 0 {
		return ThreadResolverResolved
	}
	return ThreadResolverOpen
}

// SnapshotThreadMember is one permission-checked conversation member. The
// snapshot carries no raw bodies: exact bodies travel through the existing
// comments family under the same stable revision and cross-check against
// ContentDigest. A hidden member keeps only its locator ID with
// Visible=false so the consumer blocks without leaking pending content.
type SnapshotThreadMember struct {
	OriginalAnchor *RepositoryInlineAnchor `json:"original_anchor"`
	Resolved       bool                    `json:"resolved"`
	ID             string                  `json:"id"`
	IssueID        string                  `json:"issue_id"`
	ContentDigest  string                  `json:"content_digest,omitempty"`
	ContentVersion int                     `json:"content_version,omitempty"`
	CreatedUnix    int64                   `json:"created_unix,omitempty"`
	UpdatedUnix    int64                   `json:"updated_unix,omitempty"`
	Visible        bool                    `json:"visible"`
	HiddenReason   string                  `json:"hidden_reason,omitempty"`
	Complete       bool                    `json:"complete"`
}

// SnapshotThread is the permission-checked view of one native code-review
// conversation: the anchored root comment, its location, the original and
// current commits, the resolver and the complete conversation digest. The
// root is the first comment in native (created_unix, id) order within an
// immutable-context partition of a native location bucket; replies are members, never
// resolution targets. Outdated retains native invalidation and original-head
// drift; absent original context falls back to the recorded native commit.
// Path/Side/Line are recorded native coordinates (zero has no recorded side).
// OriginalAnchor and each member's OriginalAnchor retain the exact original
// selection, or nil for unknown context. Digest binds those ordered member
// contexts and resolver states; a hidden thread carries no context or digest.
type SnapshotThread struct {
	OriginalAnchor *RepositoryInlineAnchor `json:"original_anchor"`
	RootID         string                  `json:"root_id"`
	IssueID        string                  `json:"issue_id"`
	ReviewID       string                  `json:"review_id"`
	Path           string                  `json:"path,omitempty"`
	Side           string                  `json:"side,omitempty"`
	Line           int64                   `json:"line,omitempty"`
	OriginalCommit string                  `json:"original_commit,omitempty"`
	HeadCommit     string                  `json:"head_commit,omitempty"`
	Outdated       bool                    `json:"outdated,omitempty"`
	ResolverID     string                  `json:"resolver_id,omitempty"`
	Resolved       bool                    `json:"resolved,omitempty"`
	Digest         string                  `json:"digest,omitempty"`
	Members        []SnapshotThreadMember  `json:"members"`
	MemberTotal    int                     `json:"member_total"`
	Visible        bool                    `json:"visible"`
	HiddenReason   string                  `json:"hidden_reason,omitempty"`
	Complete       bool                    `json:"complete"`
}

// SnapshotThreadPage carries one bounded conversation list with
// completeness evidence. Threads order by ascending root ID; the cursor is
// an exclusive root-ID lower bound, and a nonterminal page carries
// NextCursor equal to the final returned root ID. Complete means fully
// enumerated through the final page with every thread fully evidenced; a
// hidden, truncated or partially grouped page reports Complete=false so
// the consumer blocks, with an empty cursor when everything remaining was
// observed but blocked. An empty incomplete page is invalid.
type SnapshotThreadPage struct {
	IssueID    string           `json:"issue_id"`
	PullNumber string           `json:"pull_number"`
	Items      []SnapshotThread `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
	Total      int              `json:"total"`
	Complete   bool             `json:"complete"`
}

// Merge-eligibility blockers name the first typed native merge refusal in
// evaluator order. Empty means eligible. branch_protection is the closed
// fallback when the evaluator reports a protection refusal no predicate
// attributes.
const (
	MergeBlockerMerged           = "merged"
	MergeBlockerClosed           = "closed"
	MergeBlockerPermission       = "permission"
	MergeBlockerWorkInProgress   = "work_in_progress"
	MergeBlockerNotMergeable     = "not_mergeable"
	MergeBlockerChecking         = "checking"
	MergeBlockerStatusChecks     = "status_checks"
	MergeBlockerApprovals        = "approvals"
	MergeBlockerRequestedChanges = "requested_changes"
	MergeBlockerReviewRequests   = "review_requests"
	MergeBlockerOutdatedBranch   = "outdated_branch"
	MergeBlockerProtectedFiles   = "protected_files"
	MergeBlockerSignature        = "signature"
	MergeBlockerDependency       = "dependency"
	MergeBlockerBranchProtection = "branch_protection"
)

// SnapshotMergeEligibility is the permission-checked actor-bound merge
// readiness of one pull request. It reuses the evaluator behind
// CheckPullMergeable and exposes its first typed blocker; the read
// performs no mutation and grants no mutation authority. BlockingReviewIDs
// carries the official rejecting review IDs behind a requested-changes
// blocker, bounded to the page limit. HeadTip/BaseTip bind the exact tips
// the evaluation observed.
type SnapshotMergeEligibility struct {
	PullNumber        string   `json:"pull_number"`
	HeadTip           string   `json:"head_tip,omitempty"`
	BaseTip           string   `json:"base_tip,omitempty"`
	Eligible          bool     `json:"eligible"`
	Blocker           string   `json:"blocker,omitempty"`
	BlockingReviewIDs []string `json:"blocking_review_ids,omitempty"`
	Visible           bool     `json:"visible"`
	HiddenReason      string   `json:"hidden_reason,omitempty"`
	Complete          bool     `json:"complete"`
}

// SnapshotCheck is the permission-checked view of one native commit status.
type SnapshotCheck struct {
	ID           string `json:"id"`
	Index        int64  `json:"index,omitempty"`
	SHA          string `json:"sha"`
	Context      string `json:"context,omitempty"`
	State        string `json:"state,omitempty"`
	CreatorID    string `json:"creator_id,omitempty"`
	CreatedUnix  int64  `json:"created_unix,omitempty"`
	UpdatedUnix  int64  `json:"updated_unix,omitempty"`
	Visible      bool   `json:"visible"`
	HiddenReason string `json:"hidden_reason,omitempty"`
	Complete     bool   `json:"complete"`
}

// SnapshotCheckSet carries one bounded check list for an exact commit.
type SnapshotCheckSet struct {
	SHA      string          `json:"sha"`
	Items    []SnapshotCheck `json:"items"`
	Total    int             `json:"total"`
	Complete bool            `json:"complete"`
}

// SnapshotRef is the permission-checked view of one native branch tip.
type SnapshotRef struct {
	Ref          string `json:"ref"`
	OID          string `json:"oid,omitempty"`
	Exists       bool   `json:"exists,omitempty"`
	Visible      bool   `json:"visible"`
	HiddenReason string `json:"hidden_reason,omitempty"`
	Complete     bool   `json:"complete"`
}

// SnapshotAncestry is one scalar reachability witness for exact Git objects.
// The host returns no graph or commit metadata.
type SnapshotAncestry struct {
	AncestorOID   string `json:"ancestor_oid"`
	DescendantOID string `json:"descendant_oid"`
	Reachable     bool   `json:"reachable"`
}

// NativeSnapshot is one evidence set answering a SnapshotRequest. Only
// requested families are present. The caller binds its bracketed native
// revision; the host never sets it.
type NativeSnapshot struct {
	Writers          map[string]bool               `json:"writers,omitempty"`
	RepositoryIssues *SnapshotRepositoryIssuesPage `json:"repository_issues,omitempty"`
	RepositoryID     string                        `json:"repository_id"`
	Issue            *SnapshotIssue                `json:"issue,omitempty"`
	Comments         *SnapshotCommentPage          `json:"comments,omitempty"`
	Dependencies     *SnapshotDependencyPage       `json:"dependencies,omitempty"`
	Pull             *SnapshotPull                 `json:"pull,omitempty"`
	Reviews          *SnapshotReviewPage           `json:"reviews,omitempty"`
	Threads          *SnapshotThreadPage           `json:"review_threads,omitempty"`
	Eligibility      *SnapshotMergeEligibility     `json:"merge_eligibility,omitempty"`
	Checks           *SnapshotCheckSet             `json:"checks,omitempty"`
	Refs             []SnapshotRef                 `json:"refs,omitempty"`
	Ancestry         *SnapshotAncestry             `json:"ancestry,omitempty"`
}

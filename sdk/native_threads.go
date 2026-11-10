package extensions

import (
	"context"
	"errors"
	"strconv"
	"unicode/utf8"
)

var (
	ErrThreadQuery     = errors.New("invalid thread query")
	ErrThreadChanged   = errors.New("thread placement changed")
	ErrThreadsTooLarge = errors.New("thread catalogue exceeds read limit")
)

// Without ThreadID, Cursor pages roots; with it, Cursor pages that root's replies.
type RepositoryThreadsRequest struct {
	Index    string `json:"index"`
	ThreadID string `json:"thread_id,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit"`
}

func ValidRepositoryThreadsRequest(q RepositoryThreadsRequest) bool {
	return positivePullID(q.Index) && (q.ThreadID == "" || positivePullID(q.ThreadID)) && (q.Cursor == "" || positivePullID(q.Cursor)) && q.Limit > 0 && q.Limit <= 20
}

type RepositoryThreadAnchor struct {
	Base      string `json:"base"`
	Head      string `json:"head"`
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
	Side      string `json:"side"`
	Scope     string `json:"scope"`
	StartLine int64  `json:"start_line"`
	EndLine   int64  `json:"end_line"`
}
type RepositoryThreadLocation struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
	Side   string `json:"side"`
	Line   int64  `json:"line"`
}
type RepositoryThreadComment struct {
	Comment RepositoryDiscussionComment `json:"comment"`
	Anchor  *RepositoryThreadAnchor     `json:"anchor"`
}
type RepositoryThread struct {
	ID               string                    `json:"id"`
	ReviewID         string                    `json:"review_id"`
	Anchor           *RepositoryThreadAnchor   `json:"anchor"`
	RecordedLocation *RepositoryThreadLocation `json:"recorded_location"`
	CurrentAnchor    *RepositoryThreadAnchor   `json:"current_anchor"`
	Outdated         *bool                     `json:"outdated"`
	Resolved         bool                      `json:"resolved"`
	Comments         []RepositoryThreadComment `json:"comments"`
	CommentsNext     string                    `json:"comments_next,omitempty"`
}
type RepositoryThreadPage struct {
	RepositoryID string             `json:"repository_id"`
	PullID       string             `json:"pull_id"`
	Index        string             `json:"index"`
	Items        []RepositoryThread `json:"items"`
	NextCursor   string             `json:"next_cursor,omitempty"`
}

func ValidRepositoryThreadAnchor(a *RepositoryThreadAnchor) bool {
	if a == nil {
		return true
	}
	if !ValidPullDiffCommit(a.Base) || !ValidPullDiffCommit(a.Head) || a.Side != "old" && a.Side != "new" || a.OldPath == "" && a.NewPath == "" || len(a.OldPath) > 4096 || len(a.NewPath) > 4096 || !utf8.ValidString(a.OldPath) || !utf8.ValidString(a.NewPath) || a.Side == "old" && a.OldPath == "" || a.Side == "new" && a.NewPath == "" {
		return false
	}
	return a.Scope == "file" && a.StartLine == 0 && a.EndLine == 0 || a.Scope == "lines" && a.StartLine > 0 && a.EndLine >= a.StartLine && a.EndLine <= 9007199254740991
}

func ValidRepositoryThreadPage(p *RepositoryThreadPage, repo string, q RepositoryThreadsRequest) bool {
	if p == nil || p.RepositoryID != repo || !positivePullID(p.PullID) || p.Index != q.Index || p.Items == nil || q.ThreadID == "" && len(p.Items) > q.Limit || q.ThreadID != "" && (len(p.Items) != 1 || p.Items[0].ID != q.ThreadID || p.NextCursor != "") {
		return false
	}
	last := ""
	bound := int64(0)
	if q.ThreadID == "" {
		bound, _ = strconv.ParseInt(q.Cursor, 10, 64)
	}
	for _, t := range p.Items {
		if !positivePullID(t.ID) || !positivePullID(t.ReviewID) || !ValidRepositoryThreadAnchor(t.Anchor) || !ValidRepositoryThreadAnchor(t.CurrentAnchor) || t.Comments == nil {
			return false
		}
		id, _ := strconv.ParseInt(t.ID, 10, 64)
		if id <= bound {
			return false
		}
		bound, last = id, t.ID
		if l := t.RecordedLocation; l != nil {
			if l.Path == "" || len(l.Path) > 4096 || !utf8.ValidString(l.Path) || l.Side != "old" && l.Side != "new" || l.Line < 1 || l.Line > 9007199254740991 || l.Commit != "" && !ValidPullDiffCommit(l.Commit) {
				return false
			}
		}
		limit := 10
		if q.ThreadID != "" {
			limit = q.Limit
		}
		if len(t.Comments) > limit {
			return false
		}
		seen := map[string]bool{}
		tail := ""
		for _, c := range t.Comments {
			if !positivePullID(c.Comment.ID) || seen[c.Comment.ID] || !ValidRepositoryThreadAnchor(c.Anchor) {
				return false
			}
			seen[c.Comment.ID] = true
			tail = c.Comment.ID
		}
		if t.CommentsNext != "" && (tail == "" || tail != t.CommentsNext) {
			return false
		}
	}
	return p.NextCursor == "" || last != "" && p.NextCursor == last
}

func (a Authority) RepositoryThreads(ctx context.Context, repo string, q RepositoryThreadsRequest) (RepositoryThreadPage, error) {
	if !positivePullID(repo) || !ValidRepositoryThreadsRequest(q) {
		return RepositoryThreadPage{}, ErrThreadQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryThreads, RepositoryID: repo, Threads: &q})
	if err != nil {
		return RepositoryThreadPage{}, err
	}
	return *r.Threads, nil
}

func threadsCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest: ErrThreadQuery,
		conflict:   ErrThreadChanged,
		tooLarge:   ErrThreadsTooLarge,
		notFound:   ErrRepositoryNotVisible,
	}).forStatus(status)
}

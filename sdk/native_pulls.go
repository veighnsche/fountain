package extensions

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// Index and Cursor are repository-local display numbers, never PR/issue row IDs.
type RepositoryPullsRequest struct {
	Index  string `json:"index,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	State  string `json:"state,omitempty"`
	Limit  int    `json:"limit"`
}

func ValidRepositoryPullsRequest(q RepositoryPullsRequest) bool {
	if q.Limit < 1 || q.Limit > 50 || q.State != "" && q.State != "open" && q.State != "closed" && q.State != "all" || q.Index != "" && (q.Cursor != "" || q.State != "" || q.Limit != 1) {
		return false
	}
	for _, raw := range []string{q.Index, q.Cursor} {
		if raw != "" && !positivePullID(raw) {
			return false
		}
	}
	return true
}

func positivePullID(raw string) bool {
	id, err := strconv.ParseInt(raw, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == raw
}

// A missing Head means the fork was deleted or its code is not readable. No
// private repository identity or branch name is returned in that case.
type RepositoryPullSide struct {
	RepositoryID string `json:"repository_id"`
	Owner        string `json:"owner"`
	Name         string `json:"name"`
	Branch       string `json:"branch"`
}
type RepositoryPull struct {
	ID         string                `json:"id"`
	Index      string                `json:"index"`
	Title      string                `json:"title"`
	Body       string                `json:"body"`
	Closed     bool                  `json:"closed"`
	Merged     bool                  `json:"merged"`
	Draft      bool                  `json:"draft"`
	Author     *RepositoryIssueActor `json:"author"`
	BaseBranch string                `json:"base_branch"`
	Head       *RepositoryPullSide   `json:"head"`
	CreatedAt  *time.Time            `json:"created_at"`
	UpdatedAt  *time.Time            `json:"updated_at"`
	MergedAt   *time.Time            `json:"merged_at"`
}
type RepositoryPullPage struct {
	RepositoryID string           `json:"repository_id"`
	Items        []RepositoryPull `json:"items"`
	NextCursor   string           `json:"next_cursor,omitempty"`
}

func ValidRepositoryPullPage(p *RepositoryPullPage, repository string, q RepositoryPullsRequest) bool {
	if p == nil || p.RepositoryID != repository || p.Items == nil || len(p.Items) > q.Limit || q.Index != "" && (len(p.Items) != 1 || p.NextCursor != "") {
		return false
	}
	bound, _ := strconv.ParseInt(q.Cursor, 10, 64)
	seen := map[string]bool{}
	last := ""
	for _, item := range p.Items {
		if !positivePullID(item.ID) || seen[item.ID] || !positivePullID(item.Index) || q.Index != "" && item.Index != q.Index {
			return false
		}
		index, _ := strconv.ParseInt(item.Index, 10, 64)
		if index <= bound || q.Index == "" && q.State != "all" && item.Closed != (q.State == "closed") {
			return false
		}
		if item.Head != nil && (!positivePullID(item.Head.RepositoryID) || item.Head.Owner == "" || item.Head.Name == "") {
			return false
		}
		seen[item.ID], bound, last = true, index, item.Index
	}
	return p.NextCursor == "" || last != "" && p.NextCursor == last
}

func (a Authority) RepositoryPulls(ctx context.Context, repository string, q RepositoryPullsRequest) (RepositoryPullPage, error) {
	if !positivePullID(repository) || !ValidRepositoryPullsRequest(q) {
		return RepositoryPullPage{}, errors.New("invalid pull request query")
	}
	result, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryPulls, RepositoryID: repository, Pulls: &q})
	if err != nil {
		return RepositoryPullPage{}, err
	}
	return *result.Pulls, nil
}

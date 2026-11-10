package extensions

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// RepositoryCommentsRequest selects ordinary discussion, not review/event rows.
// Subject is issue or pull; Index is a repository-local number, never a row ID.
type RepositoryCommentsRequest struct {
	Subject string `json:"subject"`
	Index   string `json:"index"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit"`
}

func ValidRepositoryCommentsRequest(r RepositoryCommentsRequest) bool {
	index, err := strconv.ParseInt(r.Index, 10, 64)
	if err != nil || index < 1 || !decimalID(r.Index) {
		return false
	}
	if r.Cursor != "" {
		cursor, err := strconv.ParseInt(r.Cursor, 10, 64)
		if err != nil || cursor < 1 || !decimalID(r.Cursor) {
			return false
		}
	}
	return (r.Subject == "issue" || r.Subject == "pull") && r.Limit > 0 && r.Limit <= 50
}

type RepositoryDiscussionComment struct {
	ID        string                `json:"id"`
	Author    *RepositoryIssueActor `json:"author"`
	Body      string                `json:"body"`
	CreatedAt *time.Time            `json:"created_at"`
	UpdatedAt *time.Time            `json:"updated_at"`
}
type RepositoryCommentPage struct {
	RepositoryID string                        `json:"repository_id"`
	Subject      string                        `json:"subject"`
	SubjectID    string                        `json:"subject_id"`
	Index        string                        `json:"index"`
	Items        []RepositoryDiscussionComment `json:"items"`
	NextCursor   string                        `json:"next_cursor,omitempty"`
}

func ValidRepositoryCommentPage(p *RepositoryCommentPage, repository string, q RepositoryCommentsRequest) bool {
	if p == nil || p.RepositoryID != repository || p.Subject != q.Subject || p.Index != q.Index || !decimalID(p.SubjectID) || p.Items == nil || len(p.Items) > q.Limit {
		return false
	}
	bound := uint64(0)
	if q.Cursor != "" {
		var err error
		bound, err = strconv.ParseUint(q.Cursor, 10, 64)
		if err != nil {
			return false
		}
	}
	last := ""
	for _, item := range p.Items {
		if !decimalID(item.ID) {
			return false
		}
		value, err := strconv.ParseUint(item.ID, 10, 64)
		if err != nil || value == 0 || value <= bound {
			return false
		}
		bound = value
		last = item.ID
	}
	return p.NextCursor == "" || last != "" && p.NextCursor == last
}

func (a Authority) RepositoryComments(ctx context.Context, repository string, q RepositoryCommentsRequest) (RepositoryCommentPage, error) {
	if !decimalID(repository) || !ValidRepositoryCommentsRequest(q) {
		return RepositoryCommentPage{}, errors.New("invalid discussion request")
	}
	result, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryComments, RepositoryID: repository, Comments: &q})
	if err != nil {
		return RepositoryCommentPage{}, err
	}
	return *result.Comments, nil
}

package extensions

import (
	"context"
	"errors"
	"time"
)

// RepositoryIssuePage is bounded issue context read as the admitted human.
// It supplies discussion context, not evidence authorizing execution.
type RepositoryIssuePage struct {
	RepositoryID string            `json:"repository_id"`
	Items        []RepositoryIssue `json:"items"`
	NextCursor   string            `json:"next_cursor,omitempty"`
}

type RepositoryIssue struct {
	ID           string                 `json:"id"`
	Index        string                 `json:"index"`
	Title        string                 `json:"title"`
	Body         string                 `json:"body"`
	Closed       bool                   `json:"closed"`
	BodyComplete bool                   `json:"body_complete"`
	Author       *RepositoryIssueActor  `json:"author"`
	Assignees    []RepositoryIssueActor `json:"assignees"`
	Labels       []RepositoryIssueLabel `json:"labels"`
	CreatedAt    *time.Time             `json:"created_at"`
	UpdatedAt    *time.Time             `json:"updated_at"`
	ClosedAt     *time.Time             `json:"closed_at"`
}

type RepositoryIssueLabel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Participants are identity metadata, not current admission/role facts.
type RepositoryIssueActor struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// RepositoryIssues lists open (default), closed or all issues, or reads one
// explicit issue of any state. The host derives access from live admission; no service
// credential, execution enrollment or caller-supplied actor is involved.
func (a Authority) RepositoryIssues(ctx context.Context, repository, cursor, index string, limit int, completeBodies bool, state string) (RepositoryIssuePage, error) {
	if !decimalID(repository) || limit < 1 || limit > 50 || cursor != "" && !decimalID(cursor) || index != "" && (!decimalID(index) || cursor != "") {
		return RepositoryIssuePage{}, errors.New("invalid repository issue request")
	}
	if state != "" && state != "open" && state != "closed" && state != "all" || index != "" && state != "" {
		return RepositoryIssuePage{}, errors.New("invalid issue state selector")
	}
	result, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryIssues, RepositoryID: repository, Cursor: cursor, Query: index, Limit: limit, CompleteIssueBodies: completeBodies, IssueState: state})
	if err != nil {
		return RepositoryIssuePage{}, err
	}
	return *result.Issues, nil
}

package extensions

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"
)

var (
	ErrPullCommitsQuery   = errors.New("invalid pull commits query")
	ErrPullCommitsChanged = errors.New("pull commits changed")
)

type RepositoryPullCommitsRequest struct {
	Index  string `json:"index"`
	Head   string `json:"head"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

func ValidRepositoryPullCommitsRequest(q RepositoryPullCommitsRequest) bool {
	return positivePullID(q.Index) && ValidPullDiffCommit(q.Head) && q.Limit > 0 && q.Limit <= 20 && len(q.Cursor) <= 4096
}

type RepositoryPullCommit struct {
	Commit     string    `json:"commit"`
	Summary    string    `json:"summary"`
	Author     string    `json:"author"`
	AuthoredAt time.Time `json:"authored_at"`
}
type RepositoryPullCommits struct {
	RepositoryID string                 `json:"repository_id"`
	PullID       string                 `json:"pull_id"`
	Index        string                 `json:"index"`
	Head         string                 `json:"head"`
	Items        []RepositoryPullCommit `json:"items"`
	NextCursor   string                 `json:"next_cursor,omitempty"`
}

func ValidRepositoryPullCommits(p *RepositoryPullCommits, repository string, q RepositoryPullCommitsRequest) bool {
	if p == nil || p.RepositoryID != repository || !positivePullID(p.PullID) || p.Index != q.Index || p.Head != q.Head || p.Items == nil || len(p.Items) > q.Limit || len(p.NextCursor) > 4096 || p.NextCursor != "" && len(p.Items) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range p.Items {
		if !ValidPullDiffCommit(item.Commit) || seen[item.Commit] || len(item.Summary) > 4096 || !utf8.ValidString(item.Summary) || len(item.Author) > 1024 || !utf8.ValidString(item.Author) || item.AuthoredAt.IsZero() {
			return false
		}
		seen[item.Commit] = true
	}
	return true
}

func (a Authority) RepositoryPullCommits(ctx context.Context, repository string, q RepositoryPullCommitsRequest) (RepositoryPullCommits, error) {
	if !positivePullID(repository) || !ValidRepositoryPullCommitsRequest(q) {
		return RepositoryPullCommits{}, ErrPullCommitsQuery
	}
	result, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryPullCommits, RepositoryID: repository, PullCommits: &q})
	if err != nil {
		return RepositoryPullCommits{}, err
	}
	return *result.PullCommits, nil
}

func pullCommitsCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest: ErrPullCommitsQuery,
		conflict:   ErrPullCommitsChanged,
		notFound:   ErrRepositoryNotVisible,
	}).forStatus(status)
}

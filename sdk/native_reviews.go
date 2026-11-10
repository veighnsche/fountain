package extensions

import (
	"context"
	"errors"
	"strconv"
	"time"
)

type RepositoryReviewsRequest struct {
	Index    string `json:"index"`
	ReviewID string `json:"review_id,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
	Limit    int    `json:"limit"`
}

func ValidRepositoryReviewsRequest(q RepositoryReviewsRequest) bool {
	return positivePullID(q.Index) && q.Limit > 0 && q.Limit <= 50 &&
		(q.ReviewID == "" || positivePullID(q.ReviewID) && q.Cursor == "" && q.Limit == 1) &&
		(q.Cursor == "" || positivePullID(q.Cursor))
}

type RepositoryReview struct {
	ID          string                `json:"id"`
	Author      *RepositoryIssueActor `json:"author"`
	Commit      string                `json:"commit"`
	State       string                `json:"state"`
	Body        string                `json:"body"`
	Stale       bool                  `json:"stale"`
	Official    bool                  `json:"official"`
	SubmittedAt *time.Time            `json:"submitted_at"`
}
type RepositoryReviewPage struct {
	RepositoryID string             `json:"repository_id"`
	PullID       string             `json:"pull_id"`
	Index        string             `json:"index"`
	Items        []RepositoryReview `json:"items"`
	NextCursor   string             `json:"next_cursor,omitempty"`
}

func ValidRepositoryReviewPage(p *RepositoryReviewPage, repository string, q RepositoryReviewsRequest) bool {
	if p == nil || p.RepositoryID != repository || !positivePullID(p.PullID) || p.Index != q.Index || p.Items == nil || len(p.Items) > q.Limit || q.ReviewID != "" && (len(p.Items) != 1 || p.NextCursor != "") {
		return false
	}
	bound, _ := strconv.ParseInt(q.Cursor, 10, 64)
	last := ""
	for _, r := range p.Items {
		if !positivePullID(r.ID) || r.Commit != "" && !ValidPullDiffCommit(r.Commit) || q.ReviewID != "" && r.ID != q.ReviewID {
			return false
		}
		id, _ := strconv.ParseInt(r.ID, 10, 64)
		if id <= bound {
			return false
		}
		bound, last = id, r.ID
		switch r.State {
		case "pending", "approved", "changes_requested", "commented", "dismissed", "unknown":
		default:
			return false
		}
		if r.State == "pending" && r.SubmittedAt != nil {
			return false
		}
	}
	return p.NextCursor == "" || last != "" && p.NextCursor == last
}

func (a Authority) RepositoryReviews(ctx context.Context, repository string, q RepositoryReviewsRequest) (RepositoryReviewPage, error) {
	if !positivePullID(repository) || !ValidRepositoryReviewsRequest(q) {
		return RepositoryReviewPage{}, errors.New("invalid review request")
	}
	result, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRepositoryReviews, RepositoryID: repository, Reviews: &q})
	if err != nil {
		return RepositoryReviewPage{}, err
	}
	return *result.Reviews, nil
}

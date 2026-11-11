package extensions

import (
	"context"
	"net/http"
	"strconv"
	"time"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	user_model "forgejo.org/models/user"

	"xorm.io/builder"
)

// The same draft rule as Fountain's native review API, evaluated from a fresh
// native user. A caller's actor/admin selectors never supply this authority.
func readableReviewCondition(issue int64, user *user_model.User) builder.Cond {
	cond := builder.Eq{"issue_id": issue}.And(builder.Neq{"type": issues_model.ReviewTypeRequest})
	if !user.IsAdmin {
		cond = cond.And(builder.Or(builder.Neq{"type": issues_model.ReviewTypePending}, builder.Eq{"reviewer_id": user.ID}))
	}
	return cond
}

func resolveRepositoryReviews(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryReviewPage, int) {
	q := *request.Reviews
	issue, id, status := discussionParent(ctx, admission, request.RepositoryID, extension.RepositoryCommentsRequest{Subject: "pull", Index: q.Index, Limit: 1})
	if status != http.StatusOK {
		return nil, status
	}
	if issue.PullRequest == nil || issue.PullRequest.IssueID != issue.ID || issue.PullRequest.BaseRepoID != issue.RepoID || issue.PullRequest.Index != issue.Index {
		return nil, http.StatusServiceUnavailable
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	cond := readableReviewCondition(issue.ID, user)
	if q.ReviewID != "" {
		cond = cond.And(builder.Eq{"id": mustParsePositiveID(q.ReviewID)})
	}
	if q.Cursor != "" {
		cond = cond.And(builder.Gt{"id": mustParsePositiveID(q.Cursor)})
	}
	var reviews []*issues_model.Review
	if err := db.GetEngine(ctx).Where(cond).Asc("id").Limit(q.Limit + 1).Find(&reviews); err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if q.ReviewID != "" && len(reviews) != 1 {
		return nil, http.StatusNotFound
	}
	page := &extension.RepositoryReviewPage{RepositoryID: request.RepositoryID, PullID: id, Index: q.Index, Items: []extension.RepositoryReview{}}
	if len(reviews) > q.Limit {
		reviews = reviews[:q.Limit]
		page.NextCursor = strconv.FormatInt(reviews[len(reviews)-1].ID, 10)
	}
	ids := make([]int64, 0, len(reviews))
	for _, review := range reviews {
		ids = append(ids, review.ID)
		if err := review.LoadReviewer(ctx); err != nil {
			return nil, http.StatusServiceUnavailable
		}
		item := extension.RepositoryReview{ID: strconv.FormatInt(review.ID, 10), State: "unknown", Body: review.Content, Stale: review.Stale, Official: review.Official}
		switch review.Type {
		case issues_model.ReviewTypePending:
			item.State = "pending"
		case issues_model.ReviewTypeApprove:
			item.State = "approved"
		case issues_model.ReviewTypeComment:
			item.State = "commented"
		case issues_model.ReviewTypeReject:
			item.State = "changes_requested"
		}
		if review.Dismissed && review.Type != issues_model.ReviewTypePending {
			item.State = "dismissed"
		}
		if extension.ValidPullDiffCommit(review.CommitID) {
			item.Commit = review.CommitID
		}
		if review.Reviewer != nil && review.Reviewer.ID > 0 {
			item.Author = &extension.RepositoryIssueActor{ID: strconv.FormatInt(review.Reviewer.ID, 10), Username: review.Reviewer.Name}
		}
		// This follows native API's CreatedUnix convention for submitted reviews.
		// A draft's creation time must not become a submission timestamp.
		if review.Type != issues_model.ReviewTypePending && review.CreatedUnix > 0 {
			value := time.Unix(int64(review.CreatedUnix), 0).UTC()
			item.SubmittedAt = &value
		}
		page.Items = append(page.Items, item)
	}
	after, afterID, status := discussionParent(ctx, admission, request.RepositoryID, extension.RepositoryCommentsRequest{Subject: "pull", Index: q.Index, Limit: 1})
	if status != http.StatusOK {
		return nil, status
	}
	if afterID != id || after.ID != issue.ID {
		return nil, http.StatusServiceUnavailable
	}
	current, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	// A returned public review may become another user's pending review while
	// I/O is in progress. Reload visibility; a cached draft check can leak it.
	if len(ids) > 0 {
		count, err := db.GetEngine(ctx).Where(readableReviewCondition(after.ID, current)).In("id", ids).Count(new(issues_model.Review))
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		if count != int64(len(ids)) {
			return nil, http.StatusNotFound
		}
	}
	return page, http.StatusOK
}

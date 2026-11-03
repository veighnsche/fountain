package extensions

import (
	"context"
	"net/http"
	"strconv"
	"time"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"xorm.io/builder"
)

// Load the current parent and its own unit permission for each bracket side.
// PR discussion is not authorized merely by access to ordinary repository issues.
func discussionParent(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryCommentsRequest) (*issues_model.Issue, string, int) {
	if _, status := resolveRepository(ctx, admission, repository); status != http.StatusOK {
		return nil, "", status
	}
	return issueOrPullParent(ctx, admission, repository, q)
}

// Metadata editing needs the subject's own issue/PR unit, independently of
// source-code access. Code-dependent callers retain discussionParent's gate.
func issueOrPullParent(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryCommentsRequest) (*issues_model.Issue, string, int) {
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, "", status
	}
	repo, err := repo_model.GetRepositoryByID(ctx, mustParsePositiveID(repository))
	if repo_model.IsErrRepoNotExist(err) {
		return nil, "", http.StatusNotFound
	}
	if err != nil {
		return nil, "", http.StatusServiceUnavailable
	}
	issue, err := issues_model.GetIssueByIndex(ctx, repo.ID, mustParsePositiveID(q.Index))
	if issues_model.IsErrIssueNotExist(err) || err == nil && issue.IsPull != (q.Subject == "pull") {
		return nil, "", http.StatusNotFound
	}
	if err != nil {
		return nil, "", http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, user)
	if err != nil {
		return nil, "", http.StatusServiceUnavailable
	}
	kind := unit.TypeIssues
	if issue.IsPull {
		kind = unit.TypePullRequests
	}
	if !permission.CanRead(kind) {
		return nil, "", http.StatusNotFound
	}
	id := issue.ID
	if issue.IsPull {
		if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil {
			return nil, "", http.StatusServiceUnavailable
		}
		id = issue.PullRequest.ID
	}
	if id < 1 {
		return nil, "", http.StatusServiceUnavailable
	}
	return issue, strconv.FormatInt(id, 10), http.StatusOK
}
func resolveRepositoryComments(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryCommentPage, int) {
	q := *request.Comments
	issue, id, status := discussionParent(ctx, admission, request.RepositoryID, q)
	if status != http.StatusOK {
		return nil, status
	}
	bound := int64(0)
	if q.Cursor != "" {
		bound = mustParsePositiveID(q.Cursor)
	}
	var comments []*issues_model.Comment
	err := db.GetEngine(ctx).Where(builder.Eq{"issue_id": issue.ID, "type": issues_model.CommentTypeComment}).And(builder.Gt{"id": bound}).Asc("id").Limit(q.Limit + 1).Find(&comments)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	page := &extension.RepositoryCommentPage{RepositoryID: request.RepositoryID, Subject: q.Subject, SubjectID: id, Index: q.Index, Items: []extension.RepositoryDiscussionComment{}}
	if len(comments) > q.Limit {
		comments = comments[:q.Limit]
		page.NextCursor = strconv.FormatInt(comments[len(comments)-1].ID, 10)
	}
	for _, comment := range comments {
		if err := comment.LoadPoster(ctx); err != nil && !user_model.IsErrUserNotExist(err) {
			return nil, http.StatusServiceUnavailable
		}
		item := extension.RepositoryDiscussionComment{ID: strconv.FormatInt(comment.ID, 10), Body: comment.Content}
		if comment.Poster != nil && comment.Poster.ID > 0 {
			item.Author = &extension.RepositoryIssueActor{ID: strconv.FormatInt(comment.Poster.ID, 10), Username: comment.Poster.Name}
		}
		if comment.CreatedUnix > 0 {
			value := time.Unix(int64(comment.CreatedUnix), 0).UTC()
			item.CreatedAt = &value
		}
		if comment.UpdatedUnix > 0 {
			value := time.Unix(int64(comment.UpdatedUnix), 0).UTC()
			item.UpdatedAt = &value
		}
		page.Items = append(page.Items, item)
	}
	_, after, status := discussionParent(ctx, admission, request.RepositoryID, q)
	if status != http.StatusOK {
		return nil, status
	}
	if after != id {
		return nil, http.StatusServiceUnavailable
	}
	return page, http.StatusOK
}

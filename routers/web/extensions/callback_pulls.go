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

	"xorm.io/builder"
)

func readablePullRepository(ctx context.Context, admission *callbackAdmission, repository string) int {
	if _, status := resolveRepository(ctx, admission, repository); status != http.StatusOK {
		return status
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return status
	}
	repo, err := repo_model.GetRepositoryByID(ctx, mustParsePositiveID(repository))
	if err != nil {
		return http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, user)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	if !permission.CanRead(unit.TypePullRequests) {
		return http.StatusNotFound
	}
	return http.StatusOK
}

func resolveRepositoryPulls(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryPullPage, int) {
	if status := readablePullRepository(ctx, admission, request.RepositoryID); status != http.StatusOK {
		return nil, status
	}
	q := *request.Pulls
	var issues []*issues_model.Issue
	if q.Index != "" {
		issue, err := issues_model.GetIssueByIndex(ctx, mustParsePositiveID(request.RepositoryID), mustParsePositiveID(q.Index))
		if issues_model.IsErrIssueNotExist(err) || err == nil && !issue.IsPull {
			return nil, http.StatusNotFound
		}
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		issues = append(issues, issue)
	} else {
		condition := builder.Eq{"repo_id": mustParsePositiveID(request.RepositoryID), "is_pull": true}
		if q.State != "all" {
			condition["is_closed"] = q.State == "closed"
		}
		bound := int64(0)
		if q.Cursor != "" {
			bound = mustParsePositiveID(q.Cursor)
		}
		if err := db.GetEngine(ctx).Where(condition).And(builder.Gt{"`index`": bound}).Asc("index").Limit(q.Limit + 1).Find(&issues); err != nil {
			return nil, http.StatusServiceUnavailable
		}
	}
	page := &extension.RepositoryPullPage{RepositoryID: request.RepositoryID, Items: []extension.RepositoryPull{}}
	if len(issues) > q.Limit {
		issues = issues[:q.Limit]
		page.NextCursor = strconv.FormatInt(issues[len(issues)-1].Index, 10)
	}
	for _, issue := range issues {
		if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil {
			return nil, http.StatusServiceUnavailable
		}
		pr := issue.PullRequest
		if pr.ID < 1 || pr.IssueID != issue.ID || pr.Index != issue.Index || pr.BaseRepoID != issue.RepoID {
			return nil, http.StatusServiceUnavailable
		}
		if err := issue.LoadPoster(ctx); err != nil {
			return nil, http.StatusServiceUnavailable
		}
		pr.Issue = issue
		item := extension.RepositoryPull{ID: strconv.FormatInt(pr.ID, 10), Index: strconv.FormatInt(issue.Index, 10), Title: issue.Title, Body: issue.Content, Closed: issue.IsClosed, Merged: pr.HasMerged, Draft: pr.IsWorkInProgress(ctx), BaseBranch: pr.BaseBranch}
		if issue.Poster != nil && issue.Poster.ID > 0 {
			item.Author = &extension.RepositoryIssueActor{ID: strconv.FormatInt(issue.Poster.ID, 10), Username: issue.Poster.Name}
		}
		if issue.CreatedUnix > 0 {
			value := time.Unix(int64(issue.CreatedUnix), 0).UTC()
			item.CreatedAt = &value
		}
		if issue.UpdatedUnix > 0 {
			value := time.Unix(int64(issue.UpdatedUnix), 0).UTC()
			item.UpdatedAt = &value
		}
		if pr.HasMerged && pr.MergedUnix > 0 {
			value := time.Unix(int64(pr.MergedUnix), 0).UTC()
			item.MergedAt = &value
		}
		if pr.HeadRepoID > 0 {
			head, status := resolveRepository(ctx, admission, strconv.FormatInt(pr.HeadRepoID, 10))
			switch status {
			case http.StatusOK:
				item.Head = &extension.RepositoryPullSide{RepositoryID: head.ID, Owner: head.Owner, Name: head.Name, Branch: pr.HeadBranch}
			case http.StatusNotFound: // A deleted or inaccessible head does not hide the visible PR.
			default:
				return nil, status
			}
		}
		page.Items = append(page.Items, item)
	}
	// Reload repository/unit permissions after all metadata and head reads.
	if status := readablePullRepository(ctx, admission, request.RepositoryID); status != http.StatusOK {
		return nil, status
	}
	return page, http.StatusOK
}

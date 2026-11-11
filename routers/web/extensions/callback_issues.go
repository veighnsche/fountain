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
	"forgejo.org/modules/log"

	"xorm.io/builder"
)

func resolveRepositoryIssues(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryIssuePage, int) {
	if _, status := resolveIssueRepository(ctx, admission, request.RepositoryID); status != http.StatusOK {
		return nil, status
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	id := mustParsePositiveID(request.RepositoryID)
	repository, err := repo_model.GetRepositoryByID(ctx, id)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repository, user)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if !permission.CanRead(unit.TypeIssues) {
		return nil, http.StatusNotFound
	}
	page := &extension.RepositoryIssuePage{RepositoryID: request.RepositoryID, Items: []extension.RepositoryIssue{}}
	var issues []*issues_model.Issue
	if request.Query != "" {
		issue, err := issues_model.GetIssueByIndex(ctx, id, mustParsePositiveID(request.Query))
		if issues_model.IsErrIssueNotExist(err) || err == nil && issue.IsPull {
			return nil, http.StatusNotFound
		}
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		issues = append(issues, issue)
	} else {
		bound := int64(0)
		if request.Cursor != "" {
			bound = mustParsePositiveID(request.Cursor)
		}
		condition := builder.Eq{"repo_id": id, "is_pull": false}
		if request.IssueState != "all" {
			condition["is_closed"] = request.IssueState == "closed"
		}
		err := db.GetEngine(ctx).Where(condition).
			And(builder.Gt{"`index`": bound}).Asc("index").Limit(request.Limit + 1).Find(&issues)
		if err != nil {
			log.Error("Read admitted repository issue context: %v", err)
			return nil, http.StatusServiceUnavailable
		}
		if len(issues) > request.Limit {
			issues = issues[:request.Limit]
			page.NextCursor = strconv.FormatInt(issues[len(issues)-1].Index, 10)
		}
	}
	for _, issue := range issues {
		body := []rune(issue.Content)
		complete := true
		if !request.CompleteIssueBodies && len(body) > 4000 {
			body = body[:4000]
			complete = false
		}
		item := extension.RepositoryIssue{ID: strconv.FormatInt(issue.ID, 10), Index: strconv.FormatInt(issue.Index, 10), Title: issue.Title, Body: string(body), Closed: issue.IsClosed, BodyComplete: complete}
		if request.CompleteIssueBodies {
			if err := issue.LoadPoster(ctx); err != nil {
				return nil, http.StatusServiceUnavailable
			}
			if err := issue.LoadAssignees(ctx); err != nil {
				return nil, http.StatusServiceUnavailable
			}
			if err := issue.LoadLabels(ctx); err != nil {
				return nil, http.StatusServiceUnavailable
			}
			item.Assignees = []extension.RepositoryIssueActor{}
			item.Labels = []extension.RepositoryIssueLabel{}
			if issue.Poster != nil && issue.Poster.ID > 0 {
				item.Author = &extension.RepositoryIssueActor{ID: strconv.FormatInt(issue.Poster.ID, 10), Username: issue.Poster.Name}
			}
			for _, actor := range issue.Assignees {
				if actor.ID > 0 {
					item.Assignees = append(item.Assignees, extension.RepositoryIssueActor{ID: strconv.FormatInt(actor.ID, 10), Username: actor.Name})
				}
			}
			for _, label := range issue.Labels {
				item.Labels = append(item.Labels, extension.RepositoryIssueLabel{ID: strconv.FormatInt(label.ID, 10), Name: label.Name, Color: label.Color})
			}
			if issue.CreatedUnix > 0 {
				value := time.Unix(int64(issue.CreatedUnix), 0).UTC()
				item.CreatedAt = &value
			}
			if issue.UpdatedUnix > 0 {
				value := time.Unix(int64(issue.UpdatedUnix), 0).UTC()
				item.UpdatedAt = &value
			}
			if issue.ClosedUnix > 0 {
				value := time.Unix(int64(issue.ClosedUnix), 0).UTC()
				item.ClosedAt = &value
			}
		}
		page.Items = append(page.Items, item)
	}
	// Session and repository visibility must still hold after native I/O.
	if _, status := resolveIssueRepository(ctx, admission, request.RepositoryID); status != http.StatusOK {
		return nil, status
	}
	current, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	repository, err = repo_model.GetRepositoryByID(ctx, id)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	permission, err = access_model.GetUserRepoPermission(ctx, repository, current)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if !permission.CanRead(unit.TypeIssues) {
		return nil, http.StatusNotFound
	}
	return page, http.StatusOK
}

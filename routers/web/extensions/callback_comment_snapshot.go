package extensions

import (
	"context"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
)

// Bracket current human/parent facts with the native writer revision. An idle
// revision is a future CAS precondition, never a durable permission grant.
func resolveCommentSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryCommentSnapshot, int) {
	first, revision, status := observeCallbackSnapshot(ctx, func(ctx context.Context) (*extension.RepositoryCommentSnapshot, int) {
		return readCommentSnapshot(ctx, admission, request.RepositoryID, *request.CommentSnapshot)
	}, equalSnapshot[extension.RepositoryCommentSnapshot], nil, []int{http.StatusNotFound})
	if status != http.StatusOK {
		return nil, status
	}
	first.NativeRevision = revision
	return first, http.StatusOK
}

func readCommentSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryCommentSnapshotRequest) (*extension.RepositoryCommentSnapshot, int) {
	issue, subjectID, status := discussionParent(ctx, admission, repository, extension.RepositoryCommentsRequest{Subject: q.Subject, Index: q.Index})
	if status != http.StatusOK {
		return nil, status
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	repo, err := repo_model.GetRepositoryByID(ctx, issue.RepoID)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, user)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	facts := issues_model.ReadDiscussionActionFacts(ctx, repo, issue, user, permission)
	if !facts.Readable {
		return nil, http.StatusNotFound
	}
	action := discussionCommentCapability(facts, q.Subject)
	return &extension.RepositoryCommentSnapshot{
		RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10),
		Subject: q.Subject, SubjectID: subjectID, IssueID: strconv.FormatInt(issue.ID, 10), Index: q.Index, Action: action,
	}, http.StatusOK
}

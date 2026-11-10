package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
)

func resolveEditSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryEditSnapshot, int) {
	first, revision, status := observeCallbackSnapshot(ctx, func(ctx context.Context) (*extension.RepositoryEditSnapshot, int) {
		return readEditSnapshot(ctx, admission, request.RepositoryID, *request.EditSnapshot)
	}, equalSnapshot[extension.RepositoryEditSnapshot], nil, []int{http.StatusNotFound})
	if status != http.StatusOK {
		return nil, status
	}
	first.NativeRevision = revision
	return first, http.StatusOK
}

func readEditSnapshot(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryEditSnapshotRequest) (*extension.RepositoryEditSnapshot, int) {
	issue, subjectID, status := issueOrPullParent(ctx, admission, repository, extension.RepositoryCommentsRequest{Subject: q.Subject, Index: q.Index})
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
	titleAction, bodyAction := discussionEditCapabilities(facts, q.Subject)

	digest := sha256.Sum256([]byte(issue.Content))
	return &extension.RepositoryEditSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), Subject: q.Subject, SubjectID: subjectID, IssueID: strconv.FormatInt(issue.ID, 10), Index: q.Index, Title: issue.Title, BodyDigest: hex.EncodeToString(digest[:]), ContentVersion: int64(issue.ContentVersion), TitleAction: titleAction, BodyAction: bodyAction}, http.StatusOK
}

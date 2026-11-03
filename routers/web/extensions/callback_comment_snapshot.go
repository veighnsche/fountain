package extensions

import (
	"context"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

// Bracket current human/parent facts with the native writer revision. An idle
// revision is a future CAS precondition, never a durable permission grant.
func resolveCommentSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryCommentSnapshot, int) {
	before, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil || !before.Idle {
		return nil, http.StatusServiceUnavailable
	}
	first, status := readCommentSnapshot(ctx, admission, request.RepositoryID, *request.CommentSnapshot)
	if status != http.StatusOK {
		return nil, status
	}
	second, status := readCommentSnapshot(ctx, admission, request.RepositoryID, *request.CommentSnapshot)
	if status == http.StatusNotFound {
		return nil, http.StatusConflict
	}
	if status != http.StatusOK {
		return nil, status
	}
	after, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if !after.Idle || after.Revision != before.Revision || *first != *second {
		return nil, http.StatusConflict
	}
	first.NativeRevision = before.Revision
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
	kind := unit.TypeIssues
	if issue.IsPull {
		kind = unit.TypePullRequests
	}
	if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
		return nil, http.StatusNotFound
	}
	action := capabilityFact("allowed", q.Subject+"_comment")
	if issue.IsLocked && !permission.CanWriteIssuesOrPulls(issue.IsPull) && !user.IsAdmin {
		action = capabilityFact("denied", q.Subject+"_locked")
	}
	if repo.IsArchived {
		action = capabilityFact("denied", "repository_archived")
	}
	return &extension.RepositoryCommentSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10),
		Subject: q.Subject, SubjectID: subjectID, IssueID: strconv.FormatInt(issue.ID, 10), Index: q.Index, Action: action}, http.StatusOK
}

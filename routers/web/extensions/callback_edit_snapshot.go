package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

func resolveEditSnapshot(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryEditSnapshot, int) {
	before, err := nativeoperation_service.Default().ReadNativeRevision(ctx)
	if err != nil || !before.Idle {
		return nil, http.StatusServiceUnavailable
	}
	first, status := readEditSnapshot(ctx, admission, request.RepositoryID, *request.EditSnapshot)
	if status != http.StatusOK {
		return nil, status
	}
	second, status := readEditSnapshot(ctx, admission, request.RepositoryID, *request.EditSnapshot)
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
	kind := unit.TypeIssues
	if issue.IsPull {
		kind = unit.TypePullRequests
	}
	if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
		return nil, http.StatusNotFound
	}
	// Match each native route: IsPoster also excludes an imported original
	// author for title edits, while UpdateIssueContent compares PosterID directly.
	// A comment lock does not take away the author's title/description edit right.
	titleAction := capabilityFact("denied", q.Subject+"_edit_denied")
	bodyAction := capabilityFact("denied", q.Subject+"_edit_denied")
	if issue.IsPoster(user.ID) || permission.CanWriteIssuesOrPulls(issue.IsPull) {
		titleAction = capabilityFact("allowed", q.Subject+"_author_or_writer")
	}
	if issue.PosterID == user.ID || permission.CanWriteIssuesOrPulls(issue.IsPull) {
		bodyAction = capabilityFact("allowed", q.Subject+"_poster_or_writer")
	}
	if repo.IsArchived {
		titleAction, bodyAction = capabilityFact("denied", "repository_archived"), capabilityFact("denied", "repository_archived")
	}
	digest := sha256.Sum256([]byte(issue.Content))
	return &extension.RepositoryEditSnapshot{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), Subject: q.Subject, SubjectID: subjectID, IssueID: strconv.FormatInt(issue.ID, 10), Index: q.Index, Title: issue.Title, BodyDigest: hex.EncodeToString(digest[:]), ContentVersion: int64(issue.ContentVersion), TitleAction: titleAction, BodyAction: bodyAction}, http.StatusOK
}

package extensions

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	extension "forgejo.org/extension-sdk"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
)

func pullCommitsCursor(repository, pull, base, head string, offset int) string {
	return strings.Join([]string{repository, pull, base, head, strconv.Itoa(offset)}, ".")
}

func resolveRepositoryPullCommits(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryPullCommits, int) {
	q := *request.PullCommits
	pr, status := pullFilesParent(ctx, admission, request.RepositoryID, q.Index)
	if status != http.StatusOK {
		return nil, status
	}
	repository, err := repo_model.GetRepositoryByID(ctx, pr.BaseRepoID)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	native, err := git.OpenRepository(ctx, repository.RepoPath())
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	defer native.Close()
	base := pr.MergeBase
	head, err := native.GetRefCommitID(pr.GetGitRefName())
	if err != nil || !extension.ValidPullDiffCommit(base) || !extension.ValidPullDiffCommit(head) {
		return nil, http.StatusServiceUnavailable
	}
	if head != q.Head {
		return nil, http.StatusConflict
	}
	baseCommit, err := native.GetCommit(base)
	if err != nil || baseCommit.ID.String() != base {
		return nil, http.StatusServiceUnavailable
	}
	headCommit, err := native.GetCommit(head)
	if err != nil || headCommit.ID.String() != head {
		return nil, http.StatusServiceUnavailable
	}
	if base != head {
		ancestor, err := headCommit.HasPreviousCommit(baseCommit.ID)
		if err != nil || !ancestor {
			return nil, http.StatusServiceUnavailable
		}
	}
	id := strconv.FormatInt(pr.ID, 10)
	offset := 0
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ".")
		if len(parts) != 5 {
			return nil, http.StatusBadRequest
		}
		offset, err = strconv.Atoi(parts[4])
		if err != nil || offset < 1 || offset > 1_000_000 || q.Cursor != pullCommitsCursor(request.RepositoryID, id, base, head, offset) {
			return nil, http.StatusBadRequest
		}
	}
	output := &pullDiffBuffer{limit: 2048}
	err = git.NewCommand(ctx, "rev-list").AddOptionFormat("--max-count=%d", q.Limit+1).AddOptionFormat("--skip=%d", offset).AddDynamicArguments(base + ".." + head).Run(&git.RunOpts{Dir: native.Path, Stdout: output})
	if err != nil || output.exceeded {
		return nil, http.StatusServiceUnavailable
	}
	ids := strings.Fields(string(output.Bytes()))
	if len(ids) > q.Limit+1 || offset > 0 && len(ids) == 0 {
		return nil, http.StatusBadRequest
	}
	more := len(ids) > q.Limit
	if more {
		ids = ids[:q.Limit]
	}
	page := &extension.RepositoryPullCommits{RepositoryID: request.RepositoryID, PullID: id, Index: q.Index, Head: head, Items: make([]extension.RepositoryPullCommit, 0, len(ids))}
	for _, oid := range ids {
		if !extension.ValidPullDiffCommit(oid) {
			return nil, http.StatusServiceUnavailable
		}
		commit, err := native.GetCommit(oid)
		if err != nil || commit.ID.String() != oid || commit.Author == nil || commit.Author.When.IsZero() {
			return nil, http.StatusServiceUnavailable
		}
		summary, author := commit.Summary(), commit.Author.Name
		if len(summary) > 4096 || !utf8.ValidString(summary) || len(author) > 1024 || !utf8.ValidString(author) {
			return nil, http.StatusServiceUnavailable
		}
		page.Items = append(page.Items, extension.RepositoryPullCommit{Commit: oid, Summary: summary, Author: author, AuthoredAt: commit.Author.When})
	}
	if more {
		page.NextCursor = pullCommitsCursor(request.RepositoryID, id, base, head, offset+len(ids))
	}
	after, status := pullFilesParent(ctx, admission, request.RepositoryID, q.Index)
	if status != http.StatusOK {
		return nil, status
	}
	afterHead, err := native.GetRefCommitID(after.GetGitRefName())
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if after.ID != pr.ID || after.MergeBase != base || afterHead != head || after.HeadRepoID != pr.HeadRepoID || after.HeadBranch != pr.HeadBranch || after.BaseBranch != pr.BaseBranch {
		return nil, http.StatusConflict
	}
	return page, http.StatusOK
}

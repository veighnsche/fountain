package extensions

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	extension "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
)

func pullFilesParent(ctx context.Context, admission *callbackAdmission, repository, index string) (*issues_model.PullRequest, int) {
	issue, id, status := discussionParent(ctx, admission, repository, extension.RepositoryCommentsRequest{Subject: "pull", Index: index, Limit: 1})
	if status != http.StatusOK {
		return nil, status
	}
	pr := issue.PullRequest
	if pr == nil || strconv.FormatInt(pr.ID, 10) != id || pr.IssueID != issue.ID || pr.BaseRepoID != issue.RepoID || pr.Index != issue.Index {
		return nil, http.StatusServiceUnavailable
	}
	return pr, http.StatusOK
}

type pullComparison struct {
	native                        *git.Repository
	repository, index, base, head string
	pullID, headRepositoryID      int64
	headBranch, baseBranch        string
}

func openPullComparison(ctx context.Context, admission *callbackAdmission, repositoryID, index string) (*pullComparison, int) {
	pr, status := pullFilesParent(ctx, admission, repositoryID, index)
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
	head, err := native.GetRefCommitID(pr.GetGitRefName())
	if err != nil || !extension.ValidPullDiffCommit(pr.MergeBase) || !extension.ValidPullDiffCommit(head) {
		native.Close()
		return nil, http.StatusServiceUnavailable
	}
	return &pullComparison{native: native, repository: repositoryID, index: index, base: pr.MergeBase, head: head,
		pullID: pr.ID, headRepositoryID: pr.HeadRepoID, headBranch: pr.HeadBranch, baseBranch: pr.BaseBranch}, http.StatusOK
}

func (c *pullComparison) verifyObjects() int {
	baseCommit, err := c.native.GetCommit(c.base)
	if err != nil || baseCommit.ID.String() != c.base {
		return http.StatusServiceUnavailable
	}
	headCommit, err := c.native.GetCommit(c.head)
	if err != nil || headCommit.ID.String() != c.head {
		return http.StatusServiceUnavailable
	}
	if c.base != c.head {
		ancestor, err := headCommit.HasPreviousCommit(baseCommit.ID)
		if err != nil || !ancestor {
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusOK
}

func (c *pullComparison) cursor(offset int) string {
	return strings.Join([]string{c.repository, strconv.FormatInt(c.pullID, 10), c.base, c.head, strconv.Itoa(offset)}, ".")
}

func (c *pullComparison) offset(cursor string, maximum int) (int, int) {
	if cursor == "" {
		return 0, http.StatusOK
	}
	parts := strings.Split(cursor, ".")
	if len(parts) != 5 {
		return 0, http.StatusBadRequest
	}
	offset, err := strconv.Atoi(parts[4])
	if err != nil || offset < 1 || maximum > 0 && offset > maximum || cursor != c.cursor(offset) {
		return 0, http.StatusBadRequest
	}
	return offset, http.StatusOK
}

func (c *pullComparison) recheck(ctx context.Context, admission *callbackAdmission) int {
	after, status := pullFilesParent(ctx, admission, c.repository, c.index)
	if status != http.StatusOK {
		return status
	}
	afterHead, err := c.native.GetRefCommitID(after.GetGitRefName())
	if err != nil {
		return http.StatusServiceUnavailable
	}
	if after.ID != c.pullID || after.MergeBase != c.base || afterHead != c.head ||
		after.HeadRepoID != c.headRepositoryID || after.HeadBranch != c.headBranch || after.BaseBranch != c.baseBranch {
		return http.StatusConflict
	}
	return http.StatusOK
}

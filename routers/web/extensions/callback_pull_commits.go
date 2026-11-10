package extensions

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/modules/git"
)

func resolveRepositoryPullCommits(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryPullCommits, int) {
	q := *request.PullCommits
	comparison, status := openPullComparison(ctx, admission, request.RepositoryID, q.Index)
	if status != http.StatusOK {
		return nil, status
	}
	defer comparison.native.Close()
	if comparison.head != q.Head {
		return nil, http.StatusConflict
	}
	if status := comparison.verifyObjects(); status != http.StatusOK {
		return nil, status
	}
	base, head, native := comparison.base, comparison.head, comparison.native
	id := strconv.FormatInt(comparison.pullID, 10)
	offset, status := comparison.offset(q.Cursor, 1_000_000)
	if status != http.StatusOK {
		return nil, status
	}
	output := &pullDiffBuffer{limit: 2048}
	err := git.NewCommand(ctx, "rev-list").AddOptionFormat("--max-count=%d", q.Limit+1).AddOptionFormat("--skip=%d", offset).AddDynamicArguments(base + ".." + head).Run(&git.RunOpts{Dir: native.Path, Stdout: output})
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
		page.NextCursor = comparison.cursor(offset + len(ids))
	}
	if status := comparison.recheck(ctx, admission); status != http.StatusOK {
		return nil, status
	}
	return page, http.StatusOK
}

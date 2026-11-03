package extensions

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
)

// Grouping uses the native immutable-context/(created,id) owner. Fetch thin
// metadata, never every prose body, before bounded windows.
func threadCatalogue(ctx context.Context, issueID int64) ([]issues_model.CodeThread, int) {
	var rows issues_model.CommentList
	err := db.GetEngine(ctx).Where("issue_id = ? AND type = ?", issueID, issues_model.CommentTypeCode).
		Cols("id", "type", "issue_id", "review_id", "tree_path", "line", "created_unix", "resolve_doer_id", "invalidated", "commit_sha", "original_anchor").
		Asc("created_unix").Asc("id").Limit(10001).Find(&rows)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if len(rows) > 10000 {
		return nil, http.StatusRequestEntityTooLarge
	}
	groups := issues_model.GroupCodeThreads(rows)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Root().ID < groups[j].Root().ID })
	return groups, http.StatusOK
}

// Compare every grouping/display fact, including absent original context and
// native root ordering. Prose hydration may not move a row into another group.
func sameThreadMetadata(a, b *issues_model.Comment) bool {
	return a != nil && b != nil && a.ID == b.ID && a.Type == b.Type && a.IssueID == b.IssueID &&
		a.ReviewID == b.ReviewID && a.TreePath == b.TreePath && a.Line == b.Line &&
		a.CreatedUnix == b.CreatedUnix && a.OriginalAnchor.Equal(b.OriginalAnchor) &&
		a.ResolveDoerID == b.ResolveDoerID && a.Invalidated == b.Invalidated && a.CommitSHA == b.CommitSHA
}

type threadGit struct {
	ctx        context.Context
	repo       *git.Repository
	model      *repo_model.Repository
	base, head string
	files      map[string][]nativePullFile
	failed     map[string]bool
}

func (g *threadGit) anchor(a *issues_model.CodeCommentAnchor) *extension.RepositoryThreadAnchor {
	if a == nil || a.Validate() != nil || g.repo == nil {
		return nil
	}
	key := a.Base + "." + a.Head
	if g.failed[key] {
		return nil
	}
	files, ok := g.files[key]
	if !ok {
		out := &pullDiffBuffer{limit: 2 * 1024 * 1024}
		if err := pullDiffCommand(g.ctx, g.repo, a.Base, a.Head, out, "raw"); err != nil || out.exceeded {
			g.failed[key] = true
			return nil
		}
		var valid bool
		files, valid = pullFileRecords(out.Bytes())
		if !valid {
			g.failed[key] = true
			return nil
		}
		g.files[key] = files
	}
	old, new := "", ""
	for _, f := range files {
		if a.Side == "old" && f.old == a.Path || a.Side == "new" && f.new == a.Path {
			old, new = f.old, f.new
			break
		}
	}
	if old == "" && new == "" {
		before, e1 := g.repo.GetCommit(a.Base)
		after, e2 := g.repo.GetCommit(a.Head)
		if e1 != nil || e2 != nil {
			return nil
		}
		if _, err := before.GetTreeEntryByPath(a.Path); err == nil {
			old = a.Path
		}
		if _, err := after.GetTreeEntryByPath(a.Path); err == nil {
			new = a.Path
		}
	}
	result := &extension.RepositoryThreadAnchor{Base: a.Base, Head: a.Head, OldPath: old, NewPath: new, Side: a.Side, Scope: a.Scope, StartLine: a.StartLine, EndLine: a.EndLine}
	if !extension.ValidRepositoryThreadAnchor(result) {
		return nil
	}
	return result
}
func (g *threadGit) placement(root *issues_model.Comment) (*extension.RepositoryThreadAnchor, *bool) {
	if root.Invalidated {
		value := true
		return nil, &value
	}
	if g.repo == nil || !extension.ValidPullDiffCommit(g.head) || root.Line == 0 || root.UnsignedLine() > 9007199254740991 {
		return nil, nil
	}
	// Native relocation is a display fact and never overwrites OriginalAnchor.
	if a := root.OriginalAnchor; a != nil && a.Scope == "lines" && a.StartLine != a.EndLine {
		return nil, nil
	}
	blame, err := root.ResolveCurrentLine(g.ctx, g.model, g.head)
	if err != nil || blame == nil {
		return nil, nil
	}
	if blame.CommitID != g.head {
		value := true
		return nil, &value
	}
	value := false
	side := "new"
	if root.Line < 0 {
		side = "old"
	}
	a := &issues_model.CodeCommentAnchor{Base: g.base, Head: g.head, Path: blame.FilePath, Side: side, Scope: "lines", StartLine: int64(blame.LineNumber), EndLine: int64(blame.LineNumber)}
	return g.anchor(a), &value
}
func resolveRepositoryThreads(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryThreadPage, int) {
	q := *request.Threads
	issue, pullID, status := discussionParent(ctx, admission, request.RepositoryID, extension.RepositoryCommentsRequest{Subject: "pull", Index: q.Index, Limit: 1})
	if status != http.StatusOK {
		return nil, status
	}
	if issue.PullRequest == nil || issue.PullRequest.IssueID != issue.ID || issue.PullRequest.BaseRepoID != issue.RepoID || issue.PullRequest.Index != issue.Index {
		return nil, http.StatusServiceUnavailable
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	groups, status := threadCatalogue(ctx, issue.ID)
	if status != http.StatusOK {
		return nil, status
	}
	ids := []int64{}
	for _, t := range groups {
		ids = append(ids, t.Root().ReviewID)
	}
	visible := map[int64]*issues_model.Review{}
	if len(ids) > 0 {
		if err := db.GetEngine(ctx).Where(readableReviewCondition(issue.ID, user)).In("id", ids).Find(&visible); err != nil {
			return nil, http.StatusServiceUnavailable
		}
	}
	kept := []issues_model.CodeThread{}
	for _, t := range groups {
		root := t.Root()
		if visible[root.ReviewID] == nil {
			continue
		}
		if q.ThreadID != "" {
			if strconv.FormatInt(root.ID, 10) == q.ThreadID {
				kept = append(kept, t)
			}
		} else if q.Cursor == "" || root.ID > mustParsePositiveID(q.Cursor) {
			kept = append(kept, t)
		}
	}
	if q.ThreadID != "" && len(kept) != 1 {
		return nil, http.StatusNotFound
	}
	page := &extension.RepositoryThreadPage{RepositoryID: request.RepositoryID, PullID: pullID, Index: q.Index, Items: []extension.RepositoryThread{}}
	if q.ThreadID == "" && len(kept) > q.Limit {
		kept = kept[:q.Limit]
		page.NextCursor = strconv.FormatInt(kept[len(kept)-1].Root().ID, 10)
	}
	repository, err := repo_model.GetRepositoryByID(ctx, issue.RepoID)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	g := threadGit{ctx: ctx, model: repository, base: issue.PullRequest.MergeBase, files: map[string][]nativePullFile{}, failed: map[string]bool{}}
	g.repo, _ = git.OpenRepository(ctx, repository.RepoPath())
	if g.repo != nil {
		defer g.repo.Close()
		g.head, _ = g.repo.GetRefCommitID(issue.PullRequest.GetGitRefName())
	}
	reviewIDs := []int64{}
	for _, t := range kept {
		root := t.Root()
		// Old-side relocation needs the root's stored hunk excerpt. Catalogue
		// metadata deliberately omits prose/patches; hydrate only selected roots.
		liveRoot, err := issues_model.GetCommentByID(ctx, root.ID)
		if err != nil || !sameThreadMetadata(liveRoot, root) {
			return nil, http.StatusNotFound
		}
		root = liveRoot
		reviewIDs = append(reviewIDs, root.ReviewID)
		item := extension.RepositoryThread{ID: strconv.FormatInt(root.ID, 10), ReviewID: strconv.FormatInt(root.ReviewID, 10), Anchor: g.anchor(root.OriginalAnchor), Resolved: root.ResolveDoerID != 0, Comments: []extension.RepositoryThreadComment{}}
		line := root.Line
		side := "new"
		if line < 0 {
			line = -line
			side = "old"
		}
		if line > 0 && line <= 9007199254740991 && root.TreePath != "" && len(root.TreePath) <= 4096 && utf8.ValidString(root.TreePath) {
			location := &extension.RepositoryThreadLocation{Path: root.TreePath, Side: side, Line: line}
			if extension.ValidPullDiffCommit(root.CommitSHA) {
				location.Commit = root.CommitSHA
			}
			item.RecordedLocation = location
		}
		item.CurrentAnchor, item.Outdated = g.placement(root)
		start, limit := 0, 10
		if q.ThreadID != "" {
			limit = q.Limit
			if q.Cursor != "" {
				found := false
				for i, c := range t {
					if strconv.FormatInt(c.ID, 10) == q.Cursor {
						start = i + 1
						found = true
						break
					}
				}
				if !found {
					return nil, http.StatusBadRequest
				}
			}
		}
		end := min(start+limit, len(t))
		for _, meta := range t[start:end] {
			c, err := issues_model.GetCommentByID(ctx, meta.ID)
			if err != nil || !sameThreadMetadata(c, meta) {
				return nil, http.StatusNotFound
			}
			if err := c.LoadPoster(ctx); err != nil && !user_model.IsErrUserNotExist(err) {
				return nil, http.StatusServiceUnavailable
			}
			comment := extension.RepositoryDiscussionComment{ID: strconv.FormatInt(c.ID, 10), Body: c.Content}
			if c.Poster != nil && c.Poster.ID > 0 {
				comment.Author = &extension.RepositoryIssueActor{ID: strconv.FormatInt(c.Poster.ID, 10), Username: c.Poster.Name}
			}
			if c.CreatedUnix > 0 {
				v := time.Unix(int64(c.CreatedUnix), 0).UTC()
				comment.CreatedAt = &v
			}
			if c.UpdatedUnix > 0 {
				v := time.Unix(int64(c.UpdatedUnix), 0).UTC()
				comment.UpdatedAt = &v
			}
			item.Comments = append(item.Comments, extension.RepositoryThreadComment{Comment: comment, Anchor: g.anchor(c.OriginalAnchor)})
		}
		if end < len(t) {
			item.CommentsNext = strconv.FormatInt(t[end-1].ID, 10)
		}
		page.Items = append(page.Items, item)
	}
	after, afterID, status := discussionParent(ctx, admission, request.RepositoryID, extension.RepositoryCommentsRequest{Subject: "pull", Index: q.Index, Limit: 1})
	if status != http.StatusOK {
		return nil, status
	}
	if afterID != pullID || after.ID != issue.ID {
		return nil, http.StatusServiceUnavailable
	}
	current, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	if len(reviewIDs) > 0 {
		seen := map[int64]bool{}
		unique := []int64{}
		for _, id := range reviewIDs {
			if !seen[id] {
				seen[id] = true
				unique = append(unique, id)
			}
		}
		count, err := db.GetEngine(ctx).Where(readableReviewCondition(after.ID, current)).In("id", unique).Count(new(issues_model.Review))
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		if count != int64(len(unique)) {
			return nil, http.StatusNotFound
		}
	}
	if len(kept) > 0 {
		// Re-group complete thin metadata, including replies outside this
		// prose window. A new context can separate an old unknown reply or
		// replace the root even without changing any selected comment row.
		live, status := threadCatalogue(ctx, issue.ID)
		if status != http.StatusOK {
			return nil, status
		}
		for _, before := range kept {
			after := issues_model.FindCodeThread(live, before.Root().ID)
			if len(before) != len(after) {
				return nil, http.StatusConflict
			}
			for i, member := range before {
				if !sameThreadMetadata(member, after[i]) {
					return nil, http.StatusConflict
				}
			}
		}
	}
	if g.repo != nil {
		head, _ := g.repo.GetRefCommitID(after.PullRequest.GetGitRefName())
		if head != g.head || after.PullRequest.MergeBase != g.base {
			return nil, http.StatusConflict
		}
	}
	return page, http.StatusOK
}

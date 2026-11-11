package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Native protocol fingerprints require standard-library semantics.
	"net/http"
	"sort"
	"strconv"
	"strings"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	pull_service "forgejo.org/services/pull"

	"github.com/gobwas/glob"
)

func protectionRead(ctx context.Context, pr *issues_model.PullRequest) (*extension.RepositoryProtection, int) {
	if pr.BaseBranch == "" {
		return nil, http.StatusOK
	}
	p, err := git_model.GetFirstMatchProtectedBranchRule(ctx, pr.BaseRepoID, pr.BaseBranch)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	r := &extension.RepositoryProtection{Branch: pr.BaseBranch, RequiredContexts: []string{}}
	if p == nil {
		return r, http.StatusOK
	}
	if p.RequiredApprovals < 0 || p.RequiredApprovals > 9007199254740991 {
		return nil, http.StatusServiceUnavailable
	}
	r.Protected = true
	r.StatusChecks = p.EnableStatusCheck
	r.RequiredContexts = append(r.RequiredContexts, p.StatusCheckContexts...)
	r.RequiredApprovals = p.RequiredApprovals
	r.BlockRejected = p.BlockOnRejectedReviews
	r.BlockReviewRequests = p.BlockOnOfficialReviewRequests
	r.RequireUpToDate = p.BlockOnOutdatedBranch
	r.RequireSigned = p.RequireSignedCommits
	r.DismissStale = p.DismissStaleApprovals
	r.IgnoreStale = p.IgnoreStaleApprovals
	r.ApplyToAdmins = p.ApplyToAdmins
	r.ProtectedFiles = p.ProtectedFilePatterns
	r.UnprotectedFiles = p.UnprotectedFilePatterns
	return r, http.StatusOK
}

func checksFingerprint(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readChecksSnapshot(ctx context.Context, pr *issues_model.PullRequest, head, kind string) (*extension.RepositoryChecks, int) {
	if kind == "job" {
		return readCheckJobsSnapshot(ctx, pr, head)
	}
	protection, status := protectionRead(ctx, pr)
	if status != http.StatusOK {
		return nil, status
	}
	statuses, count, err := git_model.GetLatestCommitStatus(ctx, pr.BaseRepoID, head, db.ListOptions{Page: 1, PageSize: 1001})
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if count > 1000 || len(statuses) > 1000 {
		return nil, http.StatusRequestEntityTooLarge
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	r := &extension.RepositoryChecks{RepositoryID: strconv.FormatInt(pr.BaseRepoID, 10), PullID: strconv.FormatInt(pr.ID, 10), Index: strconv.FormatInt(pr.Index, 10), Head: head, Protection: protection, MissingRequired: []string{}, InvalidRequired: []string{}, StatusGate: "unknown", Items: []extension.RepositoryCommitCheck{}}
	r.Kind = "status"
	matchers := map[string]glob.Glob{}
	if protection != nil {
		r.StatusGate = "not_required"
		if protection.StatusChecks {
			for _, pattern := range protection.RequiredContexts {
				g, err := glob.Compile(pattern)
				if err != nil {
					r.InvalidRequired = append(r.InvalidRequired, pattern)
				} else {
					matchers[pattern] = g
				}
				found := false
				for _, s := range statuses {
					if s.Context == pattern || g != nil && g.Match(s.Context) {
						found = true
						break
					}
				}
				if !found {
					r.MissingRequired = append(r.MissingRequired, pattern)
				}
			}
			r.StatusGate = string(pull_service.MergeRequiredContextsCommitStatus(statuses, protection.RequiredContexts))
			switch r.StatusGate {
			case "pending", "success", "failure", "error", "warning":
			default:
				r.StatusGate = "unknown"
			}
		}
	}
	for _, s := range statuses {
		if s.RepoID != pr.BaseRepoID || s.SHA != head {
			return nil, http.StatusServiceUnavailable
		}
		var required *bool
		if protection != nil {
			value := false
			if protection.StatusChecks {
				for _, pattern := range protection.RequiredContexts {
					if s.Context == pattern || matchers[pattern] != nil && matchers[pattern].Match(s.Context) {
						value = true
						break
					}
				}
			}
			required = &value
		}
		r.Items = append(r.Items, extension.RepositoryCommitCheck{ID: strconv.FormatInt(s.ID, 10), Name: s.Context, State: string(s.State), Description: s.Description, DetailsURL: s.TargetURL, Required: required})
	}
	r.Revision = checksFingerprint(r)
	return r, http.StatusOK
}

func resolveRepositoryChecks(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryChecks, int) {
	q := *request.Checks
	pr, status := pullFilesParent(ctx, admission, request.RepositoryID, q.Index)
	if status != http.StatusOK {
		return nil, status
	}
	if q.Kind == "job" {
		if status := readableCheckJobs(ctx, admission, pr.BaseRepoID); status != http.StatusOK {
			return nil, status
		}
	}
	repo, err := repo_model.GetRepositoryByID(ctx, pr.BaseRepoID)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	native, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	defer native.Close()
	head, err := native.GetRefCommitID(pr.GetGitRefName())
	if err != nil || !extension.ValidPullDiffCommit(head) {
		return nil, http.StatusServiceUnavailable
	}
	commit, err := native.GetCommit(head)
	if err != nil || commit.ID.String() != head {
		return nil, http.StatusServiceUnavailable
	}
	if q.Head != "" && q.Head != head {
		return nil, http.StatusConflict
	}
	snapshot, status := readChecksSnapshot(ctx, pr, head, q.Kind)
	if status != http.StatusOK {
		return nil, status
	}
	offset := 0
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ".")
		if len(parts) != 5 {
			return nil, http.StatusBadRequest
		}
		offset, err = strconv.Atoi(parts[4])
		if err != nil || offset < 1 || strconv.Itoa(offset) != parts[4] || parts[0] != request.RepositoryID || parts[1] != snapshot.PullID {
			return nil, http.StatusBadRequest
		}
		if parts[2] != head || parts[3] != snapshot.Revision {
			return nil, http.StatusConflict
		}
		if offset >= len(snapshot.Items) {
			return nil, http.StatusBadRequest
		}
	}
	end := min(offset+q.Limit, len(snapshot.Items))
	page := *snapshot
	page.Items = snapshot.Items[offset:end]
	if end < len(snapshot.Items) {
		page.NextCursor = strings.Join([]string{request.RepositoryID, snapshot.PullID, head, snapshot.Revision, strconv.Itoa(end)}, ".")
	}
	// Re-read actual native scope, head and rule/status view after all I/O.
	after, status := pullFilesParent(ctx, admission, request.RepositoryID, q.Index)
	if status != http.StatusOK {
		return nil, status
	}
	afterHead, err := native.GetRefCommitID(after.GetGitRefName())
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if after.ID != pr.ID || afterHead != head || after.BaseBranch != pr.BaseBranch {
		return nil, http.StatusConflict
	}
	if q.Kind == "job" {
		if status := readableCheckJobs(ctx, admission, after.BaseRepoID); status != http.StatusOK {
			return nil, status
		}
	}
	current, status := readChecksSnapshot(ctx, after, head, q.Kind)
	if status != http.StatusOK {
		return nil, status
	}
	if current.Revision != snapshot.Revision {
		return nil, http.StatusConflict
	}
	return &page, http.StatusOK
}

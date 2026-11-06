package extensions

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	asymkey_service "forgejo.org/services/asymkey"
	pull_service "forgejo.org/services/pull"
)

func capabilityCommit(repo *git.Repository, ref string) string {
	if repo == nil || ref == "" {
		return ""
	}
	id, err := repo.GetRefCommitID(ref)
	if err != nil || !extension.ValidPullDiffCommit(id) {
		return ""
	}
	commit, err := repo.GetCommit(id)
	if err != nil || commit.ID.String() != id {
		return ""
	}
	return id
}

func normalMergeCapability(ctx context.Context, repo *repo_model.Repository, user *user_model.User, permission access_model.Permission, pr *issues_model.PullRequest, head, base string) extension.RepositoryActionCapability {
	prUnit, err := repo.GetUnit(ctx, unit.TypePullRequests)
	if err != nil {
		return capabilityFact("unavailable", "merge_policy_unavailable")
	}
	cfg := prUnit.PullRequestsConfig()
	if !(cfg.AllowMerge || cfg.AllowRebase || cfg.AllowRebaseMerge || cfg.AllowSquash || cfg.AllowFastForwardOnly) {
		return capabilityFact("unsupported", "merge_methods_disabled")
	}
	// This is the same preflight as an ordinary immediate native merge. Never
	// force protection bypass, schedule an automerge, or mark manually merged.
	err = pull_service.CheckPullMergeable(ctx, user, &permission, pr, pull_service.MergeCheckTypeGeneral, false)
	switch {
	case errors.Is(err, pull_service.ErrHasMerged):
		return capabilityFact("denied", "pull_merged")
	case errors.Is(err, pull_service.ErrIsClosed):
		return capabilityFact("denied", "pull_closed")
	case errors.Is(err, pull_service.ErrIsWorkInProgress):
		return capabilityFact("denied", "pull_draft")
	case errors.Is(err, pull_service.ErrIsChecking):
		return capabilityFact("denied", "merge_checking")
	case errors.Is(err, pull_service.ErrNotMergeableState):
		return capabilityFact("denied", "not_mergeable")
	case errors.Is(err, pull_service.ErrDependenciesLeft):
		return capabilityFact("denied", "open_dependencies")
	case errors.Is(err, pull_service.ErrUserNotAllowedToMerge):
		return capabilityFact("denied", "merge_permission_denied")
	case models.IsErrDisallowedToMerge(err):
		return capabilityFact("denied", "branch_protection")
	case asymkey_service.IsErrWontSign(err):
		return capabilityFact("denied", "required_signature_unavailable")
	case err != nil:
		return capabilityFact("unavailable", "merge_checks_unavailable")
	}
	if head == "" || base == "" {
		return capabilityFact("unavailable", "merge_refs_unavailable")
	}
	if err := pr.LoadHeadRepo(ctx); err != nil || pr.HeadRepo == nil {
		return capabilityFact("unavailable", "source_head_unavailable")
	}
	source, err := git.OpenRepository(ctx, pr.HeadRepo.RepoPath())
	if err != nil {
		return capabilityFact("unavailable", "source_head_unavailable")
	}
	defer source.Close()
	ref := git.BranchPrefix + pr.HeadBranch
	if pr.Flow == issues_model.PullRequestFlowAGit {
		ref = pr.GetGitRefName()
	}
	if capabilityCommit(source, ref) != head {
		return capabilityFact("unavailable", "head_sync_pending")
	}
	return capabilityFact("allowed", "native_merge_checks_passed")
}

func readPullCapabilities(ctx context.Context, repo *repo_model.Repository, user *user_model.User, permission access_model.Permission, q extension.RepositoryCapabilitiesRequest, r *extension.RepositoryCapabilities) (*extension.RepositoryCapabilities, int) {
	issue, err := issues_model.GetIssueByIndex(ctx, repo.ID, mustParsePositiveID(q.Index))
	if issues_model.IsErrIssueNotExist(err) || err == nil && !issue.IsPull {
		return nil, http.StatusNotFound
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil {
		return nil, http.StatusServiceUnavailable
	}
	pr := issue.PullRequest
	if pr.BaseRepoID != repo.ID || pr.IssueID != issue.ID || pr.Index != issue.Index {
		return nil, http.StatusServiceUnavailable
	}
	issue.Repo = repo
	pr.Issue, pr.BaseRepo = issue, repo
	r.ResourceID = strconv.FormatInt(pr.ID, 10)
	for _, action := range []string{"issue.edit", "issue.comment"} {
		r.Actions[action] = capabilityFact("unsupported", "pull_scope")
	}
	edit, comment := capabilityFact("denied", "pull_edit_denied"), capabilityFact("allowed", "pull_comment")
	if issue.IsPoster(user.ID) || permission.CanWriteIssuesOrPulls(true) {
		edit = capabilityFact("allowed", "pull_author_or_writer")
	}
	if issue.IsLocked && !permission.CanWriteIssuesOrPulls(true) && !user.IsAdmin {
		comment = capabilityFact("denied", "pull_locked")
	}
	resolve := capabilityFact("denied", "thread_resolution_denied")
	canResolve, err := issues_model.CanMarkConversation(ctx, issue, user)
	if err != nil {
		resolve = capabilityFact("unavailable", "thread_permission_unavailable")
	} else if canResolve {
		resolve = capabilityFact("allowed", "native_thread_permission")
	}
	r.Actions["pull.edit"], r.Actions["pull.comment"], r.Actions["thread.resolve"] = edit, comment, resolve
	native, err := git.OpenRepository(ctx, repo.RepoPath())
	if err == nil {
		defer native.Close()
		r.Head = capabilityCommit(native, pr.GetGitRefName())
		r.Base = capabilityCommit(native, git.BranchPrefix+pr.BaseBranch)
	}
	review := capabilityFact("allowed", "review_comment")
	if r.Head == "" {
		review = capabilityFact("unavailable", "head_unavailable")
	}
	r.Actions["pull.review"] = capabilityFact("unknown", "review_kind_required")
	r.Actions["pull.review.comment"] = review
	for _, action := range []string{"pull.review.approve", "pull.review.request_changes"} {
		r.Actions[action] = review
		if issue.IsPoster(user.ID) {
			r.Actions[action] = capabilityFact("denied", "self_review")
		}
	}
	if r.Head == "" {
		r.Actions["pull.review"] = review
		r.Actions["checks.read"] = capabilityFact("unavailable", "head_unavailable")
	} else {
		r.Actions["checks.read"] = capabilityFact("allowed", "current_head_statuses")
	}
	if r.Actions["checks.jobs.read"].State == "unknown" {
		r.Actions["checks.jobs.read"] = r.Actions["checks.read"]
	}
	r.Actions["pull.merge"] = normalMergeCapability(ctx, repo, user, permission, pr, r.Head, r.Base)
	if !repo.AllowsPulls(ctx) {
		for _, action := range []string{"pull.review", "pull.review.comment", "pull.review.approve", "pull.review.request_changes", "pull.merge"} {
			r.Actions[action] = capabilityFact("unsupported", "pull_workflow_unavailable")
		}
	}
	if repo.IsArchived {
		for _, action := range []string{"pull.edit", "pull.comment", "pull.review", "pull.review.comment", "pull.review.approve", "pull.review.request_changes", "thread.resolve", "pull.merge"} {
			r.Actions[action] = capabilityFact("denied", "repository_archived")
		}
	}
	r.MergeMethods = map[string]extension.RepositoryActionCapability{}
	prUnit, unitErr := repo.GetUnit(ctx, unit.TypePullRequests)
	for _, method := range []string{"merge", "squash", "rebase", "rebase-merge", "fast-forward-only"} {
		fact := r.Actions["pull.merge"]
		if unitErr != nil {
			fact = capabilityFact("unavailable", "merge_policy_unavailable")
		} else if !prUnit.PullRequestsConfig().IsMergeStyleAllowed(repo_model.MergeStyle(method)) {
			fact = capabilityFact("unsupported", "merge_method_disabled")
		} else if method == "fast-forward-only" && fact.State == "allowed" {
			mergeBase, err := native.GetMergeBaseSimple(r.Base, r.Head)
			if err != nil || !extension.ValidPullDiffCommit(mergeBase) {
				fact = capabilityFact("unavailable", "merge_base_unavailable")
			} else if mergeBase != r.Base {
				fact = capabilityFact("denied", "not_fast_forward")
			}
		}
		r.MergeMethods[method] = fact
	}
	return r, http.StatusOK
}

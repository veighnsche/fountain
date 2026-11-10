package extensions

import (
	"context"
	"net/http"
	"strconv"

	extension "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/setting"
)

func capabilityFact(state, reason string) extension.RepositoryActionCapability {
	return extension.RepositoryActionCapability{State: state, Reason: reason}
}

func discussionCommentCapability(facts issues_model.DiscussionActionFacts, subject string) extension.RepositoryActionCapability {
	switch facts.Comment() {
	case issues_model.NativeActionAllowed:
		return capabilityFact("allowed", subject+"_comment")
	case issues_model.NativeActionArchived:
		return capabilityFact("denied", "repository_archived")
	case issues_model.NativeActionBlocked:
		return capabilityFact("denied", "actor_blocked")
	default:
		return capabilityFact("denied", subject+"_locked")
	}
}

func discussionEditCapabilities(facts issues_model.DiscussionActionFacts, subject string) (title, body extension.RepositoryActionCapability) {
	title, body = capabilityFact("denied", subject+"_edit_denied"), capabilityFact("denied", subject+"_edit_denied")
	if facts.TitleEdit() == issues_model.NativeActionAllowed {
		title = capabilityFact("allowed", subject+"_author_or_writer")
	}
	if facts.BodyEdit() == issues_model.NativeActionAllowed {
		body = capabilityFact("allowed", subject+"_poster_or_writer")
	}
	if facts.Archived {
		title, body = capabilityFact("denied", "repository_archived"), capabilityFact("denied", "repository_archived")
	}
	return title, body
}

func discussionStateCapability(blocker issues_model.NativeActionBlocker, allowed string) extension.RepositoryActionCapability {
	if blocker == issues_model.NativeActionAllowed {
		return capabilityFact("allowed", allowed)
	}
	reason := string(blocker)
	if blocker == issues_model.NativeActionPermission {
		reason = "state_author_or_writer_required"
	}
	if blocker == issues_model.NativeActionBlocked {
		reason = "state_actor_blocked"
	}
	return capabilityFact("denied", reason)
}

func discussionLockCapability(blocker issues_model.NativeActionBlocker, allowed string) extension.RepositoryActionCapability {
	if blocker == issues_model.NativeActionAllowed {
		return capabilityFact("allowed", allowed)
	}
	reason := string(blocker)
	if blocker == issues_model.NativeActionPermission {
		reason = "lock_writer_required"
	}
	return capabilityFact("denied", reason)
}

func readRepositoryCapabilities(ctx context.Context, admission *callbackAdmission, repository string, q extension.RepositoryCapabilitiesRequest) (*extension.RepositoryCapabilities, int) {
	metadata, status := resolveCollaborationRepository(ctx, admission, repository)
	if status != http.StatusOK {
		return nil, status
	}
	if metadata.Permission == "issues" && q.Subject == "pull" {
		return nil, http.StatusNotFound
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	repo, err := repo_model.GetRepositoryByID(ctx, mustParsePositiveID(repository))
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, user)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	unitRead := func(kind unit.Type) extension.RepositoryActionCapability {
		if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) {
			return capabilityFact("unsupported", "unit_disabled")
		}
		if !permission.CanRead(kind) {
			return capabilityFact("denied", "unit_read_denied")
		}
		return capabilityFact("allowed", "unit_read")
	}
	issues, pulls, jobs := unitRead(unit.TypeIssues), unitRead(unit.TypePullRequests), unitRead(unit.TypeActions)
	if metadata.Permission == "issues" {
		pulls, jobs = capabilityFact("denied", "code_read_required"), capabilityFact("denied", "code_read_required")
	}
	if !setting.Actions.Enabled {
		jobs = capabilityFact("unsupported", "actions_disabled")
	}
	if issues.State == "unsupported" && repo.UnitEnabled(ctx, unit.TypeExternalTracker) {
		issues.Reason = "external_issues"
	}
	mutable := func(read extension.RepositoryActionCapability, need string) extension.RepositoryActionCapability {
		if read.State != "allowed" {
			return read
		}
		if repo.IsArchived {
			return capabilityFact("denied", "repository_archived")
		}
		return capabilityFact("unknown", need)
	}
	create := mutable(issues, "issue_input_required")
	if create.State == "unknown" {
		create = capabilityFact("allowed", "issue_create")
	}
	createPull := mutable(pulls, "comparison_required")
	if createPull.State == "unknown" && !repo.CanEnablePulls() {
		createPull = capabilityFact("unsupported", "pull_creation_unavailable")
	}
	checks := pulls
	if checks.State == "allowed" {
		checks = capabilityFact("unknown", "pull_required")
	}
	if jobs.State == "allowed" {
		jobs = checks
	}
	r := &extension.RepositoryCapabilities{RepositoryID: repository, ActorID: strconv.FormatInt(user.ID, 10), Subject: q.Subject, Index: q.Index, Actions: map[string]extension.RepositoryActionCapability{
		"issues.read": issues, "issue.create": create, "issue.edit": mutable(issues, "issue_required"), "issue.comment": mutable(issues, "issue_required"),
		"pulls.read": pulls, "pull.create": createPull, "pull.comment": mutable(pulls, "pull_required"), "pull.review": mutable(pulls, "pull_required"), "thread.resolve": mutable(pulls, "pull_required"), "pull.merge": mutable(pulls, "pull_required"), "checks.read": checks, "checks.jobs.read": jobs,
	}}
	for _, action := range []string{"pull.edit", "pull.review.comment", "pull.review.approve", "pull.review.request_changes"} {
		r.Actions[action] = mutable(pulls, "pull_required")
	}
	if q.Subject == "issue" {
		// Check the requested unit before resolving its resource. Code access
		// alone does not permit disclosure of an internal issue identity.
		if issues.State != "allowed" {
			return nil, http.StatusNotFound
		}
		issue, err := issues_model.GetIssueByIndex(ctx, repo.ID, mustParsePositiveID(q.Index))
		if issues_model.IsErrIssueNotExist(err) || err == nil && issue.IsPull {
			return nil, http.StatusNotFound
		}
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		r.ResourceID = strconv.FormatInt(issue.ID, 10)
		facts := issues_model.ReadDiscussionActionFacts(ctx, repo, issue, user, permission)
		edit, _ := discussionEditCapabilities(facts, "issue")
		comment := discussionCommentCapability(facts, "issue")
		r.Actions["issue.edit"], r.Actions["issue.comment"] = edit, comment
		for _, action := range []string{"pull.edit", "pull.comment", "pull.review", "pull.review.comment", "pull.review.approve", "pull.review.request_changes", "thread.resolve", "pull.merge", "checks.read", "checks.jobs.read"} {
			r.Actions[action] = capabilityFact("unsupported", "issue_scope")
		}
	}
	if q.Subject == "pull" {
		if pulls.State != "allowed" {
			return nil, http.StatusNotFound
		}
		return readPullCapabilities(ctx, repo, user, permission, q, r)
	}
	return r, http.StatusOK
}

func resolveRepositoryCapabilities(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryCapabilities, int) {
	q := *request.Capabilities
	before, status := readRepositoryCapabilities(ctx, admission, request.RepositoryID, q)
	if status != http.StatusOK {
		return nil, status
	}
	after, status := readRepositoryCapabilities(ctx, admission, request.RepositoryID, q)
	if status == http.StatusNotFound {
		return nil, http.StatusConflict
	}
	if status != http.StatusOK {
		return nil, status
	}
	if checksFingerprint(before) != checksFingerprint(after) {
		return nil, http.StatusConflict
	}
	return before, http.StatusOK
}

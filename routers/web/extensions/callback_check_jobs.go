package extensions

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	extension "forgejo.org/extension-sdk"
	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"

	"xorm.io/builder"
)

func readableCheckJobs(ctx context.Context, admission *callbackAdmission, repoID int64) int {
	if !setting.Actions.Enabled || unit.TypeActions.UnitGlobalDisabled() {
		return http.StatusNotFound
	}
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return status
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, user)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	if !permission.CanRead(unit.TypeActions) {
		return http.StatusNotFound
	}
	return http.StatusOK
}

func checkJobState(s actions_model.Status) string {
	if value := s.String(); value != "" {
		return value
	}
	return strconv.Itoa(int(s))
}

func checkJobTime(t timeutil.TimeStamp) *string {
	if t <= 0 {
		return nil
	}
	value := t.AsTime().UTC().Format(time.RFC3339)
	return &value
}

func readCheckJobsSnapshot(ctx context.Context, pr *issues_model.PullRequest, head string) (*extension.RepositoryChecks, int) {
	// Read commit-bound runs, keeping distinct reruns rather than conflating
	// identically named jobs from different workflows/runs. PR-specific runs
	// must belong to this PR; commit-wide runs can be relevant to any PR at it.
	var runs []*actions_model.ActionRun
	if err := db.GetEngine(ctx).Where(builder.Eq{"repo_id": pr.BaseRepoID, "commit_sha": head}).
		And(builder.Or(builder.Eq{"pull_request_id": 0}, builder.Eq{"pull_request_id": pr.ID})).
		Cols("id", "repo_id", "index", "workflow_id", "workflow_directory", "ref", "commit_sha", "workflow_source_commit", "event", "trigger_event", "status", "need_approval", "pull_request_id").Asc("id").Limit(1001).Find(&runs); err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if len(runs) > 1000 {
		return nil, http.StatusRequestEntityTooLarge
	}
	r := &extension.RepositoryChecks{Kind: "job", RepositoryID: strconv.FormatInt(pr.BaseRepoID, 10), PullID: strconv.FormatInt(pr.ID, 10), Index: strconv.FormatInt(pr.Index, 10), Head: head, Items: []extension.RepositoryCommitCheck{}, MissingRequired: []string{}, InvalidRequired: []string{}, StatusGate: "unknown"}
	runMap := map[int64]*actions_model.ActionRun{}
	runIDs := []int64{}
	for _, run := range runs {
		if strings.HasPrefix(run.Ref, "refs/pull/") && run.Ref != pr.GetGitRefName() {
			continue
		}
		runMap[run.ID] = run
		runIDs = append(runIDs, run.ID)
	}
	if len(runIDs) > 0 {
		// Include all jobs in admitted runs to retain the native job route index.
		// Payloads/logs/tasks are not selected or exported.
		var jobs []*actions_model.ActionRunJob
		if err := db.GetEngine(ctx).In("run_id", runIDs).Cols("id", "run_id", "repo_id", "commit_sha", "name", "job_id", "attempt", "status", "started", "stopped").Asc("id").Limit(1001).Find(&jobs); err != nil {
			return nil, http.StatusServiceUnavailable
		}
		if len(jobs) > 1000 {
			return nil, http.StatusRequestEntityTooLarge
		}
		repo, err := repo_model.GetRepositoryByID(ctx, pr.BaseRepoID)
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		indices := map[int64]int{}
		for _, job := range jobs {
			run := runMap[job.RunID]
			index := indices[job.RunID]
			indices[job.RunID]++
			if run == nil || job.RepoID != pr.BaseRepoID || job.CommitSHA != head {
				continue
			}
			run.Repo = repo
			workflowCommit := head
			if ok, source := run.WorkflowSourceCommit.Get(); ok {
				workflowCommit = source
			}
			attempt := max(job.Attempt, 1)
			detail := fmt.Sprintf("%s/jobs/%d/attempt/%d", run.Link(), index, attempt)
			r.Items = append(r.Items, extension.RepositoryCommitCheck{ID: strconv.FormatInt(job.ID, 10), Name: job.Name, State: checkJobState(job.Status), DetailsURL: detail, Job: &extension.RepositoryCheckJob{RunID: strconv.FormatInt(run.ID, 10), RunNumber: strconv.FormatInt(run.Index, 10), Workflow: run.WorkflowPath(), Ref: run.Ref, Event: string(run.Event), TriggerEvent: run.TriggerEvent, RunState: checkJobState(run.Status), NeedApproval: run.NeedApproval, WorkflowCommit: workflowCommit, JobID: job.JobID, Attempt: strconv.FormatInt(job.Attempt, 10), StartedAt: checkJobTime(job.Started), StoppedAt: checkJobTime(job.Stopped)}})
		}
	}
	r.Revision = checksFingerprint(r)
	return r, http.StatusOK
}

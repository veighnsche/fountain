// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"fmt"
	"strings"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	execcontext "forgejo.org/modules/nativeoperation"
	webhook_module "forgejo.org/modules/webhook"
)

// EffectActionsConsistent releases an interrupted Actions run/job update
// whose referenced rows are intact. Run/job updates apply per-transaction,
// so any observed structurally consistent state is a valid end state;
// the exclusive held reservation proves no other writer interleaved.
// Pre-claim states are by definition not this owner's effect, while a
// torn post-claim state shows as inconsistency and fences.
const EffectActionsConsistent = "consistent"

// recoverActionsRun reconciles one held ordinary Actions run/job/status
// writer from its scope identities and strictly parsed resource label.
// Anything unattributable stays fenced.
func (s *Service) recoverActionsRun(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	resource, err := execcontext.OrdinaryResource(reservation.Owner, FamilyActionsRun)
	if err != nil {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "actions-run owner names no parseable resource")
	}
	claim, err := parseActionsResource(resource)
	if err != nil {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "actions-run resource is not attributable")
	}
	switch claim.kind {
	case "run":
		return s.recoverActionsRunScope(ctx, assessment, reservation, scope)
	case "dispatch":
		return s.recoverActionsDispatch(ctx, assessment, reservation, scope, claim.workflow)
	case "schedule":
		return s.recoverActionsSchedule(ctx, assessment, reservation, scope, claim.scheduleID)
	case "trust":
		return s.recoverActionsTrust(ctx, assessment, reservation, scope, claim)
	case "schedule-batch":
		return s.recoverActionsScheduleBatch(ctx, assessment, reservation, scope, claim)
	case "status":
		return s.recoverActionsStatus(ctx, assessment, reservation, scope, claim)
	default:
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("actions-run resource %q has no offline reconciliation", resource))
	}
}

// recoverActionsRunScope reconciles one run/job-level claim (rerun,
// cancel, approval, emitter batch or sweep item) by verifying the
// structural consistency of its run and job rows.
func (s *Service) recoverActionsRunScope(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	if scope.RunID <= 0 && scope.JobID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "actions-run scope names no run or job")
	}
	if scope.RunID > 0 {
		consistent, detail, err := s.actionsRunConsistent(ctx, scope.RunID, scope.RepositoryID)
		if err != nil {
			return RecoveryAssessment{}, err
		}
		if !consistent {
			return fenced(assessment, ReasonRecoveryUnaccounted, detail)
		}
		assessment.Checks = append(assessment.Checks, detail)
	}
	if scope.JobID > 0 {
		consistent, detail, err := s.actionsJobConsistent(ctx, scope.JobID, scope.RunID)
		if err != nil {
			return RecoveryAssessment{}, err
		}
		if !consistent {
			return fenced(assessment, ReasonRecoveryUnaccounted, detail)
		}
		assessment.Checks = append(assessment.Checks, detail)
	}
	return s.releaseRecovered(ctx, assessment, reservation, EffectActionsConsistent)
}

// recoverActionsDispatch reconciles one workflow-dispatch claim by
// verifying every run under its dispatch key. Dispatches are
// human-initiated and rare, so the key stays narrow.
func (s *Service) recoverActionsDispatch(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, workflow string) (RecoveryAssessment, error) {
	if scope.RepositoryID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "dispatch scope names no repository")
	}
	if _, err := repo_model.GetRepositoryByID(ctx, scope.RepositoryID); err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return fenced(assessment, ReasonRecoveryMissingEvidence, "repository for the held scope no longer exists")
		}
		return RecoveryAssessment{}, err
	}
	runs, err := db.Find[actions_model.ActionRun](ctx, actions_model.FindRunOptions{
		RepoID:       scope.RepositoryID,
		WorkflowID:   workflow,
		TriggerEvent: webhook_module.HookEventWorkflowDispatch,
	})
	if err != nil {
		return RecoveryAssessment{}, err
	}
	for _, run := range runs {
		consistent, detail, err := s.actionsRunConsistent(ctx, run.ID, scope.RepositoryID)
		if err != nil {
			return RecoveryAssessment{}, err
		}
		if !consistent {
			return fenced(assessment, ReasonRecoveryUnaccounted, detail)
		}
		assessment.Checks = append(assessment.Checks, detail)
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("dispatch key repository %d workflow %q matches %d runs", scope.RepositoryID, workflow, len(runs)))
	return s.releaseRecovered(ctx, assessment, reservation, EffectActionsConsistent)
}

// recoverActionsSchedule reconciles one schedule-creation claim by
// verifying every run its schedule produced.
func (s *Service) recoverActionsSchedule(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, scheduleID int64) (RecoveryAssessment, error) {
	if scope.RepositoryID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "schedule scope names no repository")
	}
	if _, err := repo_model.GetRepositoryByID(ctx, scope.RepositoryID); err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return fenced(assessment, ReasonRecoveryMissingEvidence, "repository for the held scope no longer exists")
		}
		return RecoveryAssessment{}, err
	}
	runs, err := actions_model.GetRunsByScheduleID(ctx, scheduleID)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	for _, run := range runs {
		consistent, detail, err := s.actionsRunConsistent(ctx, run.ID, scope.RepositoryID)
		if err != nil {
			return RecoveryAssessment{}, err
		}
		if !consistent {
			return fenced(assessment, ReasonRecoveryUnaccounted, detail)
		}
		assessment.Checks = append(assessment.Checks, detail)
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("schedule %d matches %d runs", scheduleID, len(runs)))
	return s.releaseRecovered(ctx, assessment, reservation, EffectActionsConsistent)
}

// recoverActionsStatus reconciles one external commit-status insert from
// its exact expected latest-status tuple. Statuses are append-only and
// the insert is one transaction, so the latest row for the commit and
// context either matches this owner's insert or predates it; nothing
// else could insert while it held the reservation. Derived statuses from
// Actions updates reconcile under their own task/run owners instead.
func (s *Service) recoverActionsStatus(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, claim actionsClaim) (RecoveryAssessment, error) {
	if scope.RepositoryID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "status scope names no repository")
	}
	if _, err := repo_model.GetRepositoryByID(ctx, scope.RepositoryID); err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return fenced(assessment, ReasonRecoveryMissingEvidence, "repository for the held scope no longer exists")
		}
		return RecoveryAssessment{}, err
	}
	statuses, _, err := git_model.GetLatestCommitStatus(ctx, scope.RepositoryID, claim.sha, db.ListOptionsAll)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	for _, status := range statuses {
		if strings.TrimSpace(status.Context) != strings.TrimSpace(claim.context) {
			continue
		}
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("latest status for context %q: state=%s creator=%d", claim.context, status.State, status.CreatorID))
		if string(status.State) == claim.state && status.CreatorID == claim.creatorID {
			return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectCommitted)
		}
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("no status for commit %.12s context %q", claim.sha, claim.context))
	return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
}

// recoverActionsTaskPick reconciles one task-assignment (pick) claim,
// which names the candidate job and runner but no task yet. An
// unassigned job in any valid state predates this owner's effect; an
// assignment to another runner likewise predates its claim, since no
// other pick could run while it held the reservation. Only an
// assignment to the held runner with agreeing task/job rows and its
// resulting commit status establishes this owner's effect.
func (s *Service) recoverActionsTaskPick(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	if scope.JobID <= 0 || scope.RunnerID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "task-pick scope names no job or runner")
	}
	job, err := actions_model.GetRunJobByID(ctx, scope.JobID)
	if err != nil {
		return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("job %d for the held scope cannot be read", scope.JobID))
	}
	if job.TaskID == 0 {
		if !validActionsStatus(job.Status) {
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("job %d has invalid status %d", job.ID, int(job.Status)))
		}
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("job %d unassigned with status %s", job.ID, job.Status))
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	}
	task, err := actions_model.GetTaskByID(ctx, job.TaskID)
	if err != nil {
		return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("task %d of job %d cannot be read", job.TaskID, job.ID))
	}
	own := task.RunnerID == scope.RunnerID
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("task status=%d stopped=%d job status=%d stopped=%d runner_match=%t",
		task.Status, task.Stopped, job.Status, job.Stopped, own))
	if task.Status.IsDone() || job.Status.IsDone() {
		return fenced(assessment, ReasonRecoveryUnaccounted, "task/job left the assigned state while held")
	}
	if task.Status != actions_model.StatusRunning || job.Status != actions_model.StatusRunning || job.TaskID != task.ID {
		return fenced(assessment, ReasonRecoveryUnaccounted, "assigned task and job rows disagree")
	}
	run, err := actions_model.GetRunByID(ctx, job.RunID)
	if err != nil {
		return fenced(assessment, ReasonRecoveryUnaccounted, "run for the held job cannot be read")
	}
	if run.ScheduleID == 0 {
		statuses, _, err := git_model.GetLatestCommitStatus(ctx, job.RepoID, job.CommitSHA, db.ListOptionsAll)
		if err != nil {
			return RecoveryAssessment{}, err
		}
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("commit statuses for job sha: %d", len(statuses)))
		if len(statuses) == 0 {
			return fenced(assessment, ReasonRecoveryUnaccounted, "assigned job has no resulting commit status")
		}
	}
	if !own {
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	}
	return s.releaseRecovered(ctx, assessment, reservation, "assigned")
}

// actionsRunConsistent verifies the run row and every job row of one run:
// known statuses, matching run/repo identity and intact task references.
//
//nolint:unparam // Keep the common operation helper and callback signature.
func (s *Service) actionsRunConsistent(ctx context.Context, runID, repoID int64) (bool, string, error) {
	run, err := actions_model.GetRunByID(ctx, runID)
	if err != nil {
		return false, fmt.Sprintf("run %d for the held scope cannot be read", runID), nil
	}
	if !validActionsStatus(run.Status) {
		return false, fmt.Sprintf("run %d has invalid status %d", runID, int(run.Status)), nil
	}
	if repoID > 0 && run.RepoID != repoID {
		return false, fmt.Sprintf("run %d belongs to repository %d, not held repository %d", runID, run.RepoID, repoID), nil
	}
	jobs, err := actions_model.GetRunJobsByRunID(ctx, runID)
	if err != nil {
		return false, fmt.Sprintf("jobs of run %d cannot be read", runID), nil
	}
	for _, job := range jobs {
		if ok, detail := checkActionsJobRow(job, runID); !ok {
			return false, detail, nil
		}
		if job.TaskID != 0 {
			if _, err := actions_model.GetTaskByID(ctx, job.TaskID); err != nil {
				return false, fmt.Sprintf("task %d of job %d cannot be read", job.TaskID, job.ID), nil
			}
		}
	}
	return true, fmt.Sprintf("run %d with %d jobs is structurally consistent", runID, len(jobs)), nil
}

// actionsJobConsistent verifies one job row and its task reference.
//
//nolint:unparam // Keep the common operation helper and callback signature.
func (s *Service) actionsJobConsistent(ctx context.Context, jobID, runID int64) (bool, string, error) {
	job, err := actions_model.GetRunJobByID(ctx, jobID)
	if err != nil {
		return false, fmt.Sprintf("job %d for the held scope cannot be read", jobID), nil
	}
	if runID > 0 && job.RunID != runID {
		return false, fmt.Sprintf("job %d belongs to run %d, not held run %d", jobID, job.RunID, runID), nil
	}
	if ok, detail := checkActionsJobRow(job, job.RunID); !ok {
		return false, detail, nil
	}
	if job.TaskID != 0 {
		if _, err := actions_model.GetTaskByID(ctx, job.TaskID); err != nil {
			return false, fmt.Sprintf("task %d of job %d cannot be read", job.TaskID, job.ID), nil
		}
	}
	return true, fmt.Sprintf("job %d is structurally consistent", jobID), nil
}

func checkActionsJobRow(job *actions_model.ActionRunJob, runID int64) (bool, string) {
	if job.RunID != runID {
		return false, fmt.Sprintf("job %d belongs to run %d, not run %d", job.ID, job.RunID, runID)
	}
	if !validActionsStatus(job.Status) {
		return false, fmt.Sprintf("job %d has invalid status %d", job.ID, int(job.Status))
	}
	return true, ""
}

func validActionsStatus(status actions_model.Status) bool {
	return status >= actions_model.StatusUnknown && status <= actions_model.StatusBlocked
}

// recoverActionsTrust reconciles one held poster-trust update from its
// trust row and the runs it approves or cancels. Revocation deletes the
// trust row first and then cancels every unfinished run by the poster,
// so an absent trust row with no unfinished runs proves the committed
// end state, a present trust row proves nothing applied, and an absent
// trust row with unfinished runs fences as a partial revocation.
// Approval mirrors it: a present trusted row with no waiting runs
// proves the committed end state, an absent row proves nothing applied.
// The inactivity sweep is one atomic delete over an unknowable set, so
// any observed state is a valid end state and releases.
func (s *Service) recoverActionsTrust(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, claim actionsClaim) (RecoveryAssessment, error) {
	if claim.op == ActionsRunOpTrustSweep {
		assessment.Checks = append(assessment.Checks, "trust sweep is one atomic delete; any observed state is valid")
		return s.releaseRecovered(ctx, assessment, reservation, EffectActionsConsistent)
	}
	if scope.RepositoryID <= 0 || claim.posterID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "trust scope names no repository poster")
	}
	if _, err := repo_model.GetRepositoryByID(ctx, scope.RepositoryID); err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return fenced(assessment, ReasonRecoveryMissingEvidence, "repository for the held scope no longer exists")
		}
		return RecoveryAssessment{}, err
	}
	trust, err := actions_model.GetActionUserByUserIDAndRepoID(ctx, claim.posterID, scope.RepositoryID)
	present := err == nil
	if err != nil && !actions_model.IsErrUserNotExist(err) {
		return RecoveryAssessment{}, err
	}
	switch claim.op {
	case ActionsRunOpTrustRevoke:
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("trust revoke row_present=%t", present))
		if present {
			return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
		}
		runs, err := actions_model.GetRunsNotDoneByRepoIDAndPullRequestPosterID(ctx, scope.RepositoryID, claim.posterID)
		if err != nil {
			return RecoveryAssessment{}, err
		}
		if len(runs) > 0 {
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("trust revoke unfinished runs=%d", len(runs)))
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("%d unfinished runs survive a held trust revocation", len(runs)))
		}
		return s.releaseRecovered(ctx, assessment, reservation, EffectActionsConsistent)
	case ActionsRunOpTrustApprove:
		trusted := present && trust.TrustedWithPullRequests
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("trust approve row_present=%t trusted=%t", present, trusted))
		if !trusted {
			return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
		}
		var waiting []*actions_model.ActionRun
		if err := db.GetEngine(ctx).Where("repo_id=? AND pull_request_poster_id=? AND need_approval=?", scope.RepositoryID, claim.posterID, true).Find(&waiting); err != nil {
			return RecoveryAssessment{}, err
		}
		if len(waiting) > 0 {
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("trust approve waiting runs=%d", len(waiting)))
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("%d runs still wait for approval after a held trust grant", len(waiting)))
		}
		return s.releaseRecovered(ctx, assessment, reservation, EffectActionsConsistent)
	default:
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("trust operation %q has no offline reconciliation", claim.op))
	}
}

// recoverActionsScheduleBatch reconciles one held repo-wide schedule
// operation. Cleanup deletes every schedule row first in one statement,
// so present schedules prove nothing applied; with the /cancel suffix it
// then cancels the previous scheduled runs, which must all be finished
// for the committed end state. Detection rebuilds the schedule set from
// workflows in several statements over an unknowable set; the rebuild is
// idempotent and the next push to the default branch completes it, so
// any observed schedule set releases with a rebuild note.
func (s *Service) recoverActionsScheduleBatch(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, claim actionsClaim) (RecoveryAssessment, error) {
	if scope.RepositoryID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "schedule batch scope names no repository")
	}
	repo, err := repo_model.GetRepositoryByID(ctx, scope.RepositoryID)
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return fenced(assessment, ReasonRecoveryMissingEvidence, "repository for the held scope no longer exists")
		}
		return RecoveryAssessment{}, err
	}
	switch claim.op {
	case ActionsRunOpScheduleDetect:
		n, err := db.GetEngine(ctx).Where("repo_id=?", scope.RepositoryID).Count(new(actions_model.ActionSchedule))
		if err != nil {
			return RecoveryAssessment{}, err
		}
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("schedule detect rows=%d; push to the default branch to rebuild", n))
		return s.releaseRecovered(ctx, assessment, reservation, EffectActionsConsistent)
	case ActionsRunOpScheduleClean:
		n, err := db.GetEngine(ctx).Where("repo_id=?", scope.RepositoryID).Count(new(actions_model.ActionSchedule))
		if err != nil {
			return RecoveryAssessment{}, err
		}
		specs, err := db.GetEngine(ctx).Where("repo_id=?", scope.RepositoryID).Count(new(actions_model.ActionScheduleSpec))
		if err != nil {
			return RecoveryAssessment{}, err
		}
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("schedule clean remaining=%d specs=%d", n, specs))
		if n > 0 || specs > 0 {
			return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
		}
		if !claim.cancel {
			return s.releaseRecovered(ctx, assessment, reservation, EffectActionsConsistent)
		}
		runs, _, err := db.FindAndCount[actions_model.ActionRun](ctx, actions_model.FindRunOptions{
			RepoID:       scope.RepositoryID,
			Ref:          repo.DefaultBranch,
			TriggerEvent: webhook_module.HookEventSchedule,
			Status:       []actions_model.Status{actions_model.StatusRunning, actions_model.StatusWaiting, actions_model.StatusBlocked},
		})
		if err != nil {
			return RecoveryAssessment{}, err
		}
		if len(runs) > 0 {
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("schedule clean unfinished scheduled runs=%d", len(runs)))
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("%d scheduled runs survive a held schedule cleanup", len(runs)))
		}
		return s.releaseRecovered(ctx, assessment, reservation, EffectActionsConsistent)
	default:
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("schedule batch operation %q has no offline reconciliation", claim.op))
	}
}

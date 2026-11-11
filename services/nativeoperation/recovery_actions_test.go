// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestParseActionsResource(t *testing.T) {
	valid := map[string]actionsClaim{
		"task/pick":    {kind: "task"},
		"task/recover": {kind: "task"},
		"task/47":      {kind: "task"},
		"run/rerun":    {kind: "run", op: "rerun"},
		"run/cancel":   {kind: "run", op: "cancel"},
		"run/approve":  {kind: "run", op: "approve"},
		"run/emitter":  {kind: "run", op: "emitter"},
		"run/sweep":    {kind: "run", op: "sweep"},
		"run/dispatch/ci.yaml": {
			kind: "dispatch", workflow: "ci.yaml",
		},
		"run/dispatch/.forgejo/workflows/nested.yaml": {
			kind: "dispatch", workflow: ".forgejo/workflows/nested.yaml",
		},
		"run/schedule/7": {kind: "schedule", scheduleID: 7},
		"run/trust-revoke/4": {
			kind: "trust", op: ActionsRunOpTrustRevoke, posterID: 4,
		},
		"run/trust-approve/4": {
			kind: "trust", op: ActionsRunOpTrustApprove, posterID: 4,
		},
		"run/trust-sweep": {kind: "trust", op: ActionsRunOpTrustSweep},
		"run/schedule-detect": {
			kind: "schedule-batch", op: ActionsRunOpScheduleDetect,
		},
		"run/schedule-clean": {
			kind: "schedule-batch", op: ActionsRunOpScheduleClean,
		},
		"run/schedule-clean/cancel": {
			kind: "schedule-batch", op: ActionsRunOpScheduleClean, cancel: true,
		},
		"status/1234123412341234123412341234123412341234/pending/2/ci/awesomeness": {
			kind: "status", sha: "1234123412341234123412341234123412341234",
			state: "pending", creatorID: 2, context: "ci/awesomeness",
		},
	}
	for resource, want := range valid {
		claim, err := parseActionsResource(resource)
		require.NoError(t, err, resource)
		require.Equal(t, want, claim, resource)
	}

	invalid := []string{
		"",
		"task",
		"task/pick/extra",
		"task/abc",
		"run",
		"run/merge",
		"run/dispatch/",
		"run/schedule/0",
		"run/schedule/abc",
		"run/trust-revoke/0",
		"run/trust-approve/abc",
		"run/trust-sweep/extra",
		"run/schedule-clean/keep",
		"status/short/pending/2/ctx",
		"status/1234123412341234123412341234123412341234//2/ctx",
		"status/1234123412341234123412341234123412341234/pending/0/ctx",
		"status/1234123412341234123412341234123412341234/pending/2/",
		"status/pending/2/ctx",
		"push-completion",
	}
	for _, resource := range invalid {
		_, err := parseActionsResource(resource)
		require.Error(t, err, resource)
	}
}

func TestOrdinaryResource(t *testing.T) {
	got, err := execcontext.OrdinaryResource("ord:actions-run/run/rerun/0123456789abcdef", FamilyActionsRun)
	require.NoError(t, err)
	require.Equal(t, "run/rerun", got)

	got, err = execcontext.OrdinaryResource("ord:actions-run/status/abc/pending/2/a/b/0123456789abcdef", FamilyActionsRun)
	require.NoError(t, err)
	require.Equal(t, "status/abc/pending/2/a/b", got)

	for _, owner := range []string{
		"",
		"ord:actions-run/run/rerun",
		"ord:actions-run/rerun/xyz",
		"ord:authority/run/rerun/0123456789abcdef",
		"cond:install/op",
	} {
		_, err := execcontext.OrdinaryResource(owner, FamilyActionsRun)
		require.Error(t, err, owner)
	}
}

func recoverActionsRunCase(t *testing.T, owner string, scope Scope) RecoveryAssessment {
	t.Helper()
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	useIsolatedAppData(t)
	svc := admissionService(t, nil, 0)

	claimed := claimOrdinaryOwner(t, ctx, owner, scope)
	inhibitDomain(t)
	assessment, err := svc.recoverActionsRun(ctx, &RecoveryAssessment{
		Owner:      claimed.Owner,
		Generation: claimed.Generation,
		OwnerKind:  claimed.OwnerKind,
		Family:     FamilyActionsRun,
		Verdict:    RecoveryFenced,
	}, claimed, scope)
	require.NoError(t, err)
	return assessment
}

func TestRecoverActionsRunConsistent(t *testing.T) {
	assessment := recoverActionsRunCase(t, "ord:actions-run/run/cancel/0123456789abcdef", Scope{
		Kind:   nativeoperation_model.OwnerOrdinary,
		Family: FamilyActionsRun,
		RunID:  791,
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

func TestRecoverActionsRunMissing(t *testing.T) {
	assessment := recoverActionsRunCase(t, "ord:actions-run/run/emitter/0123456789abcdef", Scope{
		Kind:   nativeoperation_model.OwnerOrdinary,
		Family: FamilyActionsRun,
		RunID:  999999,
	})
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverActionsJobConsistent(t *testing.T) {
	assessment := recoverActionsRunCase(t, "ord:actions-run/run/sweep/0123456789abcdef", Scope{
		Kind:   nativeoperation_model.OwnerOrdinary,
		Family: FamilyActionsRun,
		RunID:  791,
		JobID:  192,
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

func TestRecoverActionsJobMismatch(t *testing.T) {
	assessment := recoverActionsRunCase(t, "ord:actions-run/run/sweep/0123456789abcdef", Scope{
		Kind:   nativeoperation_model.OwnerOrdinary,
		Family: FamilyActionsRun,
		RunID:  792,
		JobID:  192,
	})
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverActionsStatusCommitted(t *testing.T) {
	assessment := recoverActionsRunCase(t, "ord:actions-run/"+StatusResource("1234123412341234123412341234123412341234", "failure", 2, "ci/awesomeness")+"/0123456789abcdef", Scope{
		Kind:         nativeoperation_model.OwnerOrdinary,
		Family:       FamilyActionsRun,
		RepositoryID: 1,
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectCommitted, assessment.Effect)
}

func TestRecoverActionsStatusAbsent(t *testing.T) {
	assessment := recoverActionsRunCase(t, "ord:actions-run/"+StatusResource("1234123412341234123412341234123412341234", "pending", 2, "nope/nothing")+"/0123456789abcdef", Scope{
		Kind:         nativeoperation_model.OwnerOrdinary,
		Family:       FamilyActionsRun,
		RepositoryID: 1,
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
}

func TestRecoverActionsDispatchZero(t *testing.T) {
	assessment := recoverActionsRunCase(t, "ord:actions-run/"+DispatchResource("nope.yaml")+"/0123456789abcdef", Scope{
		Kind:         nativeoperation_model.OwnerOrdinary,
		Family:       FamilyActionsRun,
		RepositoryID: 4,
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

func TestRecoverActionsSchedule(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	require.NoError(t, actions_model.InsertRun(ctx, &actions_model.ActionRun{
		Title:         "scheduled",
		RepoID:        4,
		OwnerID:       5,
		TriggerUserID: 1,
		ScheduleID:    848484,
		Status:        actions_model.StatusWaiting,
	}, nil))

	useIsolatedAppData(t)
	svc := admissionService(t, nil, 0)
	scope := Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 4}
	claimed := claimOrdinaryOwner(t, ctx, "ord:actions-run/"+ScheduleResource(848484)+"/0123456789abcdef", scope)
	inhibitDomain(t)
	assessment, err := svc.recoverActionsRun(ctx, &RecoveryAssessment{
		Owner:      claimed.Owner,
		Generation: claimed.Generation,
		OwnerKind:  claimed.OwnerKind,
		Family:     FamilyActionsRun,
		Verdict:    RecoveryFenced,
	}, claimed, scope)
	require.NoError(t, err)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

//nolint:revive // Test helpers take testing.T before their context.
func insertPickJob(t *testing.T, ctx context.Context, runID int64, status actions_model.Status, taskID int64) *actions_model.ActionRunJob {
	t.Helper()
	job := &actions_model.ActionRunJob{
		RunID:     runID,
		RepoID:    4,
		OwnerID:   5,
		CommitSHA: "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		Name:      "pick-job",
		JobID:     "pick-job",
		Status:    status,
		TaskID:    taskID,
	}
	require.NoError(t, db.Insert(ctx, job))
	return job
}

//nolint:revive // Test helpers take testing.T before their context.
func insertPickTask(t *testing.T, ctx context.Context, jobID, runnerID int64, token string) *actions_model.ActionTask {
	t.Helper()
	task := &actions_model.ActionTask{
		JobID:     jobID,
		RunnerID:  runnerID,
		Status:    actions_model.StatusRunning,
		Started:   1683636528,
		RepoID:    4,
		OwnerID:   5,
		CommitSHA: "c2d72f548424103f01ee1dc02889c1e2bff816b0",
		TokenHash: token,
		TokenSalt: token,
	}
	require.NoError(t, db.Insert(ctx, task))
	return task
}

func TestRecoverActionsTaskPickUnassigned(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	job := insertPickJob(t, ctx, 791, actions_model.StatusWaiting, 0)

	useIsolatedAppData(t)
	svc := admissionService(t, nil, 0)
	scope := Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsTask, JobID: job.ID, RunnerID: 1}
	claimed := claimOrdinaryOwner(t, ctx, "ord:actions-task/task/pick/0123456789abcdef", scope)
	inhibitDomain(t)
	assessment, err := svc.recoverActionsTaskPick(ctx, &RecoveryAssessment{
		Owner:      claimed.Owner,
		Generation: claimed.Generation,
		OwnerKind:  claimed.OwnerKind,
		Family:     FamilyActionsTask,
		Verdict:    RecoveryFenced,
	}, claimed, scope)
	require.NoError(t, err)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
}

func recoverPickAssigned(t *testing.T, runnerID int64) RecoveryAssessment {
	t.Helper()
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	// A scheduled run skips the resulting-status check, like the task
	// recovery it mirrors.
	require.NoError(t, actions_model.InsertRun(ctx, &actions_model.ActionRun{
		Title:         "pick-scheduled",
		RepoID:        4,
		OwnerID:       5,
		TriggerUserID: 1,
		ScheduleID:    969696,
		Status:        actions_model.StatusWaiting,
	}, nil))
	runs, err := actions_model.GetRunsByScheduleID(ctx, 969696)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	job := insertPickJob(t, ctx, runs[0].ID, actions_model.StatusRunning, 0)
	task := insertPickTask(t, ctx, job.ID, 1, "pick-token-1")
	_, err = db.GetEngine(ctx).ID(job.ID).Cols("task_id").Update(&actions_model.ActionRunJob{TaskID: task.ID})
	require.NoError(t, err)

	useIsolatedAppData(t)
	svc := admissionService(t, nil, 0)
	scope := Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsTask, JobID: job.ID, RunnerID: runnerID}
	claimed := claimOrdinaryOwner(t, ctx, "ord:actions-task/task/pick/0123456789abcdef", scope)
	inhibitDomain(t)
	assessment, err := svc.recoverActionsTaskPick(ctx, &RecoveryAssessment{
		Owner:      claimed.Owner,
		Generation: claimed.Generation,
		OwnerKind:  claimed.OwnerKind,
		Family:     FamilyActionsTask,
		Verdict:    RecoveryFenced,
	}, claimed, scope)
	require.NoError(t, err)
	return assessment
}

func TestRecoverActionsTaskPickAssigned(t *testing.T) {
	assessment := recoverPickAssigned(t, 1)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, "assigned", assessment.Effect)
}

func TestRecoverActionsTaskPickForeignRunner(t *testing.T) {
	// A consistent assignment to another runner predates this owner's
	// claim: no other pick could run while it held the reservation.
	assessment := recoverPickAssigned(t, 2)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
}

func TestRecoverActionsTaskPickDangling(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	job := insertPickJob(t, ctx, 791, actions_model.StatusRunning, 99999999)

	useIsolatedAppData(t)
	svc := admissionService(t, nil, 0)
	scope := Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsTask, JobID: job.ID, RunnerID: 1}
	claimed := claimOrdinaryOwner(t, ctx, "ord:actions-task/task/pick/0123456789abcdef", scope)
	inhibitDomain(t)
	assessment, err := svc.recoverActionsTaskPick(ctx, &RecoveryAssessment{
		Owner:      claimed.Owner,
		Generation: claimed.Generation,
		OwnerKind:  claimed.OwnerKind,
		Family:     FamilyActionsTask,
		Verdict:    RecoveryFenced,
	}, claimed, scope)
	require.NoError(t, err)
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverOrdinaryRoutesActionsRun(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	useIsolatedAppData(t)
	svc := admissionService(t, nil, 0)
	scope := Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RunID: 791}
	claimed := claimOrdinaryOwner(t, ctx, "ord:actions-run/run/cancel/0123456789abcdef", scope)
	inhibitDomain(t)
	assessment, err := svc.recoverOrdinary(ctx, &RecoveryAssessment{
		Owner:      claimed.Owner,
		Generation: claimed.Generation,
		OwnerKind:  claimed.OwnerKind,
		Family:     FamilyActionsRun,
		Verdict:    RecoveryFenced,
	}, claimed, scope)
	require.NoError(t, err)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

func TestRecoverOrdinaryRoutesActionsTaskPick(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	job := insertPickJob(t, ctx, 791, actions_model.StatusWaiting, 0)

	useIsolatedAppData(t)
	svc := admissionService(t, nil, 0)
	scope := Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsTask, JobID: job.ID, RunnerID: 1}
	claimed := claimOrdinaryOwner(t, ctx, "ord:actions-task/task/pick/0123456789abcdef", scope)
	inhibitDomain(t)
	assessment, err := svc.recoverOrdinary(ctx, &RecoveryAssessment{
		Owner:      claimed.Owner,
		Generation: claimed.Generation,
		OwnerKind:  claimed.OwnerKind,
		Family:     FamilyActionsTask,
		Verdict:    RecoveryFenced,
	}, claimed, scope)
	require.NoError(t, err)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
}

func recoverTrustCase(t *testing.T, owner string, scope Scope, setup func(ctx context.Context)) RecoveryAssessment {
	t.Helper()
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	if setup != nil {
		setup(ctx)
	}
	useIsolatedAppData(t)
	svc := admissionService(t, nil, 0)
	claimed := claimOrdinaryOwner(t, ctx, owner, scope)
	inhibitDomain(t)
	assessment, err := svc.recoverActionsRun(ctx, &RecoveryAssessment{
		Owner:      claimed.Owner,
		Generation: claimed.Generation,
		OwnerKind:  claimed.OwnerKind,
		Family:     FamilyActionsRun,
		Verdict:    RecoveryFenced,
	}, claimed, scope)
	require.NoError(t, err)
	return assessment
}

func TestRecoverActionsTrustRevokeCommitted(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+TrustResource(ActionsRunOpTrustRevoke, 4)+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 1,
	}, nil)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

func TestRecoverActionsTrustRevokeNotCommitted(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+TrustResource(ActionsRunOpTrustRevoke, 4)+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 1,
	}, func(ctx context.Context) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionUser{UserID: 4, RepoID: 1})
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
}

func TestRecoverActionsTrustRevokePartialFences(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+TrustResource(ActionsRunOpTrustRevoke, 4)+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 1,
	}, func(ctx context.Context) {
		require.NoError(t, actions_model.InsertRun(ctx, &actions_model.ActionRun{
			Title:               "revoke-partial",
			RepoID:              1,
			OwnerID:             2,
			TriggerUserID:       4,
			PullRequestPosterID: 4,
			Status:              actions_model.StatusWaiting,
		}, nil))
	})
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverActionsTrustApproveCommitted(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+TrustResource(ActionsRunOpTrustApprove, 4)+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 1,
	}, func(ctx context.Context) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionUser{UserID: 4, RepoID: 1, TrustedWithPullRequests: true})
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

func TestRecoverActionsTrustApproveNotCommitted(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+TrustResource(ActionsRunOpTrustApprove, 4)+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 1,
	}, nil)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
}

func TestRecoverActionsTrustApprovePartialFences(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+TrustResource(ActionsRunOpTrustApprove, 4)+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 1,
	}, func(ctx context.Context) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionUser{UserID: 4, RepoID: 1, TrustedWithPullRequests: true})
		require.NoError(t, actions_model.InsertRun(ctx, &actions_model.ActionRun{
			Title:               "approve-partial",
			RepoID:              1,
			OwnerID:             2,
			TriggerUserID:       4,
			PullRequestPosterID: 4,
			Status:              actions_model.StatusWaiting,
			NeedApproval:        true,
		}, nil))
	})
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverActionsTrustSweep(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+TrustSweepResource()+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun,
	}, nil)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

func TestRecoverActionsScheduleCleanCommitted(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+ScheduleCleanResource(true)+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 1,
	}, nil)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

func TestRecoverActionsScheduleCleanNotCommitted(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+ScheduleCleanResource(false)+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 1,
	}, func(ctx context.Context) {
		unittest.AssertSuccessfulInsert(t, &actions_model.ActionSchedule{RepoID: 1, OwnerID: 2})
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
}

func TestRecoverActionsScheduleDetect(t *testing.T) {
	assessment := recoverTrustCase(t, "ord:actions-run/"+ScheduleDetectResource()+"/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyActionsRun, RepositoryID: 1,
	}, nil)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectActionsConsistent, assessment.Effect)
}

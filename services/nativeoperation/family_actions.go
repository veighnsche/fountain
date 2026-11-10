// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"forgejo.org/extension-sdk/gitsyntax"
)

// FamilyActionsRun is one run/job/status-level Actions logical update:
// rerun, run cancel/approval, workflow dispatch, schedule creation, one
// job-emitter batch, one abandoned/zombie sweep item or one external
// commit-status insert. Task-lifecycle updates stay in FamilyActionsTask.
// Each claim records its affected run/job identities in the existing Scope
// numeric fields; string payloads (workflow, status tuple, schedule)
// travel in a strictly parsed resource label so this family needs no Scope
// change. Offline recovery reconciles that update's actual effects from
// those identities; anything unparseable stays fenced.
const FamilyActionsRun = "actions-run"

// CrashPointActionsRunAfterEffects pauses one run-level Actions update
// after its effects commit. It is consumed through TestCrashBarrier like
// the points in crash_barrier.go; the point string lives here so this
// family needs no change to that file.
const CrashPointActionsRunAfterEffects = "actions-run-after-effects"

// Actions run-operation kinds carried in FamilyActionsRun resource labels.
// Dispatch, schedule and status carry extra payloads (see builders below);
// the rest are bare kinds with numeric identities in the Scope.
const (
	ActionsRunOpRerun    = "rerun"
	ActionsRunOpCancel   = "cancel"
	ActionsRunOpApprove  = "approve"
	ActionsRunOpEmitter  = "emitter"
	ActionsRunOpSweep    = "sweep"
	ActionsRunOpDispatch = "dispatch"
	ActionsRunOpSchedule = "schedule"
)

// Trust operations carried in FamilyActionsRun resource labels. Trust rows
// gate run approval and their updates cancel or approve runs, so they
// claim before their effects and nest under an enclosing owner (user block
// revokes trust under its authority owner). Standalone crashes reconcile
// the trust row plus the affected runs; the inactivity sweep fences.
const (
	// ActionsRunOpTrustRevoke removes one poster's trust and cancels
	// their unfinished runs. The resource names the poster.
	ActionsRunOpTrustRevoke = "trust-revoke"
	// ActionsRunOpTrustApprove grants one poster trust and approves
	// their waiting runs. The resource names the poster.
	ActionsRunOpTrustApprove = "trust-approve"
	// ActionsRunOpTrustSweep removes inactive trust rows across posters.
	// The affected set is unknowable, so recovery fences it.
	ActionsRunOpTrustSweep = "trust-sweep"
)

// Schedule-batch operations carried in FamilyActionsRun resource labels.
// Single-schedule creation keeps the run/schedule/<id> form; these cover
// repo-wide schedule detection and cleanup.
const (
	// ActionsRunOpScheduleDetect rebuilds one repository's schedules
	// from its workflows. The rebuilt set is unknowable, so recovery
	// fences it.
	ActionsRunOpScheduleDetect = "schedule-detect"
	// ActionsRunOpScheduleClean removes one repository's schedules and,
	// with the /cancel suffix, cancels its previous scheduled runs. The
	// schedule delete is one statement, so a present schedule set proves
	// nothing applied while an absent set plus cancelled runs proves the
	// committed end state.
	ActionsRunOpScheduleClean = "schedule-clean"
)

// RunResource names one run/job-level claim without string payload. The
// Scope carries the affected run/job identities.
func RunResource(op string) string {
	return "run/" + op
}

// DispatchResource names one workflow-dispatch claim. The workflow path is
// last because it may contain slashes; the Scope carries the repository.
func DispatchResource(workflow string) string {
	return "run/dispatch/" + workflow
}

// ScheduleResource names one schedule-creation claim. The Scope carries
// the repository.
func ScheduleResource(scheduleID int64) string {
	return fmt.Sprintf("run/schedule/%d", scheduleID)
}

// TrustResource names one poster-trust claim: revoke removes the trust row
// and cancels the poster's unfinished runs, approve grants trust and
// approves their waiting runs. The Scope carries the repository.
func TrustResource(op string, posterID int64) string {
	return fmt.Sprintf("run/%s/%d", op, posterID)
}

// TrustSweepResource names one inactive-trust sweep across posters. The
// Scope carries no repository: the sweep is instance-wide.
func TrustSweepResource() string {
	return "run/" + ActionsRunOpTrustSweep
}

// ScheduleDetectResource names one repo-wide schedule rebuild. The Scope
// carries the repository.
func ScheduleDetectResource() string {
	return "run/" + ActionsRunOpScheduleDetect
}

// ScheduleCleanResource names one repo-wide schedule removal. With cancel
// it also cancels the repository's previous scheduled runs. The Scope
// carries the repository.
func ScheduleCleanResource(cancel bool) string {
	if cancel {
		return "run/" + ActionsRunOpScheduleClean + "/cancel"
	}
	return "run/" + ActionsRunOpScheduleClean
}

// StatusResource names one external commit-status claim: the exact
// expected latest-status tuple. The context is last because it may
// contain slashes; the Scope carries the repository.
func StatusResource(sha, state string, creatorID int64, statusContext string) string {
	return strings.Join([]string{"status", sha, state, strconv.FormatInt(creatorID, 10), statusContext}, "/")
}

// actionsClaim is one parsed Actions resource label. Numeric identities
// stay in the Scope; only string payloads are parsed here.
type actionsClaim struct {
	kind       string // "task", "run", "dispatch", "schedule", "trust", "schedule-batch" or "status"
	op         string // run operation for kind "run", trust/schedule-batch operation otherwise
	workflow   string // dispatch workflow path
	scheduleID int64  // schedule row
	posterID   int64  // trust poster
	cancel     bool   // schedule-clean cancels previous scheduled runs
	sha        string // status commit
	state      string // status state
	creatorID  int64  // status creator
	context    string // status context
}

// parseActionsResource strictly parses one Actions resource label. Any
// deviation fences: recovery must never guess which update an owner ran.
func parseActionsResource(resource string) (actionsClaim, error) {
	var claim actionsClaim
	switch {
	case resource == "task/pick":
		claim.kind = "task"
	case resource == "task/recover":
		claim.kind = "task"
	case strings.HasPrefix(resource, "task/"):
		// The existing task/{id} form: the Scope carries the task ID.
		if _, err := strconv.ParseInt(strings.TrimPrefix(resource, "task/"), 10, 64); err != nil {
			return claim, fmt.Errorf("unparseable task resource %q", resource)
		}
		claim.kind = "task"
	case resource == "run/rerun" || resource == "run/cancel" || resource == "run/approve" ||
		resource == "run/emitter" || resource == "run/sweep":
		claim.kind = "run"
		claim.op = strings.TrimPrefix(resource, "run/")
	case strings.HasPrefix(resource, "run/dispatch/"):
		claim.kind = "dispatch"
		claim.workflow = strings.TrimPrefix(resource, "run/dispatch/")
		if claim.workflow == "" {
			return claim, errors.New("dispatch resource names no workflow")
		}
	case resource == "run/trust-sweep":
		claim.kind = "trust"
		claim.op = ActionsRunOpTrustSweep
	case strings.HasPrefix(resource, "run/trust-revoke/"):
		id, err := strconv.ParseInt(strings.TrimPrefix(resource, "run/trust-revoke/"), 10, 64)
		if err != nil || id <= 0 {
			return claim, fmt.Errorf("unparseable trust resource %q", resource)
		}
		claim.kind = "trust"
		claim.op = ActionsRunOpTrustRevoke
		claim.posterID = id
	case strings.HasPrefix(resource, "run/trust-approve/"):
		id, err := strconv.ParseInt(strings.TrimPrefix(resource, "run/trust-approve/"), 10, 64)
		if err != nil || id <= 0 {
			return claim, fmt.Errorf("unparseable trust resource %q", resource)
		}
		claim.kind = "trust"
		claim.op = ActionsRunOpTrustApprove
		claim.posterID = id
	case resource == "run/schedule-detect":
		claim.kind = "schedule-batch"
		claim.op = ActionsRunOpScheduleDetect
	case resource == "run/schedule-clean" || resource == "run/schedule-clean/cancel":
		claim.kind = "schedule-batch"
		claim.op = ActionsRunOpScheduleClean
		claim.cancel = resource == "run/schedule-clean/cancel"
	case strings.HasPrefix(resource, "run/schedule/"):
		id, err := strconv.ParseInt(strings.TrimPrefix(resource, "run/schedule/"), 10, 64)
		if err != nil || id <= 0 {
			return claim, fmt.Errorf("unparseable schedule resource %q", resource)
		}
		claim.kind = "schedule"
		claim.scheduleID = id
	case strings.HasPrefix(resource, "status/"):
		parts := strings.SplitN(resource, "/", 5)
		if len(parts) != 5 {
			return claim, fmt.Errorf("unparseable status resource %q", resource)
		}
		sha, state, creator, statusContext := parts[1], parts[2], parts[3], parts[4]
		if !validStatusSHA(sha) {
			return claim, fmt.Errorf("unparseable status resource %q", resource)
		}
		// States are structurally non-empty only: native status state
		// is an open string, so recovery compares it exactly rather
		// than validating an enum.
		creatorID, err := strconv.ParseInt(creator, 10, 64)
		if err != nil || creatorID <= 0 || state == "" || statusContext == "" {
			return claim, fmt.Errorf("unparseable status resource %q", resource)
		}
		claim.kind = "status"
		claim.sha, claim.state, claim.creatorID, claim.context = sha, state, creatorID, statusContext
	default:
		return claim, fmt.Errorf("unknown Actions resource %q", resource)
	}
	return claim, nil
}

func validStatusSHA(sha string) bool {
	return gitsyntax.FullOID(sha)
}

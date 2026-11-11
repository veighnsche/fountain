//go:build linux

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Persist original test intent across process death.
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "forgejo.org/extension-sdk"
	activities_model "forgejo.org/models/activities"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	webhook_model "forgejo.org/models/webhook"
	"forgejo.org/modules/setting"

	"github.com/stretchr/testify/require"
)

const conditionalRecoveryWorkerEnv = "FOUNTAIN_CONDITIONAL_SQL_RECOVERY_WORKER"

// This worker executes the actual submitter on the parent's existing SQLite
// file. The parent kills it at a disclosed production barrier, then reopens the
// file; a timeout or a constructed ledger row is not the crash proof.
func TestConditionalSQLRecoveryWorker(t *testing.T) {
	root := os.Getenv(conditionalRecoveryWorkerEnv)
	if root == "" {
		return
	}
	engine := openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
	t.Cleanup(func() { _ = engine.Close() })
	setting.AppDataPath, setting.RepoRootPath = filepath.Join(root, "appdata"), filepath.Join(root, "repositories")
	raw, err := os.ReadFile(filepath.Join(root, "intent.json"))
	require.NoError(t, err)
	var intent ValidIntent
	require.NoError(t, json.Unmarshal(raw, &intent))
	decision := lifecycleIssueDecision(t, intent.RepositoryID, intent.Kind)
	_, err = NewService().Submit(t.Context(), decision, operationTestInstallation, &intent)
	t.Fatalf("submission returned before the owned worker was killed: %v", err)
}

func conditionalRecoveryIntent(t *testing.T, kind string, existingState bool) *ValidIntent {
	t.Helper()
	repoID := int64(1)
	var payload []byte
	switch kind {
	case nativeoperation_model.KindLifecycleIssue:
		repoID, payload = 3, []byte(`{"issue_id":6,"label_id":3,"assignee_id":4}`)
		if existingState {
			issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 6})
			require.NoError(t, issue.LoadRepo(t.Context()))
			actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
			label := unittest.AssertExistsAndLoadBean(t, &issues_model.Label{ID: 3})
			require.NoError(t, issues_model.NewIssueLabel(t.Context(), issue, label, actor))
			_, _, err := issues_model.ToggleIssueAssignee(t.Context(), issue, actor, 4)
			require.NoError(t, err)
		}
	case nativeoperation_model.KindLifecycleReview:
		payload = []byte(`{"issue_id":2,"reviewer_ids":[2]}`)
		if existingState {
			issue := unittest.AssertExistsAndLoadBean(t, &issues_model.Issue{ID: 2})
			require.NoError(t, issue.LoadRepo(t.Context()))
			actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
			_, err := issues_model.AddReviewRequest(t.Context(), issue, actor, actor)
			require.NoError(t, err)
		}
	case nativeoperation_model.KindLifecycleNote:
		payload = []byte(`{"issue_id":5,"content":"exact stopped completion note","merge_ref":"pr/2@retained"}`)
	case nativeoperation_model.KindThreadResolve:
		root, reply, digest := resolveThread(t, NewService(), snapshotBranch2)
		if existingState {
			root.ResolveDoerID = 4
			_, err := db.GetEngine(t.Context()).ID(root.ID).Cols("resolve_doer_id").Update(root)
			require.NoError(t, err)
			digest = expectedThreadDigest(t, root, reply)
		}
		var err error
		payload, err = json.Marshal(resolvePayload(snapshotBranch2, snapshotMaster, digest, root.ID))
		require.NoError(t, err)
	default:
		t.Fatal("unexpected recovery fixture kind")
	}
	held, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	now := time.Now().Unix()
	intent, err := ValidateIntent("conditional-recovery-proof", 2, repoID, kind, "recovery-v1", held.Revision, now+300, payload, now)
	require.NoError(t, err)
	return intent
}

func TestConditionalSQLProcessDeathRecovery(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		before string
		after  string
	}{
		{nativeoperation_model.KindThreadResolve, CrashPointThreadResolveBeforePrimary, CrashPointThreadResolveAfterPrimary},
		{nativeoperation_model.KindLifecycleIssue, CrashPointLifecycleIssueBeforePrimary, CrashPointLifecycleIssueAfterPrimary},
		{nativeoperation_model.KindLifecycleReview, CrashPointLifecycleReviewBeforePrimary, CrashPointLifecycleReviewAfterPrimary},
		{nativeoperation_model.KindLifecycleNote, CrashPointLifecycleNoteBeforePrimary, CrashPointLifecycleNoteAfterPrimary},
	} {
		for _, point := range []string{tc.before, tc.after} {
			t.Run(point, func(t *testing.T) {
				checkConditionalSQLProcessRecovery(t, tc.kind, point, point == tc.after, false)
			})
		}
	}
	t.Run("already-resolved-other-actor", func(t *testing.T) {
		checkConditionalSQLProcessRecovery(t, nativeoperation_model.KindThreadResolve, CrashPointThreadResolveAfterPrimary, true, true)
	})
	t.Run("already-labeled-and-assigned", func(t *testing.T) {
		checkConditionalSQLProcessRecovery(t, nativeoperation_model.KindLifecycleIssue, CrashPointLifecycleIssueAfterPrimary, true, true)
	})
	t.Run("already-requested-reviewer", func(t *testing.T) {
		checkConditionalSQLProcessRecovery(t, nativeoperation_model.KindLifecycleReview, CrashPointLifecycleReviewAfterPrimary, true, true)
	})
}

func checkConditionalSQLProcessRecovery(t *testing.T, kind, point string, committed, existingState bool) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"appdata", "barrier", "worker-tmp"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0o700))
	}
	previous := db.DefaultContext.(db.Engined).Engine()
	previousAppData, previousRepositories := setting.AppDataPath, setting.RepoRootPath
	engine := openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
	setting.AppDataPath, setting.RepoRootPath = filepath.Join(root, "appdata"), filepath.Join(root, "repositories")
	t.Cleanup(func() {
		db.SetDefaultEngine(context.Background(), previous)
		setting.AppDataPath, setting.RepoRootPath = previousAppData, previousRepositories
		require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
		if engine != nil {
			require.NoError(t, engine.Close())
		}
	})
	require.NoError(t, db.SyncAllTables())
	require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
	unittest.PrepareTestEnv(t)
	intent := conditionalRecoveryIntent(t, kind, existingState)
	raw, err := json.Marshal(intent)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "intent.json"), raw, 0o600))
	command := exec.Command(os.Args[0], "-test.run=^TestConditionalSQLRecoveryWorker$", "-test.v")
	command.Env = append(os.Environ(), conditionalRecoveryWorkerEnv+"="+root, "TMPDIR="+filepath.Join(root, "worker-tmp"),
		"NATIVEOP_TEST_CRASH_POINT="+point, "NATIVEOP_TEST_CRASH_BARRIER_DIR="+filepath.Join(root, "barrier"))
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	require.NoError(t, command.Start())
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
		if t.Failed() {
			t.Logf("owned worker output:\n%s", output.String())
		}
	})
	entered := filepath.Join(root, "barrier", point+".entered")
	require.Eventually(t, func() bool { _, err := os.Stat(entered); return err == nil }, 20*time.Second, 10*time.Millisecond,
		"worker did not reach %s", point)
	require.NoError(t, command.Process.Kill())
	require.Error(t, command.Wait())
	reaped = true
	t.Logf("killed actual %s submitter at %s", kind, point)
	// A new engine observes only committed on-disk state after process death.
	require.NoError(t, engine.Close())
	engine = openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
	held, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	require.Equal(t, conditionalOwner(operationTestInstallation, intent.OperationID), held.Owner)
	op, err := nativeoperation_model.LookupOperation(t.Context(), operationTestInstallation, intent.OperationID)
	require.NoError(t, err)
	require.Equal(t, committed, op.Admitted)
	require.Equal(t, intent.Canonical, op.Intent)
	require.Equal(t, intent.Digest, op.IntentDigest)
	if committed {
		require.Equal(t, nativeoperation_model.EffectCommitted, op.EffectState)
		require.Equal(t, nativeoperation_model.CompletionPending, op.Completion)
	} else {
		require.Equal(t, nativeoperation_model.EffectPending, op.EffectState)
		require.Empty(t, op.Receipt)
	}
	inhibitDomain(t)
	svc := NewService()
	assertFenced := func() {
		t.Helper()
		a, err := svc.Recover(t.Context(), held.Owner, held.Generation)
		require.NoError(t, err)
		require.Equal(t, RecoveryFenced, a.Verdict, "%+v", a)
		stillHeld, err := nativeoperation_model.ReadReservation(t.Context())
		require.NoError(t, err)
		require.Equal(t, held.Owner, stillHeld.Owner)
		require.Equal(t, held.Generation, stillHeld.Generation)
	}
	// No alternate canonical capture, extra receipt data, or future scope is
	// silently adopted, even when the visible final native state looks right.
	_, err = db.GetEngine(t.Context()).ID(op.ID).Cols("intent_digest").Update(&nativeoperation_model.Operation{IntentDigest: strings.Repeat("0", 64)})
	require.NoError(t, err)
	assertFenced()
	_, err = db.GetEngine(t.Context()).ID(op.ID).Cols("intent_digest").Update(op)
	require.NoError(t, err)
	_, err = db.GetEngine(t.Context()).ID(1).Cols("scope").Update(&nativeoperation_model.Reservation{Scope: strings.TrimSuffix(held.Scope, "}") + `,"future_effect":true}`})
	require.NoError(t, err)
	assertFenced()
	_, err = db.GetEngine(t.Context()).ID(1).Cols("scope").Update(held)
	require.NoError(t, err)
	badReceipt := `{"unexpected":true}`
	if committed {
		badReceipt = strings.TrimSuffix(op.Receipt, "}") + `,"unexpected":true}`
	}
	_, err = db.GetEngine(t.Context()).ID(op.ID).Cols("receipt").Update(&nativeoperation_model.Operation{Receipt: badReceipt})
	require.NoError(t, err)
	assertFenced()
	_, err = db.GetEngine(t.Context()).ID(op.ID).Cols("receipt").Update(op)
	require.NoError(t, err)
	if committed {
		tamperConditionalNativeEvidence(t, kind, op, existingState, assertFenced)
		_, err = db.GetEngine(t.Context()).ID(op.ID).Cols("completion").Update(&nativeoperation_model.Operation{Completion: "future_completion"})
		require.NoError(t, err)
		assertFenced()
		_, err = db.GetEngine(t.Context()).ID(op.ID).Cols("completion").Update(op)
		require.NoError(t, err)
	}
	counts := conditionalRecoveryCounts(t)
	a, err := svc.Recover(t.Context(), held.Owner, held.Generation)
	require.NoError(t, err)
	require.Equal(t, RecoveryReleased, a.Verdict, "%+v", a)
	fresh, err := nativeoperation_model.LookupOperation(t.Context(), operationTestInstallation, intent.OperationID)
	require.NoError(t, err)
	if committed {
		require.Equal(t, op.Receipt, fresh.Receipt, "historical receipt bytes are preserved")
		completion := nativeoperation_model.CompletionNeedsIntervention
		if kind == nativeoperation_model.KindThreadResolve {
			completion = nativeoperation_model.CompletionComplete
			comment := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: intent.ThreadResolve.RootCommentID})
			resolver := int64(2)
			if existingState {
				resolver = 4
			}
			require.Equal(t, resolver, comment.ResolveDoerID)
		}
		require.Equal(t, completion, fresh.Completion)
	} else {
		require.Equal(t, nativeoperation_model.EffectNotCommitted, fresh.EffectState)
		require.Empty(t, fresh.Receipt)
	}
	idle, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	require.Empty(t, idle.Owner)
	require.Equal(t, held.Revision, idle.Revision)
	require.Equal(t, counts, conditionalRecoveryCounts(t), "recovery cannot replay native effects or delivery records")
	a, err = svc.Recover(t.Context(), held.Owner, held.Generation)
	require.NoError(t, err)
	require.Equal(t, RecoveryIdle, a.Verdict)
}

func conditionalRecoveryCounts(t *testing.T) []int64 {
	t.Helper()
	var counts []int64
	for _, bean := range []any{new(issues_model.Comment), new(issues_model.Review), new(issues_model.IssueLabel), new(issues_model.IssueAssignees), new(activities_model.Notification), new(webhook_model.HookTask)} {
		n, err := db.GetEngine(t.Context()).Count(bean)
		require.NoError(t, err)
		counts = append(counts, n)
	}
	return counts
}

func tamperConditionalNativeEvidence(t *testing.T, kind string, op *nativeoperation_model.Operation, existingState bool, assertFenced func()) {
	t.Helper()
	if kind == nativeoperation_model.KindLifecycleIssue {
		label := unittest.AssertExistsAndLoadBean(t, &issues_model.Label{ID: 3})
		_, err := db.GetEngine(t.Context()).ID(label.ID).Cols("repo_id", "org_id").Update(&issues_model.Label{RepoID: 1})
		require.NoError(t, err)
		assertFenced()
		_, err = db.GetEngine(t.Context()).ID(label.ID).Cols("repo_id", "org_id").Update(label)
		require.NoError(t, err)
	}
	if existingState && kind == nativeoperation_model.KindLifecycleIssue {
		link := unittest.AssertExistsAndLoadBean(t, &issues_model.IssueLabel{IssueID: 6, LabelID: 3})
		unittest.AssertSuccessfulDelete(t, link)
		assertFenced()
		unittest.AssertSuccessfulInsert(t, link)
		return
	}
	if existingState && kind == nativeoperation_model.KindLifecycleReview {
		review, err := issues_model.GetReviewByIssueIDAndUserID(t.Context(), 2, 2)
		require.NoError(t, err)
		_, err = db.GetEngine(t.Context()).ID(review.ID).Cols("type").Update(&issues_model.Review{Type: issues_model.ReviewTypeComment})
		require.NoError(t, err)
		assertFenced()
		_, err = db.GetEngine(t.Context()).ID(review.ID).Cols("type").Update(review)
		require.NoError(t, err)
		return
	}
	var comment *issues_model.Comment
	switch kind {
	case nativeoperation_model.KindThreadResolve:
		var receipt sdk.ThreadResolveReceipt
		require.NoError(t, json.Unmarshal([]byte(op.Receipt), &receipt))
		comment = unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: receipt.RootCommentID})
	case nativeoperation_model.KindLifecycleNote:
		var receipt LifecycleNoteReceipt
		require.NoError(t, json.Unmarshal([]byte(op.Receipt), &receipt))
		comment = unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: receipt.CommentID})
	case nativeoperation_model.KindLifecycleIssue:
		comment = unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: 6, Type: issues_model.CommentTypeLabel, LabelID: 3})
	case nativeoperation_model.KindLifecycleReview:
		comment = unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{IssueID: 2, Type: issues_model.CommentTypeReviewRequest, AssigneeID: 2})
	default:
		t.Fatal(fmt.Sprintf("unknown conditional evidence %s", kind))
	}
	if kind == nativeoperation_model.KindLifecycleReview {
		_, err := db.GetEngine(t.Context()).ID(comment.ID).Cols("removed_assignee").Update(&issues_model.Comment{RemovedAssignee: true})
		require.NoError(t, err)
		assertFenced()
		_, err = db.GetEngine(t.Context()).ID(comment.ID).Cols("removed_assignee").Update(comment)
		require.NoError(t, err)
	} else {
		_, err := db.GetEngine(t.Context()).ID(comment.ID).Cols("content").Update(&issues_model.Comment{Content: "different native evidence"})
		require.NoError(t, err)
		assertFenced()
		_, err = db.GetEngine(t.Context()).ID(comment.ID).Cols("content").Update(comment)
		require.NoError(t, err)
	}
}

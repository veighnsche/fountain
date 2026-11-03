//go:build linux

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Persist the exact native intent across process death.
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "forgejo.org/extension-sdk"
	activities_model "forgejo.org/models/activities"
	"forgejo.org/models/db"
	extensionauth_model "forgejo.org/models/extensionauth"
	git_model "forgejo.org/models/git"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/organization"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/queue"
	"forgejo.org/modules/setting"
	notify_service "forgejo.org/services/notify"
	"forgejo.org/services/uinotification"

	"code.forgejo.org/xorm/xorm"
	"code.forgejo.org/xorm/xorm/names"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const processRecoveryDirectoryEnv = "FOUNTAIN_ISSUE_GRAPH_PROCESS_RECOVERY_DIRECTORY"

func openProcessRecoveryDatabase(t *testing.T, path string) *xorm.Engine {
	t.Helper()
	engine, err := xorm.NewEngine("sqlite3", "file:"+path+"?_txlock=immediate&_busy_timeout=10000")
	require.NoError(t, err)
	engine.SetMapper(names.GonicMapper{})
	db.SetDefaultEngine(context.Background(), engine)
	return engine
}

// TestIssueGraphProcessRecoveryWorker attaches only to the parent's existing
// file. TestMain's independent in-memory fixture never initializes this file.
// The parent SIGKILLs this exact child at the production submission barrier.
func TestIssueGraphProcessRecoveryWorker(t *testing.T) {
	root := os.Getenv(processRecoveryDirectoryEnv)
	if root == "" {
		return
	}
	engine := openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
	t.Cleanup(func() { _ = engine.Close() })
	setting.AppDataPath = filepath.Join(root, "appdata")
	if os.Getenv("FOUNTAIN_HUMAN_REVIEW_REQUEST_PROCESS_RECOVERY") == "1" {
		setting.RepoRootPath = filepath.Join(root, "repositories")
		raw, err := os.ReadFile(filepath.Join(root, "review-request.json"))
		require.NoError(t, err)
		var q sdk.HumanReviewRequestChange
		require.NoError(t, json.Unmarshal(raw, &q))
		_, err = NewService().SubmitHumanReviewRequest(t.Context(), operationTestInstallation, q, processHumanCommentAdmission)
		t.Fatalf("human reviewer request returned before its owned child was killed: %v", err)
	}
	if os.Getenv("FOUNTAIN_HUMAN_THREAD_PROCESS_RECOVERY") == "1" {
		setting.RepoRootPath = filepath.Join(root, "repositories")
		raw, err := os.ReadFile(filepath.Join(root, "thread.json"))
		require.NoError(t, err)
		var q sdk.HumanThreadRequest
		require.NoError(t, json.Unmarshal(raw, &q))
		_, err = NewService().SubmitHumanThread(t.Context(), operationTestInstallation, q, processHumanCommentAdmission)
		t.Fatalf("human thread returned before its owned child was killed: %v", err)
	}
	if os.Getenv("FOUNTAIN_HUMAN_INLINE_PROCESS_RECOVERY") == "1" {
		setting.RepoRootPath = filepath.Join(root, "repositories")
		raw, err := os.ReadFile(filepath.Join(root, "inline.json"))
		require.NoError(t, err)
		var q sdk.HumanInlineRequest
		require.NoError(t, json.Unmarshal(raw, &q))
		_, err = NewService().SubmitHumanInline(t.Context(), operationTestInstallation, q, processHumanCommentAdmission)
		t.Fatalf("human inline returned before its owned child was killed: %v", err)
	}
	if os.Getenv("FOUNTAIN_HUMAN_REVIEW_PROCESS_RECOVERY") == "1" {
		setting.RepoRootPath = filepath.Join(root, "repositories")
		raw, err := os.ReadFile(filepath.Join(root, "review.json"))
		require.NoError(t, err)
		var q sdk.HumanReviewRequest
		require.NoError(t, json.Unmarshal(raw, &q))
		_, err = NewService().SubmitHumanReview(t.Context(), operationTestInstallation, q, processHumanCommentAdmission)
		t.Fatalf("human review returned before its owned child was killed: %v", err)
	}
	if os.Getenv("FOUNTAIN_HUMAN_STATE_PROCESS_RECOVERY") == "1" {
		raw, err := os.ReadFile(filepath.Join(root, "state.json"))
		require.NoError(t, err)
		var q sdk.HumanStateRequest
		require.NoError(t, json.Unmarshal(raw, &q))
		_, err = NewService().SubmitHumanState(t.Context(), operationTestInstallation, q, processHumanCommentAdmission)
		t.Fatalf("human state returned before its owned child was killed: %v", err)
	}
	if os.Getenv("FOUNTAIN_HUMAN_LOCK_PROCESS_RECOVERY") == "1" {
		raw, err := os.ReadFile(filepath.Join(root, "lock.json"))
		require.NoError(t, err)
		var q sdk.HumanLockRequest
		require.NoError(t, json.Unmarshal(raw, &q))
		_, err = NewService().SubmitHumanLock(t.Context(), operationTestInstallation, q, processHumanCommentAdmission)
		t.Fatalf("human lock returned before its owned child was killed: %v", err)
	}
	if os.Getenv("FOUNTAIN_HUMAN_ISSUE_CREATE_PROCESS_RECOVERY") == "1" {
		raw, err := os.ReadFile(filepath.Join(root, "create.json"))
		require.NoError(t, err)
		var q sdk.HumanIssueCreateRequest
		require.NoError(t, json.Unmarshal(raw, &q))
		_, err = NewService().SubmitHumanIssueCreate(t.Context(), operationTestInstallation, q, processHumanCommentAdmission)
		t.Fatalf("human issue creation returned before its owned child was killed: %v", err)
	}
	if os.Getenv("FOUNTAIN_HUMAN_EDIT_PROCESS_RECOVERY") == "1" {
		raw, err := os.ReadFile(filepath.Join(root, "edit.json"))
		require.NoError(t, err)
		var q sdk.HumanEditRequest
		require.NoError(t, json.Unmarshal(raw, &q))
		_, err = NewService().SubmitHumanEdit(t.Context(), operationTestInstallation, q, processHumanCommentAdmission)
		t.Fatalf("human edit returned before its owned child was killed: %v", err)
	}
	if os.Getenv("FOUNTAIN_HUMAN_COMMENT_PROCESS_RECOVERY") == "1" {
		raw, err := os.ReadFile(filepath.Join(root, "human.json"))
		require.NoError(t, err)
		var q sdk.HumanCommentRequest
		require.NoError(t, json.Unmarshal(raw, &q))
		_, err = NewService().SubmitHumanComment(t.Context(), operationTestInstallation, q, processHumanCommentAdmission)
		t.Fatalf("human comment returned before its owned child was killed: %v", err)
	}
	encoded, err := os.ReadFile(filepath.Join(root, "intent.json"))
	require.NoError(t, err)
	var intent ValidIntent
	require.NoError(t, json.Unmarshal(encoded, &intent))
	decision := graphDecision(t)
	_, err = NewService().Submit(t.Context(), decision, operationTestInstallation, &intent)
	t.Fatalf("submission returned before the parent killed its blocked child: %v", err)
}

// TestIssueGraphProcessDeathRecovery proves actual process death and reopening
// persistent SQLite state, before and after the selected graph/human operation's
// atomic primary commit. Human review, inline, thread and reviewer cases use the owned-worker harness.
// It does not simulate the crash with a barrier timeout, and does not establish
// PostgreSQL or installed-appliance shutdown behavior. Its Linux companion mode
// owns/reaps the callback integration root's active merge/PR Git/hook trees; that
// callback root supplies the persistent merge-recovery assertions.
func TestIssueGraphProcessDeathRecovery(t *testing.T) {
	if root := os.Getenv("FOUNTAIN_PULL_CREATE_PROCESS_SUPERVISOR_ROOT"); root != "" {
		superviseHumanGitProcessTree(t, root, os.Getenv("FOUNTAIN_PULL_CREATE_PROCESS_WORKER_EXECUTABLE"), "SODA_FOUNTAIN_PULL_CREATE_PROCESS_WORKER", os.Getenv("SODA_FOUNTAIN_PULL_CREATE_REF_HOOK_EXECUTABLE"), 3)
		return
	}
	if root := os.Getenv("FOUNTAIN_MERGE_PROCESS_SUPERVISOR_ROOT"); root != "" {
		superviseHumanGitProcessTree(t, root, os.Getenv("FOUNTAIN_MERGE_PROCESS_WORKER_EXECUTABLE"), "SODA_FOUNTAIN_MERGE_PROCESS_WORKER", os.Getenv("SODA_FOUNTAIN_MERGE_HOOK_EXECUTABLE"), 4)
		return
	}
	if os.Getenv("FOUNTAIN_HUMAN_REVIEW_REQUEST_PROCESS_RECOVERY") == "1" {
		checkHumanReviewRequestProcessRecovery(t)
		return
	}
	if os.Getenv("FOUNTAIN_HUMAN_THREAD_PROCESS_RECOVERY") == "1" {
		checkHumanThreadProcessRecovery(t)
		return
	}
	if os.Getenv("FOUNTAIN_HUMAN_REVIEW_PROCESS_RECOVERY") == "1" || os.Getenv("FOUNTAIN_HUMAN_INLINE_PROCESS_RECOVERY") == "1" {
		checkHumanReviewAndInlineProcessRecovery(t)
		return
	}
	state := os.Getenv("FOUNTAIN_HUMAN_STATE_PROCESS_RECOVERY") == "1"
	lock := os.Getenv("FOUNTAIN_HUMAN_LOCK_PROCESS_RECOVERY") == "1"
	create := os.Getenv("FOUNTAIN_HUMAN_ISSUE_CREATE_PROCESS_RECOVERY") == "1"
	edit := os.Getenv("FOUNTAIN_HUMAN_EDIT_PROCESS_RECOVERY") == "1"
	human := state || lock || create || edit || os.Getenv("FOUNTAIN_HUMAN_COMMENT_PROCESS_RECOVERY") == "1"
	points := []string{CrashPointIssueGraphBeforePrimary, CrashPointIssueGraphAfterPrimary}
	if human {
		points = []string{CrashPointHumanCommentBeforePrimary, CrashPointHumanCommentAfterPrimary}
	}
	if edit {
		points = []string{CrashPointHumanEditBeforePrimary, CrashPointHumanEditAfterPrimary}
	}
	if create {
		points = []string{CrashPointHumanIssueCreateBeforePrimary, CrashPointHumanIssueCreateAfterPrimary}
	}
	if lock {
		points = []string{CrashPointHumanLockBeforePrimary, CrashPointHumanLockAfterPrimary, CrashPointHumanLockBeforePrimary, CrashPointHumanLockAfterPrimary}
	}
	if state {
		points = []string{CrashPointHumanStateBeforePrimary, CrashPointHumanStateAfterPrimary, CrashPointHumanStateBeforePrimary, CrashPointHumanStateAfterPrimary}
	}
	for pointIndex, point := range points {
		name := point
		if lock {
			if pointIndex < 2 {
				name += "-lock"
			} else {
				name += "-unlock"
			}
		}
		if state {
			if pointIndex < 2 {
				name += "-close"
			} else {
				name += "-reopen"
			}
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"appdata", "barrier", "worker-tmp"} {
				require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0o700))
			}
			previous := db.DefaultContext.(db.Engined).Engine()
			previousAppData := setting.AppDataPath
			setting.AppDataPath = filepath.Join(root, "appdata")
			engine := openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
			t.Cleanup(func() {
				db.SetDefaultEngine(context.Background(), previous)
				setting.AppDataPath = previousAppData
				require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
				if engine != nil {
					require.NoError(t, engine.Close())
				}
			})
			require.NoError(t, db.SyncAllTables())
			require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
			require.NoError(t, unittest.LoadFixtures())
			var decision extensionauth_model.SubmissionDecision
			var intent *ValidIntent
			var humanRequest sdk.HumanCommentRequest
			var editRequest sdk.HumanEditRequest
			var createRequest sdk.HumanIssueCreateRequest
			var stateRequest sdk.HumanStateRequest
			var lockRequest sdk.HumanLockRequest
			if state {
				wantClose := pointIndex < 2
				initial := &issues_model.Issue{IsClosed: !wantClose}
				if !wantClose {
					initial.ClosedUnix = 123
				}
				_, err := db.GetEngine(t.Context()).ID(1).Cols("is_closed", "closed_unix").Update(initial)
				require.NoError(t, err)
				_, err = db.GetEngine(t.Context()).Insert(&issues_model.Stopwatch{IssueID: 1, UserID: 2})
				require.NoError(t, err)
				revision, err := NewService().ReadNativeRevision(t.Context())
				require.NoError(t, err)
				closeAction, reopenAction := sdk.RepositoryActionCapability{State: "allowed"}, sdk.RepositoryActionCapability{State: "denied"}
				if !wantClose {
					closeAction, reopenAction = reopenAction, closeAction
				}
				stateRequest = sdk.HumanStateRequest{OperationID: "state-process-death", Expected: sdk.RepositoryStateSnapshot{RepositoryID: "1", ActorID: "2", Subject: "issue", SubjectID: "1", IssueID: "1", Index: "1", IsClosed: !wantClose, NativeRevision: revision.Revision, CloseAction: closeAction, ReopenAction: reopenAction}, Close: wantClose, NotAfter: time.Now().Add(5 * time.Minute).Unix()}
				intent, err = humanStateIntent(stateRequest)
				require.NoError(t, err)
				raw, err := json.Marshal(stateRequest)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "state.json"), raw, 0o600))
			} else if lock {
				wantLock := pointIndex < 2
				_, err := db.GetEngine(t.Context()).ID(1).Cols("is_locked").Update(&issues_model.Issue{IsLocked: !wantLock})
				require.NoError(t, err)
				revision, err := NewService().ReadNativeRevision(t.Context())
				require.NoError(t, err)
				lockAction, unlockAction := sdk.RepositoryActionCapability{State: "allowed"}, sdk.RepositoryActionCapability{State: "denied"}
				if !wantLock {
					lockAction, unlockAction = unlockAction, lockAction
				}
				lockRequest = sdk.HumanLockRequest{OperationID: "lock-process-death", Expected: sdk.RepositoryLockSnapshot{RepositoryID: "1", ActorID: "2", Subject: "issue", SubjectID: "1", IssueID: "1", Index: "1", IsLocked: !wantLock, NativeRevision: revision.Revision, LockAction: lockAction, UnlockAction: unlockAction}, Lock: wantLock, NotAfter: time.Now().Add(5 * time.Minute).Unix()}
				intent, err = humanLockIntent(lockRequest)
				require.NoError(t, err)
				raw, err := json.Marshal(lockRequest)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "lock.json"), raw, 0o600))
			} else if create {
				revision, err := NewService().ReadNativeRevision(t.Context())
				require.NoError(t, err)
				createRequest = sdk.HumanIssueCreateRequest{OperationID: "create-process-death", Expected: sdk.RepositoryIssueCreateSnapshot{RepositoryID: "1", ActorID: "2", NativeRevision: revision.Revision, Action: sdk.RepositoryActionCapability{State: "allowed", Reason: "issue_create"}}, Title: "Created across actual process death", Body: "Original human Markdown 🌋\r\n across process death.", NotAfter: time.Now().Add(5 * time.Minute).Unix()}
				intent, err = humanIssueCreateIntent(createRequest)
				require.NoError(t, err)
				raw, err := json.Marshal(createRequest)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "create.json"), raw, 0o600))
			} else if edit {
				revision, err := NewService().ReadNativeRevision(t.Context())
				require.NoError(t, err)
				issue, err := issues_model.GetIssueByID(t.Context(), 1)
				require.NoError(t, err)
				title, body := "Edited across actual process death", "Original human Markdown 🌋\r\n across process death."
				editRequest = sdk.HumanEditRequest{OperationID: "edit-process-death", Expected: sdk.RepositoryEditSnapshot{RepositoryID: "1", ActorID: "2", Subject: "issue", SubjectID: "1", IssueID: "1", Index: "1", Title: issue.Title, BodyDigest: editBodyDigest(issue.Content), ContentVersion: int64(issue.ContentVersion), NativeRevision: revision.Revision, TitleAction: sdk.RepositoryActionCapability{State: "allowed"}, BodyAction: sdk.RepositoryActionCapability{State: "allowed"}}, Title: &title, Body: &body, NotAfter: time.Now().Add(5 * time.Minute).Unix()}
				intent, err = humanEditIntent(editRequest)
				require.NoError(t, err)
				raw, err := json.Marshal(editRequest)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "edit.json"), raw, 0o600))
			} else if human {
				revision, err := NewService().ReadNativeRevision(t.Context())
				require.NoError(t, err)
				humanRequest = sdk.HumanCommentRequest{OperationID: "human-process-death", Expected: sdk.RepositoryCommentSnapshot{RepositoryID: "1", ActorID: "2", Subject: "issue", SubjectID: "1", IssueID: "1", Index: "1", NativeRevision: revision.Revision, Action: sdk.RepositoryActionCapability{State: "allowed", Reason: "issue_comment"}}, Body: "Original native human comment across process death.", NotAfter: time.Now().Add(5 * time.Minute).Unix()}
				intent, err = humanCommentIntent(humanRequest)
				require.NoError(t, err)
				raw, err := json.Marshal(humanRequest)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "human.json"), raw, 0o600))
			} else {
				decision = graphDecision(t)
				intent = graphIntent(t, "graph-process-death", graphPayload())
			}
			encoded, err := json.Marshal(intent)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(root, "intent.json"), encoded, 0o600))
			countIssues := func() int64 {
				count, err := db.GetEngine(t.Context()).Where("repo_id=?", 1).Count(new(issues_model.Issue))
				require.NoError(t, err)
				return count
			}
			countEdges := func() int64 {
				count, err := db.GetEngine(t.Context()).Count(new(issues_model.IssueDependency))
				require.NoError(t, err)
				return count
			}
			beforeIssues, beforeEdges := countIssues(), countEdges()
			countComments := func() int64 {
				kind := issues_model.CommentTypeComment
				if edit {
					kind = issues_model.CommentTypeChangeTitle
				}
				if state {
					kind = issues_model.CommentTypeClose
					if !stateRequest.Close {
						kind = issues_model.CommentTypeReopen
					}
				}
				if lock {
					kind = issues_model.CommentTypeLock
					if !lockRequest.Lock {
						kind = issues_model.CommentTypeUnlock
					}
				}
				n, err := db.GetEngine(t.Context()).Where("issue_id=? AND type=?", 1, kind).Count(new(issues_model.Comment))
				require.NoError(t, err)
				return n
			}
			beforeComments := countComments()
			readIndex := func() int64 {
				var index db.ResourceIndex
				found, err := db.GetEngine(t.Context()).Table("issue_index").Where("group_id=?", 1).Get(&index)
				require.NoError(t, err)
				require.True(t, found)
				return index.MaxIndex
			}
			var beforeIndex int64
			if create {
				beforeIndex = readIndex()
			}
			require.NoError(t, engine.Close())
			engine = nil

			log, err := os.Create(filepath.Join(root, "worker.log"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = log.Close() })
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestIssueGraphProcessRecoveryWorker$", "-test.v", "-test.timeout=90s")
			child.Env = append(os.Environ(),
				processRecoveryDirectoryEnv+"="+root,
				"TMPDIR="+filepath.Join(root, "worker-tmp"),
				"NATIVEOP_TEST_CRASH_POINT="+point,
				"NATIVEOP_TEST_CRASH_BARRIER_DIR="+filepath.Join(root, "barrier"),
				"NATIVEOP_TEST_CRASH_TIMEOUT_SECONDS=60")
			child.Stdout, child.Stderr = log, log
			require.NoError(t, child.Start())
			exited := make(chan error, 1)
			go func() { exited <- child.Wait() }()
			reaped := false
			t.Cleanup(func() {
				if !reaped {
					_ = child.Process.Kill()
					<-exited
				}
			})
			entered := filepath.Join(root, "barrier", point+".entered")
			reached := false
			deadline := time.After(30 * time.Second)
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for !reached {
				select {
				case err := <-exited:
					reaped = true
					output, _ := os.ReadFile(filepath.Join(root, "worker.log"))
					t.Fatalf("child exited before barrier: %v\n%s", err, output)
				case <-deadline:
					output, _ := os.ReadFile(filepath.Join(root, "worker.log"))
					t.Fatalf("child never reached %s\n%s", point, output)
				case <-tick.C:
					_, err := os.Stat(entered)
					reached = err == nil
				}
			}
			require.NoError(t, child.Process.Kill())
			var killed *exec.ExitError
			select {
			case err := <-exited:
				reaped = true
				require.True(t, errors.As(err, &killed), "child must exit because it was killed")
			case <-time.After(10 * time.Second):
				t.Fatal("killed child did not exit")
			}
			status, ok := killed.ProcessState.Sys().(syscall.WaitStatus)
			require.True(t, ok)
			require.True(t, status.Signaled())
			require.Equal(t, syscall.SIGKILL, status.Signal())
			t.Logf("owned child pid=%d reached %s and was reaped after SIGKILL", child.Process.Pid, point)

			// No writer remains: the child has died, and the original connection
			// was closed before launch. Reopen without resetting any fixture rows.
			engine = openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
			held, err := nativeoperation_model.ReadReservation(t.Context())
			require.NoError(t, err)
			require.Equal(t, conditionalOwner(operationTestInstallation, intent.OperationID), held.Owner)
			require.Positive(t, held.Generation)
			interrupted, err := nativeoperation_model.LookupOperation(t.Context(), operationTestInstallation, intent.OperationID)
			require.NoError(t, err)
			require.NotNil(t, interrupted)
			committed := point == CrashPointIssueGraphAfterPrimary || point == CrashPointHumanCommentAfterPrimary || point == CrashPointHumanEditAfterPrimary || point == CrashPointHumanIssueCreateAfterPrimary || point == CrashPointHumanLockAfterPrimary || point == CrashPointHumanStateAfterPrimary
			require.Equal(t, committed, interrupted.Admitted)
			if committed {
				require.Equal(t, nativeoperation_model.EffectCommitted, interrupted.EffectState)
				require.NotEmpty(t, interrupted.Receipt)
				if create {
					require.Equal(t, beforeIssues+1, countIssues())
					require.Equal(t, beforeIndex+1, readIndex())
					require.Equal(t, beforeComments, countComments())
					require.Equal(t, beforeEdges, countEdges())
				} else if human {
					require.Equal(t, beforeComments+1, countComments())
					require.Equal(t, beforeIssues, countIssues())
					require.Equal(t, beforeEdges, countEdges())
				} else {
					require.Equal(t, beforeIssues+2, countIssues())
					require.Equal(t, beforeEdges+1, countEdges())
				}
			} else {
				require.Equal(t, nativeoperation_model.EffectPending, interrupted.EffectState)
				require.Empty(t, interrupted.Receipt)
				require.Equal(t, beforeIssues, countIssues())
				require.Equal(t, beforeEdges, countEdges())
				if human {
					require.Equal(t, beforeComments, countComments())
				}
				if create {
					require.Equal(t, beforeIndex, readIndex())
				}
			}
			inhibitDomain(t)
			svc := NewService()
			if human && committed {
				_, err = db.GetEngine(t.Context()).ID(interrupted.ID).Cols("receipt").Update(&nativeoperation_model.Operation{Receipt: `{"comment_id":0}`})
				require.NoError(t, err)
				refused, err := svc.Recover(t.Context(), held.Owner, held.Generation)
				require.NoError(t, err)
				require.Equal(t, RecoveryFenced, refused.Verdict)
				stillHeld, err := nativeoperation_model.ReadReservation(t.Context())
				require.NoError(t, err)
				require.Equal(t, held.Owner, stillHeld.Owner)
				_, err = db.GetEngine(t.Context()).ID(interrupted.ID).Cols("receipt").Update(&nativeoperation_model.Operation{Receipt: interrupted.Receipt})
				require.NoError(t, err)
			}
			if state && committed {
				var receipt sdk.HumanStateReceipt
				require.NoError(t, json.Unmarshal([]byte(interrupted.Receipt), &receipt))
				for _, part := range []string{"state", "history"} {
					if part == "state" {
						_, err = db.GetEngine(t.Context()).ID(receipt.IssueID).Cols("is_closed").Update(&issues_model.Issue{IsClosed: !stateRequest.Close})
					} else {
						_, err = db.GetEngine(t.Context()).ID(receipt.CommentID).Cols("content").Update(&issues_model.Comment{Content: "different retained history"})
					}
					require.NoError(t, err)
					refused, err := svc.Recover(t.Context(), held.Owner, held.Generation)
					require.NoError(t, err)
					require.Equal(t, RecoveryFenced, refused.Verdict)
					stillHeld, err := nativeoperation_model.ReadReservation(t.Context())
					require.NoError(t, err)
					require.Equal(t, held.Owner, stillHeld.Owner)
					if part == "state" {
						_, err = db.GetEngine(t.Context()).ID(receipt.IssueID).Cols("is_closed").Update(&issues_model.Issue{IsClosed: stateRequest.Close})
					} else {
						_, err = db.GetEngine(t.Context()).ID(receipt.CommentID).Cols("content").Update(&issues_model.Comment{Content: ""})
					}
					require.NoError(t, err)
				}
			}
			if lock && committed {
				var receipt sdk.HumanLockReceipt
				require.NoError(t, json.Unmarshal([]byte(interrupted.Receipt), &receipt))
				for _, part := range []string{"state", "history"} {
					if part == "state" {
						_, err = db.GetEngine(t.Context()).ID(receipt.IssueID).Cols("is_locked").Update(&issues_model.Issue{IsLocked: !lockRequest.Lock})
					} else {
						_, err = db.GetEngine(t.Context()).ID(receipt.CommentID).Cols("content").Update(&issues_model.Comment{Content: "different retained history"})
					}
					require.NoError(t, err)
					refused, err := svc.Recover(t.Context(), held.Owner, held.Generation)
					require.NoError(t, err)
					require.Equal(t, RecoveryFenced, refused.Verdict)
					stillHeld, err := nativeoperation_model.ReadReservation(t.Context())
					require.NoError(t, err)
					require.Equal(t, held.Owner, stillHeld.Owner)
					if part == "state" {
						_, err = db.GetEngine(t.Context()).ID(receipt.IssueID).Cols("is_locked").Update(&issues_model.Issue{IsLocked: lockRequest.Lock})
					} else {
						_, err = db.GetEngine(t.Context()).ID(receipt.CommentID).Cols("content").Update(&issues_model.Comment{Content: lockRequest.Reason})
					}
					require.NoError(t, err)
				}
			}
			assessment, err := svc.Recover(t.Context(), held.Owner, held.Generation)
			require.NoError(t, err)
			require.Equal(t, RecoveryReleased, assessment.Verdict)
			record, err := nativeoperation_model.LookupOperation(t.Context(), operationTestInstallation, intent.OperationID)
			require.NoError(t, err)
			if committed {
				require.Equal(t, nativeoperation_model.EffectCommitted, assessment.Effect)
				if lock {
					require.Equal(t, nativeoperation_model.CompletionComplete, record.Completion)
				} else {
					require.Equal(t, nativeoperation_model.CompletionNeedsIntervention, record.Completion)
				}
				require.Equal(t, interrupted.Receipt, record.Receipt)
				if state {
					var receipt sdk.HumanStateReceipt
					require.NoError(t, json.Unmarshal([]byte(record.Receipt), &receipt))
					require.True(t, sdk.ValidHumanStateReceipt(receipt, stateRequest))
					issue, err := issues_model.GetIssueByID(t.Context(), receipt.IssueID)
					require.NoError(t, err)
					require.Equal(t, stateRequest.Close, issue.IsClosed)
					comment, err := issues_model.GetCommentByID(t.Context(), receipt.CommentID)
					require.NoError(t, err)
					require.EqualValues(t, 2, comment.PosterID)
					require.Empty(t, comment.Content)
					require.Zero(t, record.TokenID)
					require.Empty(t, record.CredentialFingerprint)

				} else if lock {
					var receipt sdk.HumanLockReceipt
					require.NoError(t, json.Unmarshal([]byte(record.Receipt), &receipt))
					require.True(t, sdk.ValidHumanLockReceipt(receipt, lockRequest))
					issue, err := issues_model.GetIssueByID(t.Context(), receipt.IssueID)
					require.NoError(t, err)
					require.Equal(t, lockRequest.Lock, issue.IsLocked)
					comment, err := issues_model.GetCommentByID(t.Context(), receipt.CommentID)
					require.NoError(t, err)
					require.EqualValues(t, 2, comment.PosterID)
					require.Equal(t, lockRequest.Reason, comment.Content)
					require.Zero(t, record.TokenID)
					require.Empty(t, record.CredentialFingerprint)
				} else if create {
					var receipt sdk.HumanIssueCreateReceipt
					require.NoError(t, json.Unmarshal([]byte(record.Receipt), &receipt))
					require.True(t, sdk.ValidHumanIssueCreateReceipt(receipt, createRequest))
					issue, err := issues_model.GetIssueByID(t.Context(), receipt.IssueID)
					require.NoError(t, err)
					require.Equal(t, receipt.RepositoryID, issue.RepoID)
					require.Equal(t, receipt.ActorID, issue.PosterID)
					require.Equal(t, receipt.Index, issue.Index)
					require.Equal(t, beforeIndex+1, issue.Index)
					require.Equal(t, createRequest.Title, issue.Title)
					require.Equal(t, createRequest.Body, issue.Content)
					require.Zero(t, issue.ContentVersion)
					require.False(t, issue.IsPull)
					require.Zero(t, record.TokenID)
					require.Empty(t, record.CredentialFingerprint)
				} else if edit {
					var receipt sdk.HumanEditReceipt
					require.NoError(t, json.Unmarshal([]byte(record.Receipt), &receipt))
					require.EqualValues(t, 2, receipt.ActorID)
					require.EqualValues(t, 1, receipt.RepositoryID)
					require.Positive(t, receipt.TitleCommentID)
					issue, err := issues_model.GetIssueByID(t.Context(), 1)
					require.NoError(t, err)
					require.Equal(t, *editRequest.Title, issue.Title)
					require.Equal(t, *editRequest.Body, issue.Content)
					require.EqualValues(t, editRequest.Expected.ContentVersion+1, issue.ContentVersion)
					require.Zero(t, record.TokenID)
					require.Empty(t, record.CredentialFingerprint)
				} else if human {
					var receipt sdk.HumanCommentReceipt
					require.NoError(t, json.Unmarshal([]byte(record.Receipt), &receipt))
					require.Equal(t, int64(2), receipt.ActorID)
					require.Equal(t, int64(1), receipt.RepositoryID)
					require.Equal(t, int64(1), receipt.IssueID)
					comment, err := issues_model.GetCommentByID(t.Context(), receipt.CommentID)
					require.NoError(t, err)
					require.Equal(t, humanRequest.Body, comment.Content)
					require.Zero(t, record.TokenID)
					require.Empty(t, record.CredentialFingerprint)
				} else {
					var receipt sdk.IssueGraphReceipt
					require.NoError(t, json.Unmarshal([]byte(record.Receipt), &receipt))
					require.Equal(t, int64(2), receipt.ActorID)
					require.Equal(t, int64(1), receipt.RepositoryID)
					require.Len(t, receipt.Issues, 2)
					require.Len(t, receipt.Dependencies, 1)
					for i, entry := range receipt.Issues {
						issue, err := issues_model.GetIssueByID(t.Context(), entry.IssueID)
						require.NoError(t, err)
						require.Equal(t, intent.IssueGraph.Creates[i].Key, entry.Key)
						require.Equal(t, intent.IssueGraph.Creates[i].Title, issue.Title)
						require.Equal(t, intent.IssueGraph.Creates[i].Body, issue.Content)
						require.Equal(t, receipt.ActorID, issue.PosterID)
					}
					edge := receipt.Dependencies[0]
					require.Equal(t, receipt.Issues[1].IssueID, edge.IssueID)
					require.Equal(t, receipt.Issues[0].IssueID, edge.PrerequisiteID)
					require.False(t, edge.Removed)
				}
			} else {
				require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
				require.Equal(t, nativeoperation_model.ReasonRecoveredNoEffect, record.Reason)
			}
			idle, err := nativeoperation_model.ReadReservation(t.Context())
			require.NoError(t, err)
			require.Empty(t, idle.Owner)
			issues, edges := countIssues(), countEdges()
			comments := countComments()
			var replay sdk.OperationRecord
			if state {
				replay, err = svc.SubmitHumanState(t.Context(), operationTestInstallation, stateRequest, processHumanCommentAdmission)
			} else if lock {
				replay, err = svc.SubmitHumanLock(t.Context(), operationTestInstallation, lockRequest, processHumanCommentAdmission)
			} else if create {
				replay, err = svc.SubmitHumanIssueCreate(t.Context(), operationTestInstallation, createRequest, processHumanCommentAdmission)
			} else if edit {
				replay, err = svc.SubmitHumanEdit(t.Context(), operationTestInstallation, editRequest, processHumanCommentAdmission)
			} else if human {
				replay, err = svc.SubmitHumanComment(t.Context(), operationTestInstallation, humanRequest, processHumanCommentAdmission)
			} else {
				replay, err = svc.Submit(t.Context(), decision, operationTestInstallation, intent)
			}
			require.NoError(t, err)
			require.Equal(t, record.EffectState, replay.EffectState)
			require.Equal(t, record.Receipt, string(replay.Receipt))
			require.Equal(t, issues, countIssues(), "replay cannot create duplicate issues")
			require.Equal(t, edges, countEdges(), "replay cannot create duplicate dependencies")
			if human {
				require.Equal(t, comments, countComments(), "recovery/replay cannot republish a human comment")
				conflict := *record
				conflict.IntentDigest = "different-intent-under-the-same-operation"
				_, err := svc.recordStaleRevision(t.Context(), &conflict)
				require.ErrorIs(t, err, ErrIntentConflict, "a stale losing claim must not adopt another intent's durable receipt")
				require.Equal(t, comments, countComments())
				if state {
					issue, err := issues_model.GetIssueByID(t.Context(), 1)
					require.NoError(t, err)
					want := stateRequest.Expected.IsClosed
					if committed {
						want = stateRequest.Close
					}
					require.Equal(t, want, issue.IsClosed)
					require.True(t, issues_model.StopwatchExists(t.Context(), 2, 1), "recovery must not finish stopwatch")
					t.Log("PASS: human close/reopen SIGKILL/reopen, exact atomic state/history/receipt recovery; uncertain completion requires intervention without primary or stopwatch replay")

				} else if lock {
					issue, err := issues_model.GetIssueByID(t.Context(), 1)
					require.NoError(t, err)
					want := lockRequest.Expected.IsLocked
					if committed {
						want = lockRequest.Lock
					}
					require.Equal(t, want, issue.IsLocked)
					t.Log("PASS: human lock/unlock SIGKILL/reopen, exact atomic state/history/receipt recovery, complete bookkeeping without primary replay")
				} else if create {
					wantIndex := beforeIndex
					if committed {
						wantIndex++
					}
					require.Equal(t, wantIndex, readIndex())
					t.Log("PASS: human issue creation SIGKILL/reopen, exact native issue/index/receipt recovery, no duplicate creation or notification retry")
				} else if edit {
					row, err := issues_model.GetIssueByID(t.Context(), 1)
					require.NoError(t, err)
					if !committed {
						require.Equal(t, editRequest.Expected.Title, row.Title)
						require.Equal(t, editRequest.Expected.BodyDigest, editBodyDigest(row.Content))
						require.EqualValues(t, editRequest.Expected.ContentVersion, row.ContentVersion)
					}
					t.Log("PASS: human edit SIGKILL/reopen, exact content/history/receipt recovery, no primary replay or completion retry")
				} else {
					t.Log("PASS: human comment SIGKILL/reopen, exact receipt recovery, no primary replay or notification retry")
				}
			}
		})
	}
}

// The existing Linux integration root supplies subreaper ownership for the
// callback package's real repository writer. Keeping this here avoids a service
// import cycle and Linux-only syscalls in the portable callback integration root.
func superviseHumanGitProcessTree(t *testing.T, root, executable, workerVariable, hookExecutable string, minimum int) {
	t.Helper()
	require.NoError(t, unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0))
	worker := exec.CommandContext(t.Context(), executable, "-test.run=^TestRepositoryIssuesCallbackUsesHumanAccessWithoutFactoryBinding$", "-test.v", "-test.timeout=90s")
	worker.Env = append(os.Environ(), workerVariable+"="+root)
	worker.Stdout, worker.Stderr = os.Stdout, os.Stderr
	require.NoError(t, worker.Start())
	exited := make(chan error, 1)
	go func() { exited <- worker.Wait() }()
	type processProof struct {
		PID    int
		Name   string
		Reaped bool
	}
	var proof []processProof
	reaped := false
	// Stop parents before inspecting children, so this exact owned tree cannot
	// fork or reap underneath discovery. No process-name or global group kill.
	var freeze func(int)
	freeze = func(pid int) {
		require.Less(t, len(proof), 64, "unexpected owned fixture process count")
		require.NoError(t, unix.Kill(pid, unix.SIGSTOP))
		deadline := time.Now().Add(5 * time.Second)
		for {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			require.NoError(t, err)
			fields := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])
			require.NotEmpty(t, fields)
			if fields[0] == "T" || fields[0] == "t" || fields[0] == "Z" {
				break
			}
			require.True(t, time.Now().Before(deadline), "owned process did not stop")
			time.Sleep(5 * time.Millisecond)
		}
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil {
			exe = "exited-owned-child"
		}
		proof = append(proof, processProof{PID: pid, Name: filepath.Base(exe)})
		paths, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
		require.NoError(t, err)
		seen := map[int]bool{}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			require.NoError(t, err)
			for _, value := range strings.Fields(string(raw)) {
				child, err := strconv.Atoi(value)
				require.NoError(t, err)
				if !seen[child] {
					seen[child] = true
					freeze(child)
				}
			}
		}
	}
	t.Cleanup(func() {
		// Even a failed assertion must retire the owned tree. The root stays
		// our child until Wait; stopped descendants cannot have reused PIDs.
		if !reaped {
			if len(proof) == 0 {
				freeze(worker.Process.Pid)
			}
			for i := len(proof) - 1; i >= 0; i-- {
				_ = unix.Kill(proof[i].PID, unix.SIGKILL)
			}
			<-exited
		}
		for i, child := range proof {
			if i > 0 && !child.Reaped {
				_, _ = unix.Wait4(child.PID, nil, 0, nil)
			}
		}
	})
	point := os.Getenv("NATIVEOP_TEST_CRASH_POINT")
	enteredPath := filepath.Join(root, "barrier", point+".entered")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if raw, err := os.ReadFile(enteredPath); err == nil {
			require.Equal(t, strconv.Itoa(worker.Process.Pid), strings.TrimSpace(string(raw)))
			break
		}
		select {
		case err := <-exited:
			reaped = true
			t.Fatalf("owned Git worker exited before active hook barrier: %v", err)
		default:
		}
		require.True(t, time.Now().Before(deadline), "owned Git worker never reached active hook barrier")
		time.Sleep(10 * time.Millisecond)
	}
	freeze(worker.Process.Pid)
	require.GreaterOrEqual(t, len(proof), minimum, "must kill a real writer/Git/hook process tree")
	gitFound, hookFound := false, false
	for _, child := range proof[1:] {
		gitFound = gitFound || child.Name == "git"
		hookFound = hookFound || child.Name == filepath.Base(hookExecutable)
	}
	require.True(t, gitFound)
	require.True(t, hookFound)
	for i := len(proof) - 1; i >= 0; i-- {
		require.NoError(t, unix.Kill(proof[i].PID, unix.SIGKILL))
	}
	select {
	case err := <-exited:
		reaped = true
		var killed *exec.ExitError
		require.ErrorAs(t, err, &killed)
		require.Equal(t, "signal: killed", killed.ProcessState.String())
		proof[0].Reaped = true
	case <-time.After(10 * time.Second):
		t.Fatal("owned Git worker did not exit after tree SIGKILL")
	}
	// Orphaned descendants are now our children. Reap only their captured PIDs;
	// do not compete with unrelated exec.Cmd waiters via wait(-1).
	for i := 1; i < len(proof); i++ {
		deadline := time.Now().Add(10 * time.Second)
		for {
			var status unix.WaitStatus
			pid, err := unix.Wait4(proof[i].PID, &status, unix.WNOHANG, nil)
			if err == unix.EINTR {
				continue
			}
			require.NoError(t, err)
			if pid != 0 {
				require.True(t, status.Signaled())
				require.Equal(t, unix.SIGKILL, status.Signal())
				proof[i].Reaped = true
				break
			}
			require.True(t, time.Now().Before(deadline), "owned descendant was not reaped")
			time.Sleep(5 * time.Millisecond)
		}
	}
	for _, child := range proof {
		_, err := os.Stat(fmt.Sprintf("/proc/%d", child.PID))
		require.True(t, os.IsNotExist(err), "owned PID remained after wait")
	}
	raw, err := json.Marshal(proof)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "tree-reaped.json"), raw, 0o600))
	t.Logf("SIGKILL/reaped %d owned server/Git/hook processes at %s", len(proof), point)
}

// Use the existing worker/test root with real persistent SQL and Git. Only this
// selected review branch runs; earlier process families keep their qualification.
func checkHumanReviewAndInlineProcessRecovery(t *testing.T) {
	t.Helper()
	inlineWork := os.Getenv("FOUNTAIN_HUMAN_INLINE_PROCESS_RECOVERY") == "1"
	modes := []string{"draft-create", "draft-update", "final"}
	points := []string{CrashPointHumanReviewBeforePrimary, CrashPointHumanReviewAfterPrimary}
	if inlineWork {
		modes = append(modes, "final-fresh")
		points = []string{CrashPointHumanInlineBeforePrimary, CrashPointHumanInlineAfterPrimary}
	}
	for _, mode := range modes {
		for _, point := range points {
			t.Run(mode+"-"+point, func(t *testing.T) {
				fresh := mode == "draft-create" || mode == "final-fresh"
				final := mode == "final" || mode == "final-fresh"
				root := t.TempDir()
				for _, name := range []string{"appdata", "barrier", "worker-tmp", "repositories", "work"} {
					require.NoError(t, os.Mkdir(filepath.Join(root, name), 0o700))
				}
				ctx := t.Context()
				previous := db.DefaultContext.(db.Engined).Engine()
				previousAppData, previousRepoRoot := setting.AppDataPath, setting.RepoRootPath
				setting.AppDataPath, setting.RepoRootPath = filepath.Join(root, "appdata"), filepath.Join(root, "repositories")
				engine := openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
				t.Cleanup(func() {
					if engine != nil {
						require.NoError(t, engine.Close())
					}
					db.SetDefaultEngine(context.Background(), previous)
					setting.AppDataPath, setting.RepoRootPath = previousAppData, previousRepoRoot
					require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
				})
				require.NoError(t, db.SyncAllTables())
				require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
				require.NoError(t, unittest.LoadFixtures())
				owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
				mentioned := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
				repo := &repo_model.Repository{OwnerID: 1, OwnerName: owner.Name, Name: "process-human-review", LowerName: "process-human-review", IsPrivate: true}
				_, err := db.GetEngine(ctx).Insert(repo)
				require.NoError(t, err)
				for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
					_, err = db.GetEngine(ctx).Insert(&repo_model.RepoUnit{RepoID: repo.ID, Type: kind})
					require.NoError(t, err)
				}
				for _, actor := range []int64{2, 4} {
					_, err = db.GetEngine(ctx).Insert(&access_model.Access{RepoID: repo.ID, UserID: actor, Mode: perm.AccessModeRead}, &repo_model.Collaboration{RepoID: repo.ID, UserID: actor, Mode: perm.AccessModeRead})
					require.NoError(t, err)
				}
				issue := &issues_model.Issue{RepoID: repo.ID, Index: 2, PosterID: 1, IsPull: true, Title: "persistent review target"}
				_, err = db.GetEngine(ctx).Insert(issue)
				require.NoError(t, err)
				pr := &issues_model.PullRequest{IssueID: issue.ID, Index: 2, BaseRepoID: repo.ID, HeadRepoID: 6, BaseBranch: "main", HeadBranch: "private-source-not-needed"}
				_, err = db.GetEngine(ctx).Insert(pr)
				require.NoError(t, err)
				require.NotEqual(t, pr.ID, issue.ID)
				gitRun := func(dir string, args ...string) string {
					t.Helper()
					command := exec.CommandContext(ctx, "git", args...)
					command.Dir = dir
					command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Native review fixture", "GIT_AUTHOR_EMAIL=review-fixture@example.invalid", "GIT_COMMITTER_NAME=Native review fixture", "GIT_COMMITTER_EMAIL=review-fixture@example.invalid", "GIT_CONFIG_NOSYSTEM=1")
					out, err := command.CombinedOutput()
					require.NoError(t, err, string(out))
					return strings.TrimSpace(string(out))
				}
				work := filepath.Join(root, "work")
				gitRun(work, "init", "--initial-branch=main")
				commit := func(body, message string) string {
					t.Helper()
					require.NoError(t, os.WriteFile(filepath.Join(work, "README"), []byte(body), 0o600))
					gitRun(work, "add", "README")
					gitRun(work, "commit", "-m", message)
					return gitRun(work, "rev-parse", "HEAD")
				}
				base := commit("base line\nbase two\ncontext three\ncontext four\n", "base")
				oldHead := commit("old reviewed line\nold two\ncontext three\ncontext four\n", "old draft candidate")
				head := commit("current reviewed line\ncurrent two\ncontext three\ncontext four\n", "current candidate")
				require.NoError(t, os.MkdirAll(filepath.Dir(repo.RepoPath()), 0o700))
				gitRun(work, "clone", "--bare", work, repo.RepoPath())
				gitRun(repo.RepoPath(), "update-ref", "refs/heads/main", base)
				gitRun(repo.RepoPath(), "update-ref", pr.GetGitRefName(), head)
				var pending *issues_model.Review
				var inline *issues_model.Comment
				var unknown *issues_model.Comment
				if !fresh {
					pending = &issues_model.Review{IssueID: issue.ID, ReviewerID: 2, Type: issues_model.ReviewTypePending, Content: "Original unsent review 🌋\r\n", CommitID: oldHead, Stale: true}
					_, err = db.GetEngine(ctx).Insert(pending)
					require.NoError(t, err)
					inline = &issues_model.Comment{IssueID: issue.ID, ReviewID: pending.ID, PosterID: 2, Type: issues_model.CommentTypeCode, Content: "Original retained inline 完整", TreePath: "README", Line: 1, CommitSHA: oldHead, OriginalAnchor: &issues_model.CodeCommentAnchor{Base: base, Head: oldHead, Path: "README", Side: "new", Scope: "lines", StartLine: 1, EndLine: 1}}
					_, err = db.GetEngine(ctx).Insert(inline)
					require.NoError(t, err)
					if inlineWork {
						unknown = &issues_model.Comment{IssueID: issue.ID, ReviewID: pending.ID, PosterID: 2, Type: issues_model.CommentTypeCode, Content: "Original retained unknown context", TreePath: "README", Line: -1, CommitSHA: base}
						_, err = db.GetEngine(ctx).Insert(unknown)
						require.NoError(t, err)
					}
				}
				revision, err := NewService().ReadNativeRevision(ctx)
				require.NoError(t, err)
				require.True(t, revision.Idle)
				action := sdk.RepositoryActionCapability{State: "allowed", Reason: "native process fixture"}
				snapshot := sdk.RepositoryReviewSnapshot{RepositoryID: strconv.FormatInt(repo.ID, 10), ActorID: "2", PullID: strconv.FormatInt(pr.ID, 10), IssueID: strconv.FormatInt(issue.ID, 10), Index: "2", BaseBranch: "main", HeadOID: head, BaseOID: base, MergeBaseOID: base, NativeRevision: revision.Revision, CommentAction: action, DraftAction: action, ApproveAction: action, RequestChangesAction: action}
				if pending != nil {
					snapshot.PendingReviewID, snapshot.PendingCommitID = strconv.FormatInt(pending.ID, 10), oldHead
				}
				event := sdk.HumanReviewEventDraft
				if final {
					event = sdk.HumanReviewEventComment
				}
				q := sdk.HumanReviewRequest{OperationID: "human-review-process-" + mode, Expected: snapshot, NotAfter: time.Now().Add(5 * time.Minute).Unix(), Event: event, Body: "@" + mentioned.Name + " Original " + mode + " 完整 🌋\r\n"}
				var inlineRequest sdk.HumanInlineRequest
				var intent *ValidIntent
				var raw []byte
				requestFile, summary := "review.json", q.Body
				if inlineWork {
					selection := issues_model.CodeCommentAnchor{Base: base, Head: head, Path: "README", Side: "new", Scope: "lines", StartLine: 1, EndLine: 2}
					if mode == "draft-create" || mode == "final" {
						selection.Scope, selection.StartLine, selection.EndLine = "file", 0, 0
						if final {
							selection.Side = "old"
						}
					}
					native, err := git.OpenRepository(ctx, repo.RepoPath())
					require.NoError(t, err)
					proof, err := ProveHumanInlineAnchor(ctx, native, selection)
					native.Close()
					require.NoError(t, err)
					inlineRequest = sdk.HumanInlineRequest{OperationID: "human-inline-process-" + mode, Expected: sdk.RepositoryInlineSnapshot{Review: snapshot, Anchor: *threadOriginalAnchor(&selection), EntryOID: proof.EntryOID, EntryKind: proof.EntryKind, DiffDigest: proof.DiffDigest}, Draft: !final, Body: q.Body, NotAfter: q.NotAfter}
					intent, err = humanInlineIntent(inlineRequest)
					require.NoError(t, err)
					raw, err = json.Marshal(inlineRequest)
					requestFile, summary = "inline.json", ""
					if pending != nil {
						summary = pending.Content
					}
				} else {
					intent, err = humanReviewIntent(q)
					require.NoError(t, err)
					raw, err = json.Marshal(q)
				}
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, requestFile), raw, 0o600))
				type proof struct {
					Reviews, History, Code, Mentions int64
					RowsDigest                       string
				}
				readProof := func() proof {
					t.Helper()
					var p proof
					p.Reviews, err = db.GetEngine(ctx).Where("issue_id=?", issue.ID).Count(new(issues_model.Review))
					require.NoError(t, err)
					p.History, err = db.GetEngine(ctx).Where("issue_id=? AND type=?", issue.ID, issues_model.CommentTypeReview).Count(new(issues_model.Comment))
					require.NoError(t, err)
					p.Code, err = db.GetEngine(ctx).Where("issue_id=? AND type=?", issue.ID, issues_model.CommentTypeCode).Count(new(issues_model.Comment))
					require.NoError(t, err)
					p.Mentions, err = db.GetEngine(ctx).Where("issue_id=? AND uid=? AND is_mentioned=?", issue.ID, 4, true).Count(new(issues_model.IssueUser))
					require.NoError(t, err)
					var rows []*issues_model.Review
					require.NoError(t, db.GetEngine(ctx).Where("issue_id=?", issue.ID).OrderBy("id ASC").Find(&rows))
					type rowProof struct {
						ID              int64
						Kind            issues_model.ReviewType
						Commit, Body    string
						Stale, Official bool
						CommentsCount   int64
						CommentsDigest  string
					}
					evidence := make([]rowProof, 0, len(rows))
					for _, r := range rows {
						count, digest, err := humanReviewCommentsEvidence(ctx, r)
						require.NoError(t, err)
						evidence = append(evidence, rowProof{r.ID, r.Type, r.CommitID, sdk.HumanReviewBodyDigest(r.Content), r.Stale, r.Official, count, digest})
					}
					encoded, e := json.Marshal(evidence)
					require.NoError(t, e)
					p.RowsDigest = sdk.HumanReviewBodyDigest(string(encoded))
					return p
				}
				before := readProof()
				require.Zero(t, before.Mentions)
				require.NoError(t, engine.Close())
				engine = nil
				log, err := os.Create(filepath.Join(root, "worker.log"))
				require.NoError(t, err)
				t.Cleanup(func() { _ = log.Close() })
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIssueGraphProcessRecoveryWorker$", "-test.v", "-test.timeout=90s")
				child.Env = append(os.Environ(), processRecoveryDirectoryEnv+"="+root, "TMPDIR="+filepath.Join(root, "worker-tmp"), "NATIVEOP_TEST_CRASH_POINT="+point, "NATIVEOP_TEST_CRASH_BARRIER_DIR="+filepath.Join(root, "barrier"), "NATIVEOP_TEST_CRASH_TIMEOUT_SECONDS=60")
				child.Stdout, child.Stderr = log, log
				require.NoError(t, child.Start())
				exited := make(chan error, 1)
				go func() { exited <- child.Wait() }()
				reaped := false
				t.Cleanup(func() {
					if !reaped {
						_ = child.Process.Kill()
						<-exited
					}
				})
				deadline := time.After(30 * time.Second)
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				reached := false
				for !reached {
					select {
					case err := <-exited:
						reaped = true
						output, _ := os.ReadFile(filepath.Join(root, "worker.log"))
						t.Fatalf("owned review worker exited before %s: %v\n%s", point, err, output)
					case <-deadline:
						output, _ := os.ReadFile(filepath.Join(root, "worker.log"))
						t.Fatalf("owned review worker never reached %s\n%s", point, output)
					case <-tick.C:
						_, err := os.Stat(filepath.Join(root, "barrier", point+".entered"))
						reached = err == nil
					}
				}
				entered, err := os.ReadFile(filepath.Join(root, "barrier", point+".entered"))
				require.NoError(t, err)
				require.Equal(t, strconv.Itoa(child.Process.Pid), strings.TrimSpace(string(entered)))
				require.NoError(t, child.Process.Kill())
				var killed *exec.ExitError
				select {
				case err := <-exited:
					reaped = true
					require.True(t, errors.As(err, &killed))
				case <-time.After(10 * time.Second):
					t.Fatal("killed owned review worker did not exit")
				}
				status, ok := killed.ProcessState.Sys().(syscall.WaitStatus)
				require.True(t, ok)
				require.True(t, status.Signaled())
				require.Equal(t, syscall.SIGKILL, status.Signal())
				t.Logf("owned child pid=%d reached %s and was reaped after SIGKILL; reopening same native file", child.Process.Pid, point)
				engine = openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
				held, err := nativeoperation_model.ReadReservation(ctx)
				require.NoError(t, err)
				require.Equal(t, conditionalOwner(operationTestInstallation, intent.OperationID), held.Owner)
				require.Positive(t, held.Generation)
				op, err := nativeoperation_model.LookupOperation(ctx, operationTestInstallation, intent.OperationID)
				require.NoError(t, err)
				require.NotNil(t, op)
				require.Equal(t, intent.Canonical, op.Intent)
				require.Zero(t, op.TokenID)
				require.Empty(t, op.CredentialFingerprint)
				committed := point == points[1]
				require.Equal(t, committed, op.Admitted)
				var receipt sdk.HumanReviewReceipt
				var inlineReceipt sdk.HumanInlineReceipt
				var reviewID, historyID int64
				if committed {
					require.Equal(t, nativeoperation_model.EffectCommitted, op.EffectState)
					if inlineWork {
						require.NoError(t, json.Unmarshal([]byte(op.Receipt), &inlineReceipt))
						require.True(t, sdk.ValidHumanInlineReceipt(inlineReceipt, inlineRequest))
						reviewID, historyID = inlineReceipt.ReviewID, inlineReceipt.HistoryID
					} else {
						require.NoError(t, json.Unmarshal([]byte(op.Receipt), &receipt))
						require.True(t, sdk.ValidHumanReviewReceipt(receipt, q))
						reviewID, historyID = receipt.ReviewID, receipt.CommentID
					}
					after := readProof()
					wantReviews, wantHistory := before.Reviews, before.History
					if fresh {
						wantReviews++
					}
					if final {
						wantHistory++
					}
					require.Equal(t, wantReviews, after.Reviews)
					require.Equal(t, wantHistory, after.History)
					wantCode := before.Code
					if inlineWork {
						wantCode++
					}
					require.Equal(t, wantCode, after.Code)
					require.Zero(t, after.Mentions)
				} else {
					require.Equal(t, nativeoperation_model.EffectPending, op.EffectState)
					require.Empty(t, op.Receipt)
					require.Equal(t, before, readProof())
				}
				inhibitDomain(t)
				svc := NewService()
				assertFenced := func() {
					t.Helper()
					a, err := svc.Recover(ctx, held.Owner, held.Generation)
					require.NoError(t, err)
					require.Equal(t, RecoveryFenced, a.Verdict)
					still, err := nativeoperation_model.ReadReservation(ctx)
					require.NoError(t, err)
					require.Equal(t, held.Owner, still.Owner)
				}
				if committed {
					_, err = db.GetEngine(ctx).ID(op.ID).Cols("receipt").Update(&nativeoperation_model.Operation{Receipt: `{"review_id":0}`})
					require.NoError(t, err)
					assertFenced()
					_, err = db.GetEngine(ctx).ID(op.ID).Cols("receipt").Update(&nativeoperation_model.Operation{Receipt: op.Receipt})
					require.NoError(t, err)
					_, err = db.GetEngine(ctx).ID(reviewID).Cols("content").Update(&issues_model.Review{Content: "different native review evidence"})
					require.NoError(t, err)
					assertFenced()
					_, err = db.GetEngine(ctx).ID(reviewID).Cols("content").Update(&issues_model.Review{Content: summary})
					require.NoError(t, err)
					if historyID > 0 {
						_, err = db.GetEngine(ctx).NoAutoTime().ID(historyID).Cols("content").Update(&issues_model.Comment{Content: "different native review history"})
						require.NoError(t, err)
						assertFenced()
						_, err = db.GetEngine(ctx).NoAutoTime().ID(historyID).Cols("content").Update(&issues_model.Comment{Content: summary})
						require.NoError(t, err)
					}
					if inline != nil {
						_, err = db.GetEngine(ctx).NoAutoTime().ID(inline.ID).Cols("content").Update(&issues_model.Comment{Content: "different retained inline"})
						require.NoError(t, err)
						assertFenced()
						_, err = db.GetEngine(ctx).NoAutoTime().ID(inline.ID).Cols("content").Update(&issues_model.Comment{Content: inline.Content})
						require.NoError(t, err)
						changed := *inline.OriginalAnchor
						changed.Head = head
						_, err = db.GetEngine(ctx).NoAutoTime().ID(inline.ID).Cols("original_anchor").Update(&issues_model.Comment{OriginalAnchor: &changed})
						require.NoError(t, err)
						assertFenced()
						_, err = db.GetEngine(ctx).NoAutoTime().ID(inline.ID).Cols("original_anchor").Update(&issues_model.Comment{OriginalAnchor: inline.OriginalAnchor})
						require.NoError(t, err)
					}
					if inlineWork {
						row, err := issues_model.GetCommentByID(ctx, inlineReceipt.CommentID)
						require.NoError(t, err)
						changed := row.OriginalAnchor.Copy()
						if changed.Side == "old" {
							changed.Side = "new"
						} else {
							changed.Side = "old"
						}
						_, err = db.GetEngine(ctx).NoAutoTime().ID(row.ID).Cols("original_anchor").Update(&issues_model.Comment{OriginalAnchor: changed})
						require.NoError(t, err)
						assertFenced()
						_, err = db.GetEngine(ctx).NoAutoTime().ID(row.ID).Cols("original_anchor").Update(row)
						require.NoError(t, err)
						_, err = db.GetEngine(ctx).NoAutoTime().ID(row.ID).Cols("content_version").Update(&issues_model.Comment{ContentVersion: row.ContentVersion + 1})
						require.NoError(t, err)
						assertFenced()
						_, err = db.GetEngine(ctx).NoAutoTime().ID(row.ID).Cols("content_version").Update(row)
						require.NoError(t, err)
					}
				}
				// A second close/reopen ensures reconciliation uses retained SQL,
				// not worker or parent objects left over from primary execution.
				require.NoError(t, engine.Close())
				engine = openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
				svc = NewService()
				assessment, err := svc.Recover(ctx, held.Owner, held.Generation)
				require.NoError(t, err)
				require.Equal(t, RecoveryReleased, assessment.Verdict)
				record, err := nativeoperation_model.LookupOperation(ctx, operationTestInstallation, intent.OperationID)
				require.NoError(t, err)
				require.NotNil(t, record)
				if committed {
					require.Equal(t, op.Receipt, record.Receipt)
					require.Equal(t, nativeoperation_model.EffectCommitted, record.EffectState)
					completion := nativeoperation_model.CompletionComplete
					if final {
						completion = nativeoperation_model.CompletionNeedsIntervention
					}
					require.Equal(t, completion, record.Completion)
					row, err := issues_model.GetReviewByID(ctx, reviewID)
					require.NoError(t, err)
					require.Equal(t, summary, row.Content)
					if mode == "draft-update" {
						require.Equal(t, oldHead, row.CommitID)
						require.True(t, row.Stale)
					} else {
						require.Equal(t, head, row.CommitID)
					}
					if inlineWork {
						created, err := issues_model.GetCommentByID(ctx, inlineReceipt.CommentID)
						require.NoError(t, err)
						require.Equal(t, inlineRequest.Body, created.Content)
						require.Equal(t, inlineRequest.Expected.Anchor, *threadOriginalAnchor(created.OriginalAnchor))
						line, commit := inlineRequest.Expected.Anchor.EndLine, head
						if inlineRequest.Expected.Anchor.Side == "old" {
							line, commit = -line, base
						}
						require.Equal(t, line, created.Line)
						require.Equal(t, commit, created.CommitSHA)
						if final {
							history, err := issues_model.GetCommentByID(ctx, historyID)
							require.NoError(t, err)
							require.Equal(t, summary, history.Content)
						}
					}
				} else {
					require.Equal(t, nativeoperation_model.EffectNotCommitted, record.EffectState)
					require.Equal(t, before, readProof())
				}
				if inline != nil {
					kept, err := issues_model.GetCommentByID(ctx, inline.ID)
					require.NoError(t, err)
					require.Equal(t, inline.OriginalAnchor, kept.OriginalAnchor)
					require.Equal(t, oldHead, kept.CommitSHA)
					require.Equal(t, inline.Content, kept.Content)
				}
				if unknown != nil {
					kept, err := issues_model.GetCommentByID(ctx, unknown.ID)
					require.NoError(t, err)
					require.Nil(t, kept.OriginalAnchor)
					require.Equal(t, unknown.Content, kept.Content)
					require.Equal(t, unknown.CommitSHA, kept.CommitSHA)
				}
				idle, err := nativeoperation_model.ReadReservation(ctx)
				require.NoError(t, err)
				require.Empty(t, idle.Owner)
				require.NoError(t, os.Remove(nativeoperation_model.OfflineMarkerPath()))
				after := readProof()
				var replay sdk.OperationRecord
				if inlineWork {
					replay, err = svc.SubmitHumanInline(ctx, operationTestInstallation, inlineRequest, processHumanCommentAdmission)
				} else {
					replay, err = svc.SubmitHumanReview(ctx, operationTestInstallation, q, processHumanCommentAdmission)
				}
				require.NoError(t, err)
				require.Equal(t, record.Receipt, string(replay.Receipt))
				require.Equal(t, record.EffectState, replay.EffectState)
				require.Equal(t, record.Completion, replay.CompletionState)
				require.Equal(t, after, readProof())
				require.Zero(t, readProof().Mentions, "recovery/replay repeated uncertain completion")
				t.Logf("PASS: %s %s, persistent review/history/inline evidence retained, exact fencing and no primary/completion replay", mode, point)
			})
		}
	}
}

func checkHumanReviewRequestProcessRecovery(t *testing.T) {
	t.Helper()
	// Use the real UI queue/database as an observer. Recovery and replay must
	// leave its read marker intact even where native notification work is pending.
	notifier := uinotification.NewNotifier()
	notify_service.RegisterNotifier(notifier)
	t.Cleanup(func() { notify_service.UnregisterNotifier(notifier) })
	for _, mode := range []string{"user-add", "user-remove", "user-noop-add", "user-noop-remove", "team-add", "team-remove", "team-noop-add", "team-noop-remove", "user-duplicate-remove", "team-duplicate-remove", "self-add", "self-remove"} {
		for _, point := range []string{CrashPointHumanReviewRequestBeforePrimary, CrashPointHumanReviewRequestAfterPrimary} {
			t.Run(mode+"-"+point, func(t *testing.T) {
				root := t.TempDir()
				for _, name := range []string{"appdata", "barrier", "worker-tmp", "repositories", "work"} {
					require.NoError(t, os.Mkdir(filepath.Join(root, name), 0o700))
				}
				ctx := t.Context()
				previous := db.DefaultContext.(db.Engined).Engine()
				previousAppData, previousRepoRoot := setting.AppDataPath, setting.RepoRootPath
				setting.AppDataPath, setting.RepoRootPath = filepath.Join(root, "appdata"), filepath.Join(root, "repositories")
				engine := openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
				t.Cleanup(func() {
					require.NoError(t, queue.GetManager().FlushAll(context.Background(), 5*time.Second))
					if engine != nil {
						require.NoError(t, engine.Close())
					}
					db.SetDefaultEngine(context.Background(), previous)
					setting.AppDataPath, setting.RepoRootPath = previousAppData, previousRepoRoot
					require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
				})
				require.NoError(t, db.SyncAllTables())
				require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
				require.NoError(t, unittest.LoadFixtures())
				org := &user_model.User{Name: "process-review-org", LowerName: "process-review-org", Type: user_model.UserTypeOrganization, IsActive: true}
				_, err := db.GetEngine(ctx).Insert(org)
				require.NoError(t, err)
				repo := &repo_model.Repository{OwnerID: org.ID, OwnerName: org.Name, Name: "process-reviewer", LowerName: "process-reviewer", IsPrivate: true}
				_, err = db.GetEngine(ctx).Insert(repo)
				require.NoError(t, err)
				for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
					_, err = db.GetEngine(ctx).Insert(&repo_model.RepoUnit{RepoID: repo.ID, Type: kind})
					require.NoError(t, err)
				}
				for _, id := range []int64{2, 4} {
					_, err = db.GetEngine(ctx).Insert(&access_model.Access{RepoID: repo.ID, UserID: id, Mode: perm.AccessModeWrite}, &repo_model.Collaboration{RepoID: repo.ID, UserID: id, Mode: perm.AccessModeWrite})
					require.NoError(t, err)
				}
				team := &organization.Team{OrgID: org.ID, Name: "process-reviewers", LowerName: "process-reviewers", AccessMode: perm.AccessModeWrite}
				_, err = db.GetEngine(ctx).Insert(team)
				require.NoError(t, err)
				_, err = db.GetEngine(ctx).Insert(&organization.TeamRepo{OrgID: org.ID, TeamID: team.ID, RepoID: repo.ID}, &organization.TeamUser{OrgID: org.ID, TeamID: team.ID, UID: 4})
				require.NoError(t, err)
				_, err = db.GetEngine(ctx).Insert(&git_model.ProtectedBranch{RepoID: repo.ID, RuleName: "main", EnableApprovalsWhitelist: true, ApprovalsWhitelistUserIDs: []int64{2, 4}, ApprovalsWhitelistTeamIDs: []int64{team.ID}})
				require.NoError(t, err)
				issue := &issues_model.Issue{RepoID: repo.ID, Index: 2, PosterID: 1, IsPull: true, Title: "persistent reviewer request"}
				_, err = db.GetEngine(ctx).Insert(issue)
				require.NoError(t, err)
				pr := &issues_model.PullRequest{IssueID: issue.ID, Index: 2, BaseRepoID: repo.ID, HeadRepoID: 6, BaseBranch: "main", HeadBranch: "private-source-not-needed"}
				_, err = db.GetEngine(ctx).Insert(pr)
				require.NoError(t, err)
				require.NotEqual(t, pr.ID, issue.ID)
				gitRun := func(dir string, args ...string) string {
					t.Helper()
					command := exec.CommandContext(ctx, "git", args...)
					command.Dir = dir
					command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Native reviewer fixture", "GIT_AUTHOR_EMAIL=reviewer@example.invalid", "GIT_COMMITTER_NAME=Native reviewer fixture", "GIT_COMMITTER_EMAIL=reviewer@example.invalid", "GIT_CONFIG_NOSYSTEM=1")
					out, err := command.CombinedOutput()
					require.NoError(t, err, string(out))
					return strings.TrimSpace(string(out))
				}
				work := filepath.Join(root, "work")
				gitRun(work, "init", "--initial-branch=main")
				commit := func(body, message string) string {
					t.Helper()
					require.NoError(t, os.WriteFile(filepath.Join(work, "README"), []byte(body), 0o600))
					gitRun(work, "add", "README")
					gitRun(work, "commit", "-m", message)
					return gitRun(work, "rev-parse", "HEAD")
				}
				base := commit("base line\ncontext two\n", "base")
				oldHead := commit("old draft line\ncontext two\n", "old draft")
				head := commit("current line\ncontext two\n", "current candidate")
				require.NoError(t, os.MkdirAll(filepath.Dir(repo.RepoPath()), 0o700))
				gitRun(work, "clone", "--bare", work, repo.RepoPath())
				gitRun(repo.RepoPath(), "update-ref", "refs/heads/main", base)
				gitRun(repo.RepoPath(), "update-ref", pr.GetGitRefName(), head)
				kind, id := "user", int64(4)
				if strings.HasPrefix(mode, "team") {
					kind, id = "team", team.ID
				}
				if strings.HasPrefix(mode, "self") {
					id = 2
				}
				completedID := id
				if kind == "team" {
					completedID = -id
				}
				completed := &issues_model.Review{IssueID: issue.ID, ReviewerID: completedID, Type: issues_model.ReviewTypeApprove, Official: true, Content: "Retained completed review 完整\r\n", CommitID: oldHead, CreatedUnix: 1, UpdatedUnix: 2}
				own := &issues_model.Review{IssueID: issue.ID, ReviewerID: 2, Type: issues_model.ReviewTypePending, Content: "Own older private draft 完整\r\n", CommitID: oldHead, Stale: true}
				foreign := &issues_model.Review{IssueID: issue.ID, ReviewerID: 4, Type: issues_model.ReviewTypePending, Content: "Foreign private pending review", CommitID: head}
				_, err = db.GetEngine(ctx).Insert(completed, own, foreign)
				require.NoError(t, err)
				for _, review := range []*issues_model.Review{own, foreign} {
					_, err = db.GetEngine(ctx).Insert(&issues_model.Comment{IssueID: issue.ID, ReviewID: review.ID, PosterID: review.ReviewerID, Type: issues_model.CommentTypeCode, Content: "Retained unpublished inline 🌋\r\n", TreePath: "README", Line: 2, CommitSHA: oldHead, OriginalAnchor: &issues_model.CodeCommentAnchor{Base: base, Head: oldHead, Path: "README", Side: "new", Scope: "lines", StartLine: 1, EndLine: 2}, CreatedUnix: 1, UpdatedUnix: 2})
					require.NoError(t, err)
				}
				desired := !strings.HasSuffix(mode, "remove")
				initial := !desired
				if strings.Contains(mode, "noop") {
					initial = desired
				}
				var initialRequests []*issues_model.Review
				if initial {
					n := 1
					if strings.Contains(mode, "duplicate") {
						n = 2
					}
					for range n {
						r := &issues_model.Review{IssueID: issue.ID, Type: issues_model.ReviewTypeRequest, Content: "Exact retained request metadata 完整\r\n", Official: true, CreatedUnix: 3, UpdatedUnix: 4}
						if kind == "user" {
							r.ReviewerID = id
						} else {
							r.ReviewerTeamID = id
						}
						_, err = db.GetEngine(ctx).Insert(r)
						require.NoError(t, err)
						initialRequests = append(initialRequests, r)
					}
				}
				// A real read notification detects any attempted replay of user/team
				// delivery, independent of timestamp resolution or missing observers.
				_, err = db.GetEngine(ctx).Insert(&activities_model.Notification{IssueID: issue.ID, RepoID: repo.ID, UserID: 4, Status: activities_model.NotificationStatusRead, Source: activities_model.NotificationSourcePullRequest, CreatedUnix: 1, UpdatedUnix: 2})
				require.NoError(t, err)
				actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
				selected, err := ReadHumanReviewRequestSelection(ctx, actor, issue, kind, id)
				require.NoError(t, err)
				revision, err := NewService().ReadNativeRevision(ctx)
				require.NoError(t, err)
				require.True(t, revision.Idle)
				action := sdk.RepositoryActionCapability{State: "allowed", Reason: "native process fixture"}
				snapshot := sdk.RepositoryReviewSnapshot{RepositoryID: strconv.FormatInt(repo.ID, 10), ActorID: "2", PullID: strconv.FormatInt(pr.ID, 10), IssueID: strconv.FormatInt(issue.ID, 10), Index: "2", BaseBranch: "main", HeadOID: head, BaseOID: base, MergeBaseOID: base, PendingReviewID: strconv.FormatInt(own.ID, 10), PendingCommitID: oldHead, NativeRevision: revision.Revision, CommentAction: action, DraftAction: action, ApproveAction: action, RequestChangesAction: action}
				q := sdk.HumanReviewRequestChange{OperationID: "human-review-request:process-" + mode, Expected: sdk.RepositoryReviewRequestSnapshot{Review: snapshot, ReviewerKind: kind, ReviewerID: strconv.FormatInt(id, 10), Requests: selected.Requests, AddAction: selected.AddAction, RemoveAction: selected.RemoveAction}, Requested: desired, NotAfter: time.Now().Add(5 * time.Minute).Unix()}
				intent, err := humanReviewRequestIntent(q)
				require.NoError(t, err)
				raw, err := json.Marshal(q)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "review-request.json"), raw, 0o600))
				encode := func(v any) string { raw, err := json.Marshal(v); require.NoError(t, err); return string(raw) }
				readRows := func() string {
					var reviews []*issues_model.Review
					var comments []*issues_model.Comment
					require.NoError(t, db.GetEngine(ctx).Where("issue_id=?", issue.ID).Asc("id").Find(&reviews))
					require.NoError(t, db.GetEngine(ctx).Where("issue_id=?", issue.ID).Asc("id").Find(&comments))
					return encode(struct {
						Reviews  []*issues_model.Review
						Comments []*issues_model.Comment
					}{reviews, comments})
				}
				notifications := func() string {
					require.NoError(t, queue.GetManager().FlushAll(ctx, 5*time.Second))
					var rows []*activities_model.Notification
					require.NoError(t, db.GetEngine(ctx).Where("issue_id=?", issue.ID).Asc("id").Find(&rows))
					return encode(rows)
				}
				before, notices := readRows(), notifications()
				require.NoError(t, engine.Close())
				engine = nil
				log, err := os.Create(filepath.Join(root, "worker.log"))
				require.NoError(t, err)
				t.Cleanup(func() { _ = log.Close() })
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIssueGraphProcessRecoveryWorker$", "-test.v", "-test.timeout=90s")
				child.Env = append(os.Environ(), processRecoveryDirectoryEnv+"="+root, "TMPDIR="+filepath.Join(root, "worker-tmp"), "NATIVEOP_TEST_CRASH_POINT="+point, "NATIVEOP_TEST_CRASH_BARRIER_DIR="+filepath.Join(root, "barrier"), "NATIVEOP_TEST_CRASH_TIMEOUT_SECONDS=60")
				child.Stdout, child.Stderr = log, log
				require.NoError(t, child.Start())
				exited := make(chan error, 1)
				go func() { exited <- child.Wait() }()
				reaped := false
				t.Cleanup(func() {
					if !reaped {
						_ = child.Process.Kill()
						<-exited
					}
				})
				deadline := time.After(30 * time.Second)
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				reached := false
				for !reached {
					select {
					case err := <-exited:
						reaped = true
						output, _ := os.ReadFile(filepath.Join(root, "worker.log"))
						t.Fatalf("owned reviewer worker exited before %s: %v\n%s", point, err, output)
					case <-deadline:
						output, _ := os.ReadFile(filepath.Join(root, "worker.log"))
						t.Fatalf("owned reviewer worker never reached %s\n%s", point, output)
					case <-tick.C:
						_, err := os.Stat(filepath.Join(root, "barrier", point+".entered"))
						reached = err == nil
					}
				}
				entered, err := os.ReadFile(filepath.Join(root, "barrier", point+".entered"))
				require.NoError(t, err)
				require.Equal(t, strconv.Itoa(child.Process.Pid), strings.TrimSpace(string(entered)))
				require.NoError(t, child.Process.Signal(syscall.Signal(0)))
				require.NoError(t, child.Process.Kill())
				var killed *exec.ExitError
				select {
				case err := <-exited:
					reaped = true
					require.True(t, errors.As(err, &killed))
				case <-time.After(10 * time.Second):
					t.Fatal("killed owned reviewer worker did not exit")
				}
				status, ok := killed.ProcessState.Sys().(syscall.WaitStatus)
				require.True(t, ok)
				require.True(t, status.Signaled())
				require.Equal(t, syscall.SIGKILL, status.Signal())
				t.Logf("owned child pid=%d reached %s and was reaped after SIGKILL; reopening same native file", child.Process.Pid, point)
				engine = openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
				held, err := nativeoperation_model.ReadReservation(ctx)
				require.NoError(t, err)
				require.Equal(t, conditionalOwner(operationTestInstallation, q.OperationID), held.Owner)
				require.Positive(t, held.Generation)
				op, err := nativeoperation_model.LookupOperation(ctx, operationTestInstallation, q.OperationID)
				require.NoError(t, err)
				require.NotNil(t, op)
				require.Equal(t, intent.Canonical, op.Intent)
				require.Equal(t, intent.Digest, op.IntentDigest)
				require.Zero(t, op.TokenID)
				require.Empty(t, op.CredentialFingerprint)
				committed := point == CrashPointHumanReviewRequestAfterPrimary
				require.Equal(t, committed, op.Admitted)
				var receipt sdk.HumanReviewRequestReceipt
				if committed {
					require.NoError(t, json.Unmarshal([]byte(op.Receipt), &receipt))
					require.True(t, sdk.ValidHumanReviewRequestReceipt(receipt, q))
					require.Equal(t, initial != desired, receipt.Changed)
					after, err := ReadHumanReviewRequestEvidence(ctx, issue.ID, kind, id)
					require.NoError(t, err)
					require.Equal(t, receipt.After, after)
					require.Equal(t, nativeoperation_model.CompletionPending, op.Completion)
					if !receipt.Changed {
						require.Equal(t, before, readRows())
					}
					if strings.Contains(mode, "duplicate") {
						require.Len(t, receipt.Before.RequestIDs, 2)
						require.Len(t, receipt.HistoryIDs, 2)
					}
					// Every old review/comment retains all persisted metadata except
					// native request deletion and historical official-flag restoration.
					var old struct {
						Reviews  []*issues_model.Review
						Comments []*issues_model.Comment
					}
					require.NoError(t, json.Unmarshal([]byte(before), &old))
					for _, saved := range old.Reviews {
						removed := false
						for _, request := range initialRequests {
							removed = removed || (receipt.Changed && !desired && request.ID == saved.ID)
						}
						if removed {
							exists, err := db.GetEngine(ctx).ID(saved.ID).Exist(new(issues_model.Review))
							require.NoError(t, err)
							require.False(t, exists)
							continue
						}
						current, err := issues_model.GetReviewByID(ctx, saved.ID)
						require.NoError(t, err)
						if receipt.Changed && kind == "user" && saved.ID == completed.ID {
							saved.Official = !desired
						}
						require.Equal(t, encode(saved), encode(current), "persistent reviewer primary changed retained metadata")
					}
					for _, saved := range old.Comments {
						current, err := issues_model.GetCommentByID(ctx, saved.ID)
						require.NoError(t, err)
						require.Equal(t, encode(saved), encode(current), "persistent reviewer primary changed unpublished inline/history")
					}
				} else {
					require.Equal(t, before, readRows())
					require.Empty(t, op.Receipt)
					require.Equal(t, nativeoperation_model.EffectPending, op.EffectState)
				}
				require.Equal(t, notices, notifications(), "owned worker performed notification work before the completion barrier")
				inhibitDomain(t)
				svc := NewService()
				assertFenced := func() {
					t.Helper()
					before, notices := readRows(), notifications()
					a, err := svc.Recover(ctx, held.Owner, held.Generation)
					require.NoError(t, err)
					require.Equal(t, RecoveryFenced, a.Verdict, "%+v", a)
					still, err := nativeoperation_model.ReadReservation(ctx)
					require.NoError(t, err)
					require.Equal(t, held.Owner, still.Owner)
					require.Equal(t, before, readRows())
					require.Equal(t, notices, notifications())
				}
				if committed {
					for _, field := range []string{"receipt-encoding", "intent", "scope", "historical-review", "selected-draft", "change-history", "active-request"} {
						var restore func()
						switch field {
						case "receipt-encoding":
							_, err = db.GetEngine(ctx).ID(op.ID).Cols("receipt").Update(&nativeoperation_model.Operation{Receipt: op.Receipt + " "})
							restore = func() { _, err = db.GetEngine(ctx).ID(op.ID).Cols("receipt").Update(op) }
						case "intent":
							_, err = db.GetEngine(ctx).ID(op.ID).Cols("intent").Update(&nativeoperation_model.Operation{Intent: op.Intent + " "})
							restore = func() { _, err = db.GetEngine(ctx).ID(op.ID).Cols("intent").Update(op) }
						case "scope":
							var scope Scope
							require.NoError(t, json.Unmarshal([]byte(held.Scope), &scope))
							scope.IssueID++
							_, err = db.GetEngine(ctx).ID(1).Cols("scope").Update(&nativeoperation_model.Reservation{Scope: encode(scope)})
							restore = func() { _, err = db.GetEngine(ctx).ID(1).Cols("scope").Update(held) }
						case "historical-review", "selected-draft":
							target := completed.ID
							if field == "selected-draft" {
								if kind == "team" {
									continue
								}
								target = foreign.ID
								if id == 2 {
									target = own.ID
								}
							}
							saved, e := issues_model.GetReviewByID(ctx, target)
							require.NoError(t, e)
							_, err = db.GetEngine(ctx).NoAutoTime().ID(saved.ID).Cols("content").Update(&issues_model.Review{Content: "Changed retained reviewer writing"})
							restore = func() { _, err = db.GetEngine(ctx).NoAutoTime().ID(saved.ID).Cols("content").Update(saved) }
						case "change-history":
							if len(receipt.HistoryIDs) == 0 {
								continue
							}
							saved, e := issues_model.GetCommentByID(ctx, nativeCommentID(receipt.HistoryIDs[0]))
							require.NoError(t, e)
							_, err = db.GetEngine(ctx).Exec("UPDATE comment SET updated_unix=? WHERE id=?", saved.UpdatedUnix+100, saved.ID)
							restore = func() {
								_, err = db.GetEngine(ctx).Exec("UPDATE comment SET updated_unix=? WHERE id=?", saved.UpdatedUnix, saved.ID)
							}
						case "active-request":
							if len(receipt.After.RequestIDs) == 0 {
								continue
							}
							saved, e := issues_model.GetReviewByID(ctx, nativeCommentID(receipt.After.RequestIDs[0]))
							require.NoError(t, e)
							_, err = db.GetEngine(ctx).NoAutoTime().ID(saved.ID).Cols("official").Update(&issues_model.Review{Official: !saved.Official})
							restore = func() { _, err = db.GetEngine(ctx).NoAutoTime().ID(saved.ID).Cols("official").Update(saved) }
						}
						require.NoError(t, err)
						assertFenced()
						restore()
						require.NoError(t, err)
					}
				} else {
					_, err = db.GetEngine(ctx).ID(op.ID).Cols("receipt").Update(&nativeoperation_model.Operation{Receipt: "{}"})
					require.NoError(t, err)
					assertFenced()
					_, err = db.GetEngine(ctx).ID(op.ID).Cols("receipt").Update(op)
					require.NoError(t, err)
				}
				// A second persistent reopen rules out in-memory receipt/row caches.
				require.NoError(t, engine.Close())
				engine = openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
				svc = NewService()
				retained, notices := readRows(), notifications()
				a, err := svc.Recover(ctx, held.Owner, held.Generation)
				require.NoError(t, err)
				require.Equal(t, RecoveryReleased, a.Verdict, "%+v", a)
				require.Equal(t, retained, readRows())
				require.Equal(t, notices, notifications())
				record, err := nativeoperation_model.LookupOperation(ctx, operationTestInstallation, q.OperationID)
				require.NoError(t, err)
				require.Equal(t, op.Receipt, record.Receipt)
				if committed {
					completion := nativeoperation_model.CompletionComplete
					if initial != desired && (kind == "user" && id != 2 || kind == "team" && desired) {
						completion = nativeoperation_model.CompletionNeedsIntervention
					}
					require.Equal(t, nativeoperation_model.EffectCommitted, record.EffectState)
					require.Equal(t, completion, record.Completion)
				} else {
					require.Equal(t, nativeoperation_model.EffectNotCommitted, record.EffectState)
					require.Equal(t, nativeoperation_model.ReasonRecoveredNoEffect, record.Reason)
				}
				idle, err := nativeoperation_model.ReadReservation(ctx)
				require.NoError(t, err)
				require.Empty(t, idle.Owner)
				require.NoError(t, os.Remove(nativeoperation_model.OfflineMarkerPath()))
				replay, err := svc.SubmitHumanReviewRequest(ctx, operationTestInstallation, q, processHumanCommentAdmission)
				require.NoError(t, err)
				require.Equal(t, record.Receipt, string(replay.Receipt))
				require.Equal(t, record.EffectState, replay.EffectState)
				require.Equal(t, record.Completion, replay.CompletionState)
				lookup, err := svc.Get(ctx, operationTestInstallation, q.OperationID)
				require.NoError(t, err)
				require.NotNil(t, lookup.Record)
				require.Equal(t, replay.Receipt, lookup.Record.Receipt)
				require.Equal(t, retained, readRows(), "recovery/replay repeated reviewer SQL/history")
				require.Equal(t, notices, notifications(), "recovery/replay repeated native UI notifications")
				t.Logf("PASS: %s %s; owned SIGKILL/reap, two persistent reopens, exact request/review/history proof and unchanged notifications on recovery/replay", mode, point)
			})
		}
	}
}

func checkHumanThreadProcessRecovery(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"resolve", "unresolve", "noop-resolve", "noop-unresolve", "unknown", "own-draft"} {
		for _, point := range []string{CrashPointHumanThreadBeforePrimary, CrashPointHumanThreadAfterPrimary} {
			t.Run(mode+"-"+point, func(t *testing.T) {
				root := t.TempDir()
				for _, name := range []string{"appdata", "barrier", "worker-tmp", "repositories", "work"} {
					require.NoError(t, os.Mkdir(filepath.Join(root, name), 0o700))
				}
				ctx := t.Context()
				previous := db.DefaultContext.(db.Engined).Engine()
				previousAppData, previousRepoRoot := setting.AppDataPath, setting.RepoRootPath
				setting.AppDataPath, setting.RepoRootPath = filepath.Join(root, "appdata"), filepath.Join(root, "repositories")
				engine := openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
				t.Cleanup(func() {
					if engine != nil {
						require.NoError(t, engine.Close())
					}
					db.SetDefaultEngine(context.Background(), previous)
					setting.AppDataPath, setting.RepoRootPath = previousAppData, previousRepoRoot
					require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
				})
				require.NoError(t, db.SyncAllTables())
				require.NoError(t, unittest.InitFixtures(unittest.FixturesOptions{Dir: unittest.FixturesDir()}))
				require.NoError(t, unittest.LoadFixtures())
				owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
				repo := &repo_model.Repository{OwnerID: 1, OwnerName: owner.Name, Name: "process-human-thread", LowerName: "process-human-thread", IsPrivate: true}
				_, err := db.GetEngine(ctx).Insert(repo)
				require.NoError(t, err)
				for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
					_, err = db.GetEngine(ctx).Insert(&repo_model.RepoUnit{RepoID: repo.ID, Type: kind})
					require.NoError(t, err)
				}
				for _, actor := range []int64{2} {
					_, err = db.GetEngine(ctx).Insert(&access_model.Access{RepoID: repo.ID, UserID: actor, Mode: perm.AccessModeWrite}, &repo_model.Collaboration{RepoID: repo.ID, UserID: actor, Mode: perm.AccessModeWrite})
					require.NoError(t, err)
				}
				issue := &issues_model.Issue{RepoID: repo.ID, Index: 2, PosterID: 1, IsPull: true, Title: "persistent review target"}
				_, err = db.GetEngine(ctx).Insert(issue)
				require.NoError(t, err)
				pr := &issues_model.PullRequest{IssueID: issue.ID, Index: 2, BaseRepoID: repo.ID, HeadRepoID: 6, BaseBranch: "main", HeadBranch: "private-source-not-needed"}
				_, err = db.GetEngine(ctx).Insert(pr)
				require.NoError(t, err)
				require.NotEqual(t, pr.ID, issue.ID)
				gitRun := func(dir string, args ...string) string {
					t.Helper()
					command := exec.CommandContext(ctx, "git", args...)
					command.Dir = dir
					command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Native review fixture", "GIT_AUTHOR_EMAIL=review-fixture@example.invalid", "GIT_COMMITTER_NAME=Native review fixture", "GIT_COMMITTER_EMAIL=review-fixture@example.invalid", "GIT_CONFIG_NOSYSTEM=1")
					out, err := command.CombinedOutput()
					require.NoError(t, err, string(out))
					return strings.TrimSpace(string(out))
				}
				work := filepath.Join(root, "work")
				gitRun(work, "init", "--initial-branch=main")
				commit := func(body, message string) string {
					t.Helper()
					require.NoError(t, os.WriteFile(filepath.Join(work, "README"), []byte(body), 0o600))
					gitRun(work, "add", "README")
					gitRun(work, "commit", "-m", message)
					return gitRun(work, "rev-parse", "HEAD")
				}
				base := commit("base line\nbase two\ncontext three\ncontext four\n", "base")
				oldHead := commit("old reviewed line\nold two\ncontext three\ncontext four\n", "old draft candidate")
				head := commit("current reviewed line\ncurrent two\ncontext three\ncontext four\n", "current candidate")
				require.NoError(t, os.MkdirAll(filepath.Dir(repo.RepoPath()), 0o700))
				gitRun(work, "clone", "--bare", work, repo.RepoPath())
				gitRun(repo.RepoPath(), "update-ref", "refs/heads/main", base)
				gitRun(repo.RepoPath(), "update-ref", pr.GetGitRefName(), head)

				review := &issues_model.Review{IssueID: issue.ID, ReviewerID: 2, Type: issues_model.ReviewTypeComment, Content: "Retained published review 完整\r\n", CommitID: oldHead}
				ownReview := &issues_model.Review{IssueID: issue.ID, ReviewerID: 2, Type: issues_model.ReviewTypePending, Content: "Own older unpublished review", CommitID: oldHead, Stale: true}
				foreignReview := &issues_model.Review{IssueID: issue.ID, ReviewerID: 4, Type: issues_model.ReviewTypePending, Content: "Foreign private review", CommitID: head}
				_, err = db.GetEngine(ctx).Insert(review, ownReview, foreignReview)
				require.NoError(t, err)
				anchor := &issues_model.CodeCommentAnchor{Base: base, Head: oldHead, Path: "README", Side: "new", Scope: "lines", StartLine: 2, EndLine: 3}
				switch mode {
				case "resolve":
					anchor.Side, anchor.Scope, anchor.StartLine, anchor.EndLine = "old", "file", 0, 0
				case "noop-resolve":
					anchor.Scope, anchor.StartLine, anchor.EndLine = "file", 0, 0
				case "unknown":
					anchor = nil
				}
				selectedReview := review
				if mode == "own-draft" {
					selectedReview = ownReview
				}
				add := func(r *issues_model.Review, a *issues_model.CodeCommentAnchor, body string) *issues_model.Comment {
					t.Helper()
					line := int64(3)
					if a != nil {
						line = a.EndLine
						if a.Side == "old" {
							line = -line
						}
					}
					row := &issues_model.Comment{IssueID: issue.ID, ReviewID: r.ID, PosterID: r.ReviewerID, Type: issues_model.CommentTypeCode, Content: body, ContentVersion: 3, TreePath: "README", Line: line, CommitSHA: oldHead, OriginalAnchor: a, Patch: "Original retained hunk", OriginalAuthorID: 42, OriginalAuthor: "Imported author", CreatedUnix: 1, UpdatedUnix: 1}
					_, err := db.GetEngine(ctx).Insert(row)
					require.NoError(t, err)
					return row
				}
				target := add(selectedReview, anchor, "Original root 完整 🌋\r\n")
				reply := add(selectedReview, anchor.Copy(), "Original retained reply")
				add(foreignReview, anchor.Copy(), "Foreign unpublished inline")
				if mode != "own-draft" {
					add(ownReview, anchor.Copy(), "Own other unpublished inline")
				}
				resolver := int64(0)
				if mode == "unresolve" {
					resolver = 2
				}
				if mode == "noop-resolve" {
					resolver = 4
				}
				_, err = db.GetEngine(ctx).NoAutoTime().ID(target.ID).Cols("resolve_doer_id").Update(&issues_model.Comment{ResolveDoerID: resolver})
				require.NoError(t, err)
				actor := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
				evidence, err := ReadHumanThreadEvidence(ctx, issue, actor, target.ID)
				require.NoError(t, err)
				require.Equal(t, 2, evidence.MemberCount)
				require.Equal(t, threadOriginalAnchor(anchor), evidence.OriginalAnchor)
				revision, err := NewService().ReadNativeRevision(ctx)
				require.NoError(t, err)
				require.True(t, revision.Idle)
				action := sdk.RepositoryActionCapability{State: "allowed", Reason: "native process fixture"}
				snapshot := sdk.RepositoryReviewSnapshot{RepositoryID: strconv.FormatInt(repo.ID, 10), ActorID: "2", PullID: strconv.FormatInt(pr.ID, 10), IssueID: strconv.FormatInt(issue.ID, 10), Index: "2", BaseBranch: "main", HeadOID: head, BaseOID: base, MergeBaseOID: base, NativeRevision: revision.Revision, CommentAction: action, DraftAction: action, ApproveAction: action, RequestChangesAction: action}
				q := sdk.HumanThreadRequest{OperationID: "human-thread:process-" + mode, Expected: sdk.RepositoryThreadSnapshot{Review: snapshot, Thread: evidence, ResolveAction: action, UnresolveAction: action}, Resolved: mode != "unresolve" && mode != "noop-unresolve", NotAfter: time.Now().Add(5 * time.Minute).Unix()}
				intent, err := humanThreadIntent(q)
				require.NoError(t, err)
				raw, err := json.Marshal(q)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "thread.json"), raw, 0o600))
				readRows := func(override *int64) string {
					t.Helper()
					var comments []*issues_model.Comment
					var reviews []*issues_model.Review
					require.NoError(t, db.GetEngine(ctx).Where("issue_id=?", issue.ID).Asc("id").Find(&comments))
					require.NoError(t, db.GetEngine(ctx).Where("issue_id=?", issue.ID).Asc("id").Find(&reviews))
					if override != nil {
						for _, c := range comments {
							if c.ID == target.ID {
								c.ResolveDoerID = *override
							}
						}
					}
					encoded, err := json.Marshal(struct {
						Comments []*issues_model.Comment
						Reviews  []*issues_model.Review
					}{comments, reviews})
					require.NoError(t, err)
					return string(encoded)
				}
				expectedResolver := resolver
				if q.Resolved && resolver == 0 {
					expectedResolver = 2
				}
				if !q.Resolved {
					expectedResolver = 0
				}
				before, expectedAfter := readRows(nil), readRows(&expectedResolver)
				require.NoError(t, engine.Close())
				engine = nil
				log, err := os.Create(filepath.Join(root, "worker.log"))
				require.NoError(t, err)
				t.Cleanup(func() { _ = log.Close() })
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIssueGraphProcessRecoveryWorker$", "-test.v", "-test.timeout=90s")
				child.Env = append(os.Environ(), processRecoveryDirectoryEnv+"="+root, "TMPDIR="+filepath.Join(root, "worker-tmp"), "NATIVEOP_TEST_CRASH_POINT="+point, "NATIVEOP_TEST_CRASH_BARRIER_DIR="+filepath.Join(root, "barrier"), "NATIVEOP_TEST_CRASH_TIMEOUT_SECONDS=60")
				child.Stdout, child.Stderr = log, log
				require.NoError(t, child.Start())
				exited := make(chan error, 1)
				go func() { exited <- child.Wait() }()
				reaped := false
				t.Cleanup(func() {
					if !reaped {
						_ = child.Process.Kill()
						<-exited
					}
				})
				deadline := time.After(30 * time.Second)
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				reached := false
				for !reached {
					select {
					case err := <-exited:
						reaped = true
						output, _ := os.ReadFile(filepath.Join(root, "worker.log"))
						t.Fatalf("owned thread worker exited before %s: %v\n%s", point, err, output)
					case <-deadline:
						output, _ := os.ReadFile(filepath.Join(root, "worker.log"))
						t.Fatalf("owned thread worker never reached %s\n%s", point, output)
					case <-tick.C:
						_, err := os.Stat(filepath.Join(root, "barrier", point+".entered"))
						reached = err == nil
					}
				}
				entered, err := os.ReadFile(filepath.Join(root, "barrier", point+".entered"))
				require.NoError(t, err)
				require.Equal(t, strconv.Itoa(child.Process.Pid), strings.TrimSpace(string(entered)))
				require.NoError(t, child.Process.Kill())
				var killed *exec.ExitError
				select {
				case err := <-exited:
					reaped = true
					require.True(t, errors.As(err, &killed))
				case <-time.After(10 * time.Second):
					t.Fatal("killed owned thread worker did not exit")
				}
				status, ok := killed.ProcessState.Sys().(syscall.WaitStatus)
				require.True(t, ok)
				require.True(t, status.Signaled())
				require.Equal(t, syscall.SIGKILL, status.Signal())
				t.Logf("owned child pid=%d reached %s and was reaped after SIGKILL; reopening same native file", child.Process.Pid, point)
				engine = openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))

				held, err := nativeoperation_model.ReadReservation(ctx)
				require.NoError(t, err)
				require.Equal(t, conditionalOwner(operationTestInstallation, q.OperationID), held.Owner)
				require.Positive(t, held.Generation)
				op, err := nativeoperation_model.LookupOperation(ctx, operationTestInstallation, q.OperationID)
				require.NoError(t, err)
				require.NotNil(t, op)
				require.Equal(t, intent.Canonical, op.Intent)
				require.Equal(t, intent.Digest, op.IntentDigest)
				require.Zero(t, op.TokenID)
				require.Empty(t, op.CredentialFingerprint)
				committed := point == CrashPointHumanThreadAfterPrimary
				require.Equal(t, committed, op.Admitted)
				if committed {
					require.Equal(t, expectedAfter, readRows(nil), "only root resolver may change")
					var receipt sdk.HumanThreadReceipt
					require.NoError(t, json.Unmarshal([]byte(op.Receipt), &receipt))
					require.True(t, sdk.ValidHumanThreadReceipt(receipt, q))
					require.Equal(t, resolver != expectedResolver, receipt.Changed)
					after, err := ReadHumanThreadEvidence(ctx, issue, actor, target.ID)
					require.NoError(t, err)
					require.Equal(t, receipt.After, after)
					require.Equal(t, nativeoperation_model.CompletionPending, op.Completion)
				} else {
					require.Equal(t, before, readRows(nil))
					require.Empty(t, op.Receipt)
					require.Equal(t, nativeoperation_model.EffectPending, op.EffectState)
				}
				inhibitDomain(t)
				svc := NewService()
				assertFenced := func() {
					t.Helper()
					before := readRows(nil)
					a, err := svc.Recover(ctx, held.Owner, held.Generation)
					require.NoError(t, err)
					require.Equal(t, RecoveryFenced, a.Verdict, "%+v", a)
					still, err := nativeoperation_model.ReadReservation(ctx)
					require.NoError(t, err)
					require.Equal(t, held.Owner, still.Owner)
					require.Equal(t, before, readRows(nil))
				}
				if committed {
					_, err = db.GetEngine(ctx).ID(op.ID).Cols("receipt").Update(&nativeoperation_model.Operation{Receipt: op.Receipt + " "})
					require.NoError(t, err)
					assertFenced()
					_, err = db.GetEngine(ctx).ID(op.ID).Cols("receipt").Update(op)
					require.NoError(t, err)
					for _, column := range []string{"content", "original_anchor", "patch", "original_author_id", "resolve_doer_id"} {
						id := reply.ID
						if column == "resolve_doer_id" {
							id = target.ID
						}
						saved, err := issues_model.GetCommentByID(ctx, id)
						require.NoError(t, err)
						changed := *saved
						changed.Content = "Changed retained writing"
						changed.Patch = "Changed retained patch"
						changed.OriginalAuthorID = 777
						changed.ResolveDoerID = 3
						changed.OriginalAnchor = &issues_model.CodeCommentAnchor{Base: base, Head: head, Path: "README", Side: "old", Scope: "lines", StartLine: 1, EndLine: 1}
						_, err = db.GetEngine(ctx).NoAutoTime().ID(id).Cols(column).Update(&changed)
						require.NoError(t, err)
						assertFenced()
						_, err = db.GetEngine(ctx).NoAutoTime().ID(id).Cols(column).Update(saved)
						require.NoError(t, err)
					}
				}
				// Reopen again before reconciliation: only retained SQL is evidence.
				require.NoError(t, engine.Close())
				engine = openProcessRecoveryDatabase(t, filepath.Join(root, "native.sqlite"))
				svc = NewService()
				retained := readRows(nil)
				a, err := svc.Recover(ctx, held.Owner, held.Generation)
				require.NoError(t, err)
				require.Equal(t, RecoveryReleased, a.Verdict, "%+v", a)
				require.Equal(t, retained, readRows(nil))
				record, err := nativeoperation_model.LookupOperation(ctx, operationTestInstallation, q.OperationID)
				require.NoError(t, err)
				require.Equal(t, op.Receipt, record.Receipt)
				if committed {
					require.Equal(t, nativeoperation_model.EffectCommitted, record.EffectState)
					require.Equal(t, nativeoperation_model.CompletionComplete, record.Completion)
				} else {
					require.Equal(t, nativeoperation_model.EffectNotCommitted, record.EffectState)
					require.Equal(t, nativeoperation_model.ReasonRecoveredNoEffect, record.Reason)
				}
				idle, err := nativeoperation_model.ReadReservation(ctx)
				require.NoError(t, err)
				require.Empty(t, idle.Owner)
				require.NoError(t, os.Remove(nativeoperation_model.OfflineMarkerPath()))
				replay, err := svc.SubmitHumanThread(ctx, operationTestInstallation, q, processHumanCommentAdmission)
				require.NoError(t, err)
				require.Equal(t, record.Receipt, string(replay.Receipt))
				require.Equal(t, record.EffectState, replay.EffectState)
				require.Equal(t, record.Completion, replay.CompletionState)
				lookup, err := svc.Get(ctx, operationTestInstallation, q.OperationID)
				require.NoError(t, err)
				require.NotNil(t, lookup.Record)
				require.Equal(t, replay.Receipt, lookup.Record.Receipt)
				require.Equal(t, retained, readRows(nil), "recovery/replay must never repeat thread mutation")
				t.Logf("PASS: %s %s; owned SIGKILL/reap, two persistent reopens, exact receipt/evidence fencing, root-only result and no mutation replay", mode, point)
			})
		}
	}
}

// A trusted native fixture principal; the callback integration separately
// qualifies live session revocation. No personal token is created or borrowed.
func processHumanCommentAdmission(ctx context.Context) error {
	actor, err := user_model.GetUserByID(ctx, 2)
	if err != nil || !actor.IsAccessAllowed(ctx) {
		return ErrAuthorityLost
	}
	return nil
}

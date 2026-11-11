// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Verify the exact persisted native receipt.
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

func humanExecutionComment(t *testing.T, command string) (*Service, sdk.HumanCommentRequest) {
	t.Helper()
	useIsolatedAppData(t)
	svc := NewService()
	revision, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	id, err := sdk.HumanCommentOperationID("2", "1", command)
	require.NoError(t, err)
	q := sdk.HumanCommentRequest{
		OperationID: id, NotAfter: time.Now().Add(time.Minute).Unix(), Body: "live human body " + command,
		Expected: sdk.RepositoryCommentSnapshot{
			RepositoryID: "1", ActorID: "2", Subject: "issue", SubjectID: "1", IssueID: "1", Index: "1", NativeRevision: revision.Revision,
			Action: sdk.RepositoryActionCapability{State: "allowed", Reason: "issue_comment"},
		},
	}
	require.True(t, sdk.ValidHumanCommentRequest(q))
	return svc, q
}

func TestHumanFreshCompletionRechecksLiveAdmissionWithoutErasingPrimary(t *testing.T) {
	unittest.PrepareTestEnv(t)
	svc, q := humanExecutionComment(t, "final-admission")
	calls := 0
	record, err := svc.SubmitHumanComment(t.Context(), operationTestInstallation, q, func(context.Context) error {
		calls++
		if calls > 2 {
			return ErrAuthorityLost
		}
		return nil
	})
	require.ErrorIs(t, err, ErrAuthorityLost)
	require.Empty(t, record.OperationID)
	require.Equal(t, 3, calls, "initial, SQL primary and result admission have separate checks")
	op, err := nativeoperation_model.LookupOperation(t.Context(), operationTestInstallation, q.OperationID)
	require.NoError(t, err)
	require.NotNil(t, op)
	require.Equal(t, nativeoperation_model.EffectCommitted, op.EffectState)
	require.Equal(t, nativeoperation_model.CompletionComplete, op.Completion)
	var receipt sdk.HumanCommentReceipt
	require.NoError(t, json.Unmarshal([]byte(op.Receipt), &receipt))
	comment := unittest.AssertExistsAndLoadBean(t, &issues_model.Comment{ID: receipt.CommentID})
	require.Equal(t, q.Body, comment.Content)
	held, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	require.Empty(t, held.Owner)
	dir, err := svc.execDir()
	require.NoError(t, err)
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestHumanReplayRechecksLiveAdmissionBeforeReturningSavedTruth(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "cancelled before submit"}[cancelled], func(t *testing.T) {
			unittest.PrepareTestEnv(t)
			svc, q := humanExecutionComment(t, "replay-admission")
			// Existing truth is still replayable after its original deadline.
			q.NotAfter = time.Now().Add(-time.Minute).Unix()
			intent, err := humanCommentIntent(q)
			require.NoError(t, err)
			savedReceipt, err := json.Marshal(sdk.HumanCommentReceipt{RepositoryID: 1, ActorID: 2, Subject: "issue", SubjectID: 1, IssueID: 1, Index: 1, CommentID: 42})
			require.NoError(t, err)
			if cancelled {
				_, err = svc.Cancel(t.Context(), operationTestInstallation, q.OperationID)
				require.NoError(t, err)
			} else {
				op := operationForSubmission(testDecision(), operationTestInstallation, intent)
				op.Submitted, op.EffectState, op.Completion, op.Receipt = true, nativeoperation_model.EffectCommitted, nativeoperation_model.CompletionComplete, string(savedReceipt)
				require.NoError(t, db.Insert(t.Context(), op))
			}
			before, err := nativeoperation_model.LookupOperation(t.Context(), operationTestInstallation, q.OperationID)
			require.NoError(t, err)
			calls := 0
			record, err := svc.SubmitHumanComment(t.Context(), operationTestInstallation, q, func(context.Context) error {
				calls++
				if calls == 2 {
					return ErrAuthorityLost
				}
				return nil
			})
			require.ErrorIs(t, err, ErrAuthorityLost)
			require.Empty(t, record.OperationID)
			require.Equal(t, 2, calls)
			after, err := nativeoperation_model.LookupOperation(t.Context(), operationTestInstallation, q.OperationID)
			require.NoError(t, err)
			require.Equal(t, before, after)
			record, err = svc.SubmitHumanComment(t.Context(), operationTestInstallation, q, func(context.Context) error { return nil })
			if cancelled {
				require.ErrorIs(t, err, ErrCancelledBeforeSubmit)
			} else {
				require.NoError(t, err)
				require.Equal(t, string(savedReceipt), string(record.Receipt))
			}
			require.Equal(t, q.OperationID, record.OperationID)
		})
	}
}

func TestHumanCancellationOrderingAndDetachedSettlement(t *testing.T) {
	for _, afterPrimary := range []bool{false, true} {
		t.Run(map[bool]string{false: "journal cancellation before primary", true: "request cancellation after primary"}[afterPrimary], func(t *testing.T) {
			unittest.PrepareTestEnv(t)
			svc, q := humanExecutionComment(t, "ordered-cancellation")
			point := CrashPointHumanCommentBeforePrimary
			if afterPrimary {
				point = CrashPointHumanCommentAfterPrimary
			}
			dir := t.TempDir()
			t.Setenv("NATIVEOP_TEST_CRASH_POINT", point)
			t.Setenv("NATIVEOP_TEST_CRASH_BARRIER_DIR", dir)
			release := filepath.Join(dir, point+".release")
			t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			type outcome struct {
				record sdk.OperationRecord
				err    error
			}
			done := make(chan outcome, 1)
			var admissionCalls atomic.Int32
			go func() {
				r, err := svc.SubmitHumanComment(ctx, operationTestInstallation, q, func(context.Context) error { admissionCalls.Add(1); return nil })
				done <- outcome{r, err}
			}()
			require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, point+".entered")); return err == nil }, 5*time.Second, 5*time.Millisecond)
			if afterPrimary {
				cancel()
			} else {
				_, err := svc.Cancel(t.Context(), operationTestInstallation, q.OperationID)
				require.NoError(t, err)
			}
			require.NoError(t, os.WriteFile(release, nil, 0o600))
			var result outcome
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("human operation did not settle")
			}
			op, err := nativeoperation_model.LookupOperation(t.Context(), operationTestInstallation, q.OperationID)
			require.NoError(t, err)
			if afterPrimary {
				require.ErrorIs(t, result.err, ErrAuthorityLost)
				require.Empty(t, result.record.OperationID)
				require.Equal(t, nativeoperation_model.EffectCommitted, op.EffectState)
				require.NotEmpty(t, op.Receipt)
			} else {
				require.NoError(t, result.err)
				require.Equal(t, nativeoperation_model.EffectNotCommitted, op.EffectState)
				require.Equal(t, nativeoperation_model.ReasonCancelledBeforeAdmission, op.Reason)
				require.Empty(t, op.Receipt)
			}
			held, err := nativeoperation_model.ReadReservation(t.Context())
			require.NoError(t, err)
			require.Empty(t, held.Owner)
			require.GreaterOrEqual(t, admissionCalls.Load(), int32(2))
		})
	}
}

func TestTypedReviewRefusalProseCannotChangeReceiptOrFenceMapping(t *testing.T) {
	for _, item := range []struct {
		code   issues_model.ExactReviewRefusalCode
		reason string
	}{
		{issues_model.ReviewRefusalHeadChanged, nativeoperation_model.ReasonStaleHead},
		{issues_model.ReviewRefusalPendingDraftExists, nativeoperation_model.ReasonPendingReviewExists},
		{reviewRefusalMergeBaseUnavailable, nativeoperation_model.ReasonNativeRefused},
	} {
		err := issues_model.NewExactReviewRefusal(item.code, "completely revised explanatory prose")
		require.Equal(t, item.reason, mapExactReviewRefusal(errors.Join(errors.New("context"), err)))
	}
	ownReview := issues_model.NewExactReviewRefusal(issues_model.ReviewRefusalCannotReviewYourOwnPullRequest, "reworded permission explanation")
	require.Equal(t, nativeoperation_model.ReasonAuthorityLost, mapHumanReviewRefusal(ownReview))
	require.Equal(t, nativeoperation_model.ReasonNativeRefused, mapHumanInlineRefusal(ownReview), "inline preserves its distinct fallback profile")
	pending := issues_model.NewExactReviewRefusal(issues_model.ReviewRefusalPendingDraftChanged, "reworded draft explanation")
	require.Equal(t, nativeoperation_model.ReasonPendingReviewExists, mapHumanReviewRefusal(pending))
	require.Equal(t, nativeoperation_model.ReasonPRMismatch, mapExactReviewRefusal(pending), "conditional draft behavior stays distinct")
	for _, item := range []struct {
		code  issues_model.ExactReviewRefusalCode
		mapIt func(issues_model.ErrExactReviewRefused) (string, bool)
	}{
		{reviewRefusalHumanThreadPrimaryEvidenceDiffers, mapHumanThreadRefusal},
		{reviewRefusalReviewerPrimaryReadbackDiffers, mapHumanReviewRequestRefusal},
	} {
		reason, retain := item.mapIt(issues_model.NewExactReviewRefusal(item.code, "new SQL evidence explanation"))
		require.True(t, retain)
		require.Empty(t, reason)
	}
}

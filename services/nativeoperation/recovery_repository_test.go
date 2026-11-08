// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"strings"
	"testing"

	execcontext "forgejo.org/modules/nativeoperation"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

func TestSplitOrdinaryResource(t *testing.T) {
	family, resource, ok := execcontext.SplitOrdinaryResource("ord:ref-write/1/file/refs/heads/main/0123456789abcdef")
	if !ok || family != "ref-write" || resource != "1/file/refs/heads/main" {
		t.Fatalf("split = %q %q %t, want ref-write 1/file/refs/heads/main true", family, resource, ok)
	}

	if _, _, ok := execcontext.SplitOrdinaryResource("cond:installation/operation"); ok {
		t.Fatal("conditional owner must not parse as an ordinary resource")
	}
	if _, _, ok := execcontext.SplitOrdinaryResource("ord:familyonly"); ok {
		t.Fatal("owner without a resource must not parse")
	}
	if _, _, ok := execcontext.SplitOrdinaryResource(""); ok {
		t.Fatal("empty owner must not parse")
	}
}

// TestRepositoryResourceFormats pins the owner resource shapes that
// offline recovery parses back: three segments for ref writes,
// four for lifecycle operations and protection rules, with slashes
// permitted only in the trailing segment.
func TestRepositoryResourceFormats(t *testing.T) {
	refWrite := execcontext.RefWriteResource(7, RefWriteFile, "wiki/refs/heads/master")
	parts := strings.SplitN(refWrite, "/", 3)
	if len(parts) != 3 || parts[0] != "7" || parts[1] != RefWriteFile || parts[2] != "wiki/refs/heads/master" {
		t.Fatalf("ref-write resource %q splits to %#v", refWrite, parts)
	}

	lifecycle := LifecycleResource(0, LifecycleMigrate, "soda-tester", "demo")
	parts = strings.SplitN(lifecycle, "/", 4)
	if len(parts) != 4 || parts[0] != "0" || parts[1] != LifecycleMigrate || parts[2] != "soda-tester" || parts[3] != "demo" {
		t.Fatalf("lifecycle resource %q splits to %#v", lifecycle, parts)
	}

	protection := ProtectionResource(9, ProtectionBranch, ProtectionEdit, "feature/*")
	parts = strings.SplitN(protection, "/", 4)
	if len(parts) != 4 || parts[0] != "9" || parts[1] != ProtectionBranch || parts[2] != ProtectionEdit || parts[3] != "feature/*" {
		t.Fatalf("protection resource %q splits to %#v", protection, parts)
	}
}

func recoverRepositoryCase(t *testing.T, owner string, scope Scope, setup func(ctx context.Context)) RecoveryAssessment {
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
	assessment, err := svc.Recover(ctx, claimed.Owner, claimed.Generation)
	require.NoError(t, err)
	return assessment
}

func TestRecoverMaintenanceBatchConsistent(t *testing.T) {
	for _, op := range []string{"0/orphan-release", "0/orphan-attachments", "0/null-archived", "0/runner-owner", "0/topics", "0/oauth2-apps", "0/owner-teams", "0/user-type", "0/team-8312"} {
		assessment := recoverRepositoryCase(t, "ord:maintenance/"+op+"/0123456789abcdef", Scope{
			Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyMaintenance,
		}, nil)
		require.Equal(t, RecoveryReleased, assessment.Verdict, op)
		require.Equal(t, "consistent", assessment.Effect, op)
	}
}

func TestRecoverMaintenanceLFSGCConsistent(t *testing.T) {
	assessment := recoverRepositoryCase(t, "ord:maintenance/1/lfs-gc/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyMaintenance, RepositoryID: 1,
	}, nil)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, "consistent", assessment.Effect)
}

func TestRecoverMaintenanceIsEmpty(t *testing.T) {
	assessment := recoverRepositoryCase(t, "ord:maintenance/1/is-empty/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyMaintenance, RepositoryID: 1,
	}, nil)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, "repaired", assessment.Effect)

	assessment = recoverRepositoryCase(t, "ord:maintenance/1/is-empty/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyMaintenance, RepositoryID: 1,
	}, func(ctx context.Context) {
		_, err := db.GetEngine(ctx).ID(1).Cols("is_empty").Update(&repo_model.Repository{IsEmpty: true})
		require.NoError(t, err)
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
}

func TestRecoverMaintenanceGCEmpty(t *testing.T) {
	assessment := recoverRepositoryCase(t, "ord:maintenance/1/gc/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyMaintenance, RepositoryID: 1,
	}, func(ctx context.Context) {
		_, err := db.GetEngine(ctx).ID(1).Cols("is_empty").Update(&repo_model.Repository{IsEmpty: true})
		require.NoError(t, err)
	})
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, "consistent", assessment.Effect)
}

func TestRecoverMaintenanceGCMissingDirFences(t *testing.T) {
	// The fixture default-branch tip cannot be read in unit tests,
	// so a non-empty collection fences for fsck.
	assessment := recoverRepositoryCase(t, "ord:maintenance/1/gc/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyMaintenance, RepositoryID: 1,
	}, nil)
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryMissingEvidence, assessment.Reason)
}

func TestRecoverRefSyncPurgeConsistent(t *testing.T) {
	assessment := recoverRepositoryCase(t, "ord:ref-sync/0/deleted-branches/0123456789abcdef", Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyRefSync,
	}, nil)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, "consistent", assessment.Effect)
}

func recoverPushCase(t *testing.T, tips map[string]string, scope Scope, setup func(ctx context.Context)) RecoveryAssessment {
	t.Helper()
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	if setup != nil {
		setup(ctx)
	}
	useIsolatedAppData(t)
	svc := admissionService(t, tips, 0)
	claimed := claimOrdinaryOwner(t, ctx, "ord:push-completion/1/batch-1/0123456789abcdef", scope)
	inhibitDomain(t)
	assessment, err := svc.recoverPushCompletion(ctx, &RecoveryAssessment{
		Owner:      claimed.Owner,
		Generation: claimed.Generation,
		OwnerKind:  claimed.OwnerKind,
		Family:     FamilyPushCompletion,
		Verdict:    RecoveryFenced,
	}, claimed, scope)
	require.NoError(t, err)
	return assessment
}

func pushScope() Scope {
	return Scope{
		Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyPushCompletion, RepositoryID: 1,
		Refs: []ScopedRef{{Ref: "refs/heads/master", NewOID: "65f1bf27bc3bf70f64657658635e66094edbcb4d"}},
	}
}

func TestRecoverPushCompletionConsistent(t *testing.T) {
	assessment := recoverPushCase(t, map[string]string{"refs/heads/master": "65f1bf27bc3bf70f64657658635e66094edbcb4d"}, pushScope(), nil)
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, "consistent", assessment.Effect)
}

func TestRecoverPushCompletionTornRunFences(t *testing.T) {
	assessment := recoverPushCase(t, map[string]string{"refs/heads/master": "65f1bf27bc3bf70f64657658635e66094edbcb4d"}, pushScope(), func(ctx context.Context) {
		run := &actions_model.ActionRun{
			Title: "push-torn", RepoID: 1, OwnerID: 2, TriggerUserID: 2,
			Status: actions_model.StatusWaiting,
		}
		require.NoError(t, actions_model.InsertRun(ctx, run, nil))
		require.NoError(t, db.Insert(ctx, &actions_model.ActionRunJob{
			RunID: run.ID, RepoID: 1, OwnerID: 2,
			Name: "torn-job", JobID: "torn-job", Status: actions_model.StatusWaiting,
			CommitSHA: "65f1bf27bc3bf70f64657658635e66094edbcb4d",
			TaskID:    99999,
		}))
	})
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverPushCompletionMovedRefFences(t *testing.T) {
	assessment := recoverPushCase(t, map[string]string{"refs/heads/master": "0000000000000000000000000000000000000001"}, pushScope(), nil)
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

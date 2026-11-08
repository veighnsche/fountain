// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"testing"
	"time"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

// TestClassifyOrdinaryOwnerMultiRefScope pins the multi-ref ordinary
// gate: a proven ref listed in the scope's ref batch is admitted, an
// unlisted ref refuses, and a batch scope with no single ref admits
// exactly its members. Single-ref verdicts are covered by
// TestClassifyOrdinaryOwnerScope and must not change.
func TestClassifyOrdinaryOwnerMultiRefScope(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := admissionService(t, nil, time.Now().Unix())
	encoded, err := encodeScope(Scope{
		Kind:         nativeoperation_model.OwnerOrdinary,
		Family:       FamilyReceiveHTTP,
		RepositoryID: 1,
		Refs: []ScopedRef{
			{Ref: "refs/heads/one", OldOID: testBase, NewOID: testHead},
			{Ref: "refs/heads/two", OldOID: testBase, NewOID: testHead},
		},
	})
	require.NoError(t, err)
	owner := "ord:receive-http/1/main/z"
	_, err = nativeoperation_model.ClaimOrdinary(ctx, owner, encoded, execcontext.Verifier("receive-proof"))
	require.NoError(t, err)
	defer func() { require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, owner)) }()

	allowed, err := svc.ClassifyTransaction(ctx, TransactionRequest{
		OwnerName: "user2", RepoName: "repo1", Phase: PhasePrepared,
		Lines: []RefLine{
			{Old: testBase, New: testHead, Ref: "refs/heads/one"},
			{Old: testBase, New: testHead, Ref: "refs/heads/two"},
		},
		Proof: "receive-proof",
	})
	require.NoError(t, err)
	require.True(t, allowed.Allowed)

	denied, err := svc.ClassifyTransaction(ctx, TransactionRequest{
		OwnerName: "user2", RepoName: "repo1", Phase: PhasePrepared,
		Lines: []RefLine{{Old: testBase, New: testHead, Ref: "refs/heads/other"}},
		Proof: "receive-proof",
	})
	require.NoError(t, err)
	require.False(t, denied.Allowed)

	completed, err := svc.ClassifyCompletion(ctx, 1, []string{"refs/heads/one", "refs/heads/two"}, "receive-proof", false)
	require.NoError(t, err)
	require.True(t, completed.Allowed)

	completedDenied, err := svc.ClassifyCompletion(ctx, 1, []string{"refs/heads/other"}, "receive-proof", false)
	require.NoError(t, err)
	require.False(t, completedDenied.Allowed)

	for _, denied := range []struct {
		repo  int64
		refs  []string
		proof string
	}{
		{1, []string{"refs/heads/one"}, "forged"},
		{2, []string{"refs/heads/one"}, "receive-proof"},
		{1, []string{"refs/heads/other"}, "receive-proof"},
	} {
		decision, err := svc.ClassifyCompletion(ctx, denied.repo, denied.refs, denied.proof, true)
		require.NoError(t, err)
		require.False(t, decision.Allowed)
	}
	withOptions, err := svc.ClassifyCompletion(ctx, 1, []string{"refs/heads/one"}, "receive-proof", true)
	require.NoError(t, err)
	require.True(t, withOptions.Allowed)
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	scope, err := decodeScope(reservation.Scope)
	require.NoError(t, err)
	require.True(t, scope.ReceiveHasPushOptions)
	assessment, err := svc.recoverReceive(ctx, &RecoveryAssessment{}, reservation, scope)
	require.NoError(t, err)
	require.Equal(t, RecoveryFenced, assessment.Verdict)
}

// TestCollabPullCreateRefinesPRRef pins the pull-creation gate: the
// derived PR ref is unknowable at claim time, so the hook-time
// declaration admits the operation's own ref once refined, while a
// non-PR ref under the same owner still refuses.
func TestCollabPullCreateRefinesPRRef(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := admissionService(t, nil, time.Now().Unix())
	encoded, err := encodeScope(Scope{
		Kind:         nativeoperation_model.OwnerOrdinary,
		Family:       FamilyCollaboration,
		RepositoryID: 1,
	})
	require.NoError(t, err)
	owner := "ord:collaboration/pull/new/1/abcdef0123456789"
	_, err = nativeoperation_model.ClaimOrdinary(ctx, owner, encoded, execcontext.Verifier("pull-proof"))
	require.NoError(t, err)
	defer func() { require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, owner)) }()

	classify := func(ref string) TransactionDecision {
		decision, err := svc.ClassifyTransaction(ctx, TransactionRequest{
			OwnerName: "user2", RepoName: "repo1", Phase: PhasePrepared,
			Lines: []RefLine{{Old: "0000000000000000000000000000000000000000", New: testHead, Ref: ref}},
			Proof: "pull-proof",
		})
		require.NoError(t, err)
		return decision
	}

	denied := classify("refs/pull/1/head")
	require.False(t, denied.Allowed)

	RefineCollabPullScope(ctx, "pull-proof", []ScopedRef{{Ref: "refs/pull/1/head", NewOID: testHead}})
	allowed := classify("refs/pull/1/head")
	require.True(t, allowed.Allowed, "refused: %s", allowed.Reason)

	RefineCollabPullScope(ctx, "pull-proof", []ScopedRef{{Ref: "refs/heads/main", NewOID: testHead}})
	stillDenied := classify("refs/heads/main")
	require.False(t, stillDenied.Allowed)
}

// TestCollabPullRefinementNoOps pins the refinement boundaries: forged
// proofs, other collaboration operations and other families never
// refine, so their ref pushes keep the existing strict checking.
func TestCollabPullRefinementNoOps(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	svc := admissionService(t, nil, time.Now().Unix())

	claim := func(owner string) {
		encoded, err := encodeScope(Scope{
			Kind:         nativeoperation_model.OwnerOrdinary,
			Family:       FamilyCollaboration,
			RepositoryID: 1,
		})
		require.NoError(t, err)
		_, err = nativeoperation_model.ClaimOrdinary(ctx, owner, encoded, execcontext.Verifier("pull-proof"))
		require.NoError(t, err)
	}
	classify := func() TransactionDecision {
		decision, err := svc.ClassifyTransaction(ctx, TransactionRequest{
			OwnerName: "user2", RepoName: "repo1", Phase: PhasePrepared,
			Lines: []RefLine{{Old: "0000000000000000000000000000000000000000", New: testHead, Ref: "refs/pull/1/head"}},
			Proof: "pull-proof",
		})
		require.NoError(t, err)
		return decision
	}

	claim("ord:collaboration/pull/new/1/abcdef0123456789")
	RefineCollabPullScope(ctx, "forged-proof", []ScopedRef{{Ref: "refs/pull/1/head", NewOID: testHead}})
	require.False(t, classify().Allowed)
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, "ord:collaboration/pull/new/1/abcdef0123456789"))

	claim("ord:collaboration/issue/1/title/abcdef0123456789")
	RefineCollabPullScope(ctx, "pull-proof", []ScopedRef{{Ref: "refs/pull/1/head", NewOID: testHead}})
	require.False(t, classify().Allowed)
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, "ord:collaboration/issue/1/title/abcdef0123456789"))
}

func TestPRSweepBindsDerivedHeadRefs(t *testing.T) {
	for _, test := range []struct {
		name, ref, branch, resource, proof string
		headRepoID, baseRepoID             int64
		wiki, deletion, allowed            bool
	}{
		{name: "same repository", headRepoID: 1, baseRepoID: 1, branch: "branch2", ref: "refs/pull/3/head", allowed: true},
		{name: "fork head", headRepoID: 11, baseRepoID: 10, branch: "branch2", ref: "refs/pull/1/head", allowed: true},
		{name: "unrelated repository", headRepoID: 1, baseRepoID: 10, branch: "branch2", ref: "refs/pull/1/head"},
		{name: "different head branch", headRepoID: 1, baseRepoID: 1, branch: "master", ref: "refs/pull/3/head"},
		{name: "missing PR", headRepoID: 1, baseRepoID: 1, branch: "branch2", ref: "refs/pull/999/head"},
		{name: "merged PR", headRepoID: 1, baseRepoID: 1, branch: "branch1", ref: "refs/pull/2/head"},
		{name: "branch write", headRepoID: 1, baseRepoID: 1, branch: "branch2", ref: "refs/heads/branch2"},
		{name: "merge ref", headRepoID: 1, baseRepoID: 1, branch: "branch2", ref: "refs/pull/3/merge"},
		{name: "deletion", headRepoID: 1, baseRepoID: 1, branch: "branch2", ref: "refs/pull/3/head", deletion: true},
		{name: "wiki", headRepoID: 1, baseRepoID: 1, branch: "branch2", ref: "refs/pull/3/head", wiki: true},
		{name: "other collaboration", headRepoID: 1, baseRepoID: 1, branch: "branch2", ref: "refs/pull/3/head", resource: "issue/1/title"},
		{name: "wrong proof", headRepoID: 1, baseRepoID: 1, branch: "branch2", ref: "refs/pull/3/head", proof: "forged"},
	} {
		t.Run(test.name, func(t *testing.T) {
			unittest.PrepareTestEnv(t)
			resource := test.resource
			if resource == "" {
				resource = execcontext.CollabBatchResource("pr-sweep")
			}
			proof := test.proof
			if proof == "" {
				proof = "sweep-proof"
			}
			scope, err := encodeScope(Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyCollaboration, RepositoryID: test.headRepoID, HeadRef: "refs/heads/" + test.branch})
			require.NoError(t, err)
			_, err = nativeoperation_model.ClaimOrdinary(t.Context(), "ord:collaboration/"+resource+"/0123456789abcdef", scope, execcontext.Verifier("sweep-proof"))
			require.NoError(t, err)
			repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: test.baseRepoID})
			owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: repo.OwnerID})
			repoName := repo.Name
			if test.wiki {
				repoName += ".wiki"
			}
			newOID := testHead
			if test.deletion {
				newOID = "0000000000000000000000000000000000000000"
			}
			decision, err := NewService().ClassifyTransaction(t.Context(), TransactionRequest{OwnerName: owner.Name, RepoName: repoName, Phase: PhasePrepared, Proof: proof, Lines: []RefLine{{Ref: test.ref, Old: testBase, New: newOID}}})
			require.NoError(t, err)
			require.Equal(t, test.allowed, decision.Allowed, decision.Reason)
		})
	}
}

func TestPushOptionsRequireOrdinaryMainReceive(t *testing.T) {
	unittest.PrepareTestEnv(t)
	svc := admissionService(t, nil, time.Now().Unix())
	for _, tc := range []struct {
		family, scopeFamily, resource string
		allowed                       bool
	}{
		{FamilyReceiveHTTP, FamilyReceiveHTTP, "1/main", true},
		{FamilyReceiveSSH, FamilyReceiveSSH, "1/main", true},
		{FamilyReceiveHTTP, FamilyReceiveHTTP, "1/wiki", false},
		{FamilyReceiveSSH, FamilyReceiveSSH, "1/wiki", false},
		{FamilyReceiveHTTP, FamilyReceiveSSH, "1/main", false},
		{FamilyRefWrite, FamilyRefWrite, "1/main", false},
	} {
		t.Run(tc.family+"/"+tc.scopeFamily+"/"+tc.resource, func(t *testing.T) {
			encoded, err := encodeScope(Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: tc.scopeFamily, RepositoryID: 1, Ref: "refs/heads/master"})
			require.NoError(t, err)
			owner := "ord:" + tc.family + "/" + tc.resource + "/nonce"
			_, err = nativeoperation_model.ClaimOrdinary(t.Context(), owner, encoded, execcontext.Verifier("options-proof"))
			require.NoError(t, err)
			defer func() { require.NoError(t, nativeoperation_model.ReleaseOwner(t.Context(), owner)) }()
			decision, err := svc.ClassifyCompletion(t.Context(), 1, []string{"refs/heads/master"}, "options-proof", true)
			require.NoError(t, err)
			require.Equal(t, tc.allowed, decision.Allowed)
		})
	}
}

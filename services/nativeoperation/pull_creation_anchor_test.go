// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"testing"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestPullCreationUnknownAnchorRefinesOnlyProvenPRScope(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	owner := collabNonceOwner(execcontext.PullCreateResource(0))
	before, err := encodeScope(Scope{Kind: nativeoperation_model.OwnerOrdinary, Family: FamilyCollaboration, RepositoryID: 1, NewOID: testHead})
	require.NoError(t, err)
	_, err = nativeoperation_model.ClaimOrdinary(ctx, owner, before, execcontext.Verifier("zero-anchor-proof"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, nativeoperation_model.ReleaseOwner(context.WithoutCancel(ctx), owner)) })

	refs := []ScopedRef{{Ref: "refs/pull/1/head", NewOID: testHead}}
	result, err := RefineCollabPullScopeChecked(ctx, "forged-proof", refs)
	require.ErrorIs(t, err, nativeoperation_model.ErrBusy)
	require.Equal(t, RefinementNotApplicable, result)
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, before, held.Scope)

	result, err = RefineCollabPullScopeChecked(ctx, "zero-anchor-proof", refs)
	require.NoError(t, err)
	require.Equal(t, RefinementUpdated, result)
	held, err = nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	refined := held.Scope
	result, err = RefineCollabPullScopeChecked(ctx, "zero-anchor-proof", []ScopedRef{{Ref: "refs/heads/main", NewOID: testHead}})
	require.NoError(t, err)
	require.Equal(t, RefinementNotApplicable, result)
	held, err = nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, refined, held.Scope)
	svc := admissionService(t, nil, 0)
	request := TransactionRequest{
		OwnerName: "user2", RepoName: "repo1", Phase: PhasePrepared,
		Origin: execcontext.GitOriginInternalRef, Proof: "zero-anchor-proof",
		Lines: []RefLine{{Old: "0000000000000000000000000000000000000000", New: testHead, Ref: "refs/pull/1/head"}},
	}
	decision, err := svc.ClassifyTransaction(ctx, request)
	require.NoError(t, err)
	require.True(t, decision.Allowed, decision.Reason)

	for _, test := range []struct {
		name string
		edit func(*TransactionRequest)
	}{
		{"forged proof", func(req *TransactionRequest) { req.Proof = "forged-proof" }},
		{"wrong repository", func(req *TransactionRequest) { req.RepoName = "repo2" }},
		{"ordinary branch", func(req *TransactionRequest) {
			req.Lines = []RefLine{{Old: testBase, New: testHead, Ref: "refs/heads/main"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.edit(&changed)
			decision, err := svc.ClassifyTransaction(ctx, changed)
			require.NoError(t, err)
			require.False(t, decision.Allowed)
		})
	}
}

func TestPullCreationUnknownAnchorRecoveryKeepsStoppedOwnerFenced(t *testing.T) {
	assessment := recoverCollab(t, execcontext.PullCreateResource(0), collabScope(1))
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
	held, err := nativeoperation_model.ReadReservation(t.Context())
	require.NoError(t, err)
	require.Equal(t, assessment.Owner, held.Owner)
	require.Equal(t, assessment.Generation, held.Generation)
}

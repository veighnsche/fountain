// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"testing"
	"time"

	"forgejo.org/services/nativeownership"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"
	execcontext "forgejo.org/modules/nativeoperation"

	"github.com/stretchr/testify/require"
)

func TestTransactionBindsWikiTarget(t *testing.T) {
	for _, test := range []struct {
		name, family, resource, repository string
		allowed                            bool
	}{
		{"main file", FamilyRefWrite, execcontext.RefWriteResource(1, RefWriteFile, "refs/heads/master"), "repo1", true},
		{"wiki file", FamilyRefWrite, execcontext.RefWriteResource(1, RefWriteFile, "wiki/refs/heads/master"), "repo1.wiki", true},
		{"main proof on wiki", FamilyRefWrite, execcontext.RefWriteResource(1, RefWriteFile, "refs/heads/master"), "repo1.wiki", false},
		{"wiki proof on main", FamilyRefWrite, execcontext.RefWriteResource(1, RefWriteFile, "wiki/refs/heads/master"), "repo1", false},
		{"wiki SSH receive", FamilyReceiveSSH, "1/wiki", "repo1.wiki", true},
		{"main SSH proof on wiki", FamilyReceiveSSH, "1/main", "repo1.wiki", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			unittest.PrepareTestEnv(t)
			ctx := t.Context()
			scope, err := encodeScope(Scope{Family: test.family, RepositoryID: 1, Ref: "refs/heads/master"})
			require.NoError(t, err)
			_, err = nativeoperation_model.ClaimOrdinary(ctx, nativeownership.NewOwner(test.family, test.resource), scope, execcontext.Verifier("wiki-target-proof"))
			require.NoError(t, err)
			decision, err := NewService().ClassifyTransaction(ctx, TransactionRequest{
				OwnerName: "user2", RepoName: test.repository, Phase: PhasePrepared,
				Lines: []RefLine{{Old: testBase, New: testHead, Ref: "refs/heads/master"}}, Proof: "wiki-target-proof",
			})
			require.NoError(t, err)
			require.Equal(t, test.allowed, decision.Allowed)
		})
	}
}

func TestConditionalMergeCannotWriteWiki(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	now := time.Now().Unix()
	service := admissionService(t, map[string]string{"refs/heads/branch2": testHead}, now)
	_, proof := claimMergeOwner(t, ctx, writerToken(t), "op-wiki-refusal", mergeScope(), now)
	decision, err := service.ClassifyTransaction(ctx, TransactionRequest{
		OwnerName: "user2", RepoName: "repo1.wiki", Phase: PhasePrepared,
		Lines: []RefLine{{Old: testBase, New: testHead, Ref: "refs/heads/master"}}, Proof: proof,
	})
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	operation, err := nativeoperation_model.LookupOperation(ctx, operationTestInstallation, "op-wiki-refusal")
	require.NoError(t, err)
	require.NotNil(t, operation)
	require.False(t, operation.Admitted)
}

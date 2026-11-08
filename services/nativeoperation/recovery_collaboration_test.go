// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"testing"

	execcontext "forgejo.org/modules/nativeoperation"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

// collabNonceOwner builds a well-formed collaboration owner string: the
// recovery parser requires the 16-hex nonce of a real claim.
func collabNonceOwner(resource string) string {
	return "ord:" + FamilyCollaboration + "/" + resource + "/0123456789abcdef"
}

func recoverCollab(t *testing.T, resource string, scope Scope) RecoveryAssessment {
	t.Helper()
	unittest.PrepareTestEnv(t)
	ctx := t.Context()
	useIsolatedAppData(t)
	svc := admissionService(t, nil, 0)

	claimed := claimOrdinaryOwner(t, ctx, collabNonceOwner(resource), scope)
	inhibitDomain(t)

	assessment := RecoveryAssessment{Owner: claimed.Owner, Generation: claimed.Generation, OwnerKind: claimed.OwnerKind, Verdict: RecoveryFenced}
	result, err := svc.recoverCollaboration(ctx, &assessment, claimed, scope)
	require.NoError(t, err)
	return result
}

func collabScope(repoID int64) Scope {
	return Scope{Kind: nativeoperation_model.OwnerOrdinary, RepositoryID: repoID, Family: FamilyCollaboration}
}

func TestRecoverCollabIssueUpdateConsistent(t *testing.T) {
	assessment := recoverCollab(t, execcontext.IssueResource(1, "title"), collabScope(1))
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectCollaborationConsistent, assessment.Effect)
}

func TestRecoverCollabDependencyConsistent(t *testing.T) {
	assessment := recoverCollab(t, execcontext.DependencyResource(1, 2), collabScope(1))
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectCollaborationConsistent, assessment.Effect)
}

func TestRecoverCollabReviewSubmitConsistent(t *testing.T) {
	assessment := recoverCollab(t, execcontext.ReviewSubmitResource(1), collabScope(1))
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectCollaborationConsistent, assessment.Effect)
}

func TestRecoverCollabReactionConsistent(t *testing.T) {
	assessment := recoverCollab(t, execcontext.ReactionResource(1, 0, 2), collabScope(1))
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectCollaborationConsistent, assessment.Effect)
}

func TestRecoverCollabCommentConversationConsistent(t *testing.T) {
	assessment := recoverCollab(t, execcontext.CommentResource(1, "conversation"), collabScope(1))
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectCollaborationConsistent, assessment.Effect)
}

func TestRecoverCollabReviewDeleteConsistent(t *testing.T) {
	assessment := recoverCollab(t, execcontext.ReviewDeleteResource(1, 2), collabScope(1))
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, EffectCollaborationConsistent, assessment.Effect)
}

func TestRecoverCollabCommentUpdateFences(t *testing.T) {
	assessment := recoverCollab(t, execcontext.CommentResource(1, "update"), collabScope(1))
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverCollabCreateFences(t *testing.T) {
	assessment := recoverCollab(t, execcontext.IssueCreateResource(1), collabScope(1))
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverCollabStatusFences(t *testing.T) {
	assessment := recoverCollab(t, execcontext.IssueResource(1, "status"), collabScope(1))
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverCollabBatchFences(t *testing.T) {
	assessment := recoverCollab(t, execcontext.CollabBatchResource("orphan-fix"), collabScope(1))
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverCollabMissingAnchorFences(t *testing.T) {
	assessment := recoverCollab(t, execcontext.IssueResource(9999, "title"), collabScope(1))
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnaccounted, assessment.Reason)
}

func TestRecoverCollabUnparseableFences(t *testing.T) {
	assessment := recoverCollab(t, "issue/NaN/title", collabScope(1))
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUnknownFamily, assessment.Reason)
}

func TestRecoverCollabAttachmentDeleteAbsent(t *testing.T) {
	assessment := recoverCollab(t, execcontext.AttachmentResource(9999, execcontext.CollabAttachmentDelete), collabScope(1))
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, "deleted", assessment.Effect)
}

func TestRecoverCollabAttachmentDeletePresent(t *testing.T) {
	assessment := recoverCollab(t, execcontext.AttachmentResource(1, execcontext.CollabAttachmentDelete), collabScope(1))
	require.Equal(t, RecoveryReleased, assessment.Verdict)
	require.Equal(t, nativeoperation_model.EffectNotCommitted, assessment.Effect)
}

func TestRecoverCollabAttachmentCreateFences(t *testing.T) {
	assessment := recoverCollab(t, execcontext.AttachmentCreateResource(1), collabScope(1))
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUncertainEffect, assessment.Reason)
}

func TestRecoverCollabAttachmentUpdateFences(t *testing.T) {
	assessment := recoverCollab(t, execcontext.AttachmentResource(1, execcontext.CollabAttachmentUpdate), collabScope(1))
	require.Equal(t, RecoveryFenced, assessment.Verdict)
	require.Equal(t, ReasonRecoveryUncertainEffect, assessment.Reason)
}

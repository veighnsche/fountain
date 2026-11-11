// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"strings"

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
)

func (s *Service) recoverConditionalThreadResolve(ctx context.Context, a *RecoveryAssessment, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	payload, err := conditionalRecoveryPayload(op, nativeoperation_model.KindThreadResolve)
	if err != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "thread resolve canonical capture differs")
	}
	want, err := parseThreadResolvePayload(payload, op.RepositoryID)
	if err != nil {
		return fenced(a, ReasonRecoveryUncertainEffect, "thread resolve payload differs")
	}
	expected := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: op.RepositoryID, Ref: want.BaseRef, OldOID: want.ExpectedBaseOID,
		HeadRef: want.HeadRef, HeadOID: want.ExpectedHeadOID, PRNumber: want.PullRequestNumber}
	if op.Admitted {
		repository, err := repo_model.GetRepositoryByID(ctx, op.RepositoryID)
		if err != nil {
			return fenced(a, ReasonRecoveryMissingEvidence, "thread resolve repository is missing")
		}
		head, errHead := s.readRef(ctx, repository.RepoPath(), want.HeadRef)
		base, errBase := s.readRef(ctx, repository.RepoPath(), want.BaseRef)
		if errHead != nil || errBase != nil || !strings.EqualFold(head, want.ExpectedHeadOID) || !strings.EqualFold(base, want.ExpectedBaseOID) {
			return fenced(a, ReasonRecoveryMissingEvidence, "thread resolve recorded candidate refs differ")
		}
	}
	return s.recoverConditionalSQL(ctx, a, held, scope, op, expected, func(tx context.Context, current *nativeoperation_model.Operation) (string, error) {
		var receipt sdk.ThreadResolveReceipt
		if exactConditionalReceipt(current.Receipt, &receipt) != nil || receipt.RootCommentID != want.RootCommentID || receipt.PullRequestNumber != want.PullRequestNumber ||
			!receipt.Resolved || receipt.ConversationDigest != strings.ToLower(want.ConversationDigest) {
			return "", errConditionalRecoveryEvidence
		}
		pr, err := issues_model.GetPullRequestByIndex(tx, current.RepositoryID, want.PullRequestNumber)
		if err != nil || pr.HeadRepoID != want.HeadRepositoryID || pr.BaseBranch != strings.TrimPrefix(want.BaseRef, git.BranchPrefix) ||
			pr.HeadBranch != strings.TrimPrefix(want.HeadRef, git.BranchPrefix) || pr.LoadIssue(tx) != nil || !pr.Issue.IsPull || pr.Issue.RepoID != current.RepositoryID {
			return "", errConditionalRecoveryEvidence
		}
		comments, _, err := issues_model.FetchThreadComments(tx, pr.Issue)
		if err != nil {
			return "", err
		}
		thread := issues_model.FindCodeThread(issues_model.GroupCodeThreads(comments), want.RootCommentID)
		if len(thread) == 0 || thread.Root().ID != want.RootCommentID || thread.Root().ResolveDoerID == 0 || thread.Root().Review == nil || thread.Root().Review.IssueID != pr.IssueID {
			return "", errConditionalRecoveryEvidence
		}
		post, err := resolvedThreadDigest(thread, false)
		if err != nil {
			return "", err
		}
		if post != receipt.ConversationDigest {
			// MarkConversation changes only an unresolved root. Already-resolved
			// roots are no-ops and may retain another actor's resolver identity.
			if thread.Root().ResolveDoerID != current.ActorID {
				return "", errConditionalRecoveryEvidence
			}
			before, err := resolvedThreadDigest(thread, true)
			if err != nil || before != receipt.ConversationDigest {
				return "", errConditionalRecoveryEvidence
			}
		}
		if current.Completion != nativeoperation_model.CompletionPending && current.Completion != nativeoperation_model.CompletionComplete {
			return "", errConditionalRecoveryEvidence
		}
		return nativeoperation_model.CompletionComplete, nil
	})
}

// resolvedThreadDigest uses the producer's conversation bytes. Restoring a
// zero resolver is a private in-memory view, never an update or an unresolve.
func resolvedThreadDigest(thread issues_model.CodeThread, before bool) (string, error) {
	side, line, ok := threadRecordedLocation(thread)
	if !ok {
		return "", errConditionalRecoveryEvidence
	}
	location := sdk.RenderThreadLocation(thread.Root().TreePath, side, line)
	lines := make([]string, 0, len(thread))
	for i, member := range thread {
		resolver := member.ResolveDoerID
		if before && i == 0 {
			resolver = 0
		}
		lines = append(lines, sdk.ResolveConversationLine(sdk.ResolveConversationMember{
			OriginalAnchor: threadOriginalAnchor(member.OriginalAnchor), ID: member.ID, Body: member.Content,
			ContentVer: int64(member.ContentVersion), Location: location, ResolverState: sdk.ThreadResolverState(resolver),
		}))
	}
	return sdk.ResolveConversationDigest(lines)
}

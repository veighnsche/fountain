// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json" //nolint:depguard // Exact persisted intent and native receipt.
	"errors"
	"time"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"

	"github.com/google/uuid"
)

const CrashPointHumanInlineBeforePrimary = "human-inline-before-primary"
const CrashPointHumanInlineAfterPrimary = "human-inline-after-primary"

func humanInlineIntent(q sdk.HumanInlineRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanInlineRequest(q) {
		return nil, ErrInvalidIntent
	}
	p := q.Expected.Review
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(p.ActorID), RepositoryID: nativeCommentID(p.RepositoryID), Kind: nativeoperation_model.KindHumanInline, AuthRevision: sdk.HumanInlineAuthorizationRevision, ExpectedNativeRevision: p.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanInlineIntent(q)
	return intent, err
}

// SubmitHumanInline composes fresh held Git proof, prepared native SQL and an
// exact journal receipt. The caller supplies current human request admission;
// no background binding or credential can authorize this operation.
func (s *Service) SubmitHumanInline(ctx context.Context, installation string, q sdk.HumanInlineRequest, admit HumanReviewAdmission) (sdk.OperationRecord, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	intent, err := humanInlineIntent(q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if admit(ctx) != nil {
		return sdk.OperationRecord{}, ErrAuthorityLost
	}
	existing, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if existing != nil {
		return s.reconcileExisting(existing, intent)
	}
	if q.NotAfter <= s.clock() || q.NotAfter > s.clock()+int64(MaxOperationLifetime/time.Second) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: intent.RepositoryID, IssueID: nativeCommentID(q.Expected.Review.IssueID)}
	return s.claimConditionalSubmit(ctx, extensionauth_model.SubmissionDecision{ActorID: intent.ActorID}, installation, intent, scope, func(ctx context.Context, op *nativeoperation_model.Operation, _ *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
		return s.executeHumanInline(ctx, op, q, admit, execution)
	})
}

func (s *Service) executeHumanInline(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanInlineRequest, admit HumanReviewAdmission, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owned, release := execcontext.NewContext(ctx, execution), context.WithoutCancel(ctx)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		r, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(r), terminalResult{}, nil
	}
	if err := TestCrashBarrier(CrashPointHumanInlineBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	var result *issues_model.PreparedHumanInlineResult
	var actor *user_model.User
	committed, err := nativeoperation_model.CommitHumanInlinePrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		if tx.Err() != nil || admit(tx) != nil {
			return "", ErrAuthorityLost
		}
		if s.clock() >= op.NotAfter {
			return "", nativeoperation_model.ErrOperationExpired
		}
		p := q.Expected.Review
		issue, err := issues_model.GetIssueByID(tx, nativeCommentID(p.IssueID))
		if err != nil || issue.RepoID != op.RepositoryID || issue.Index != nativeCommentID(p.Index) || !issue.IsPull {
			return "", ErrAuthorityLost
		}
		if err := issue.LoadPullRequest(tx); err != nil || issue.PullRequest == nil {
			return "", ErrAuthorityLost
		}
		if err := issue.LoadRepo(tx); err != nil {
			return "", err
		}
		actor, err = user_model.GetUserByID(tx, op.ActorID)
		if err != nil || !actor.IsAccessAllowed(tx) {
			return "", ErrAuthorityLost
		}
		native, err := git.OpenRepository(tx, issue.Repo.RepoPath())
		if err != nil {
			return "", err
		}
		defer native.Close()
		head, err := native.GetRefCommitID(issue.PullRequest.GetGitRefName())
		if err != nil {
			return "", issues_model.ErrExactReviewRefused{Reason: "head changed"}
		}
		base, err := native.GetRefCommitID(git.BranchPrefix + p.BaseBranch)
		if err != nil {
			return "", issues_model.ErrExactReviewRefused{Reason: "base changed"}
		}
		mergeBase, err := native.GetMergeBaseSimple(base, head)
		if err != nil {
			return "", issues_model.ErrExactReviewRefused{Reason: "merge base changed"}
		}
		a := q.Expected.Anchor
		anchor := issues_model.CodeCommentAnchor{Base: a.Base, Head: a.Head, Path: a.Path, Side: a.Side, Scope: a.Scope, StartLine: a.StartLine, EndLine: a.EndLine}
		proof, err := ProveHumanInlineAnchor(tx, native, anchor)
		if err != nil {
			return "", err
		}
		want := issues_model.PreparedHumanInlineTarget{Review: issues_model.PreparedHumanReviewTarget{RepositoryID: op.RepositoryID, ReviewerID: op.ActorID, PullID: nativeCommentID(p.PullID), IssueID: issue.ID, Index: issue.Index, BaseBranch: p.BaseBranch, IsClosed: p.IsClosed, HasMerged: p.HasMerged, IsAuthor: p.IsAuthor, HeadOID: p.HeadOID, BaseOID: p.BaseOID, MergeBaseOID: p.MergeBaseOID, CurrentHeadOID: head, CurrentBaseOID: base, CurrentMergeBaseOID: mergeBase, PendingReviewID: nativeCommentID(p.PendingReviewID), PendingCommitID: p.PendingCommitID}, Anchor: anchor, EntryOID: q.Expected.EntryOID, EntryKind: q.Expected.EntryKind, DiffDigest: q.Expected.DiffDigest, CurrentAnchor: anchor, CurrentEntryOID: proof.EntryOID, CurrentEntryKind: proof.EntryKind, CurrentDiffDigest: proof.DiffDigest}
		result, err = issues_model.CreatePreparedHumanInlineComment(tx, q.Body, q.Draft, want)
		if err != nil {
			return "", err
		}
		count, digest, err := humanReviewCommentsEvidence(tx, result.Review)
		if err != nil {
			return "", err
		}
		r := sdk.HumanInlineReceipt{RepositoryID: op.RepositoryID, ActorID: op.ActorID, PullID: want.Review.PullID, IssueID: issue.ID, Index: issue.Index, ReviewID: result.Review.ID, CommentID: result.Inline.ID, Draft: q.Draft, CommitID: result.Review.CommitID, Official: result.Review.Official, Stale: result.Review.Stale, BodyDigest: sdk.HumanReviewBodyDigest(q.Body), ReviewDigest: sdk.HumanReviewBodyDigest(result.Review.Content), Anchor: a, EntryOID: q.Expected.EntryOID, EntryKind: q.Expected.EntryKind, DiffDigest: q.Expected.DiffDigest, CommentsCount: count, CommentsDigest: digest}
		if result.History != nil {
			r.HistoryID = result.History.ID
		}
		if !sdk.ValidHumanInlineReceipt(r, q) {
			return "", errors.New("native human inline receipt differs")
		}
		if tx.Err() != nil {
			return "", ErrAuthorityLost
		}
		raw, err := json.Marshal(r)
		return string(raw), err
	})
	if err != nil {
		var deterministic issues_model.ErrExactReviewRefused
		switch {
		case errors.Is(err, ErrAuthorityLost):
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		case errors.As(err, &deterministic):
			reason := nativeoperation_model.ReasonNativeRefused
			switch deterministic.Reason {
			case "head changed":
				reason = nativeoperation_model.ReasonStaleHead
			case "base changed", "merge base changed":
				reason = nativeoperation_model.ReasonStaleBaseOrResult
			case "pending draft changed":
				reason = nativeoperation_model.ReasonPendingReviewExists
			case "reviewer is inactive", "reviewer cannot read this pull request", "reviewer is blocked", "review workflow unavailable":
				reason = nativeoperation_model.ReasonAuthorityLost
			case "human review parent changed", "human review pull changed":
				reason = nativeoperation_model.ReasonPRMismatch
			}
			return refuse(reason)
		case issues_model.IsContentEmptyErr(err):
			return refuse(nativeoperation_model.ReasonNativeRefused)
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			current, e := nativeoperation_model.LookupOperation(release, op.InstallationID, op.OperationID)
			if e != nil {
				return sdk.OperationRecord{}, terminalResult{}, e
			}
			if current != nil && current.Revoked {
				return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
			}
		}
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if err := TestCrashBarrier(CrashPointHumanInlineAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	completion := nativeoperation_model.CompletionComplete
	if !q.Draft {
		if owned.Err() != nil {
			completion = nativeoperation_model.CompletionNeedsIntervention
		} else if err := pull_service.CompleteReviewSubmission(owned, actor, result.Review.Issue, result.Review, result.History); err != nil {
			completion = nativeoperation_model.CompletionNeedsIntervention
		}
	}
	r, err := nativeoperation_model.SetCompletionAndRelease(release, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(r), terminalResult{}, nil
}

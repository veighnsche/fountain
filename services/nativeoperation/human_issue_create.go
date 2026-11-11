// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Persisted native intent and receipt.
	"errors"

	sdk "forgejo.org/extension-sdk"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	notify_service "forgejo.org/services/notify"
)

const CrashPointHumanIssueCreateBeforePrimary = "human-issue-create-before-primary"
const CrashPointHumanIssueCreateAfterPrimary = "human-issue-create-after-primary"

// HumanIssueCreateAdmission revalidates the current human/session, installation
// and exact request lifetime before replay/claim and inside the SQL transaction.
// Only trusted native request composition supplies it; preparation is not authority.
type HumanIssueCreateAdmission func(context.Context) error

func humanIssueCreateIntent(q sdk.HumanIssueCreateRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanIssueCreateRequest(q) {
		return nil, ErrInvalidIntent
	}
	intent := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(q.Expected.ActorID), RepositoryID: nativeCommentID(q.Expected.RepositoryID),
		Kind: nativeoperation_model.KindHumanIssueCreate, AuthRevision: sdk.HumanIssueCreateAuthorizationRevision, ExpectedNativeRevision: q.Expected.NativeRevision, NotAfter: q.NotAfter}
	var err error
	intent.Canonical, intent.Digest, err = sdk.CanonicalHumanIssueCreateIntent(q)
	return intent, err
}

// SubmitHumanIssueCreate creates an ordinary issue using the existing native
// journal and conditional owner. It requires no background token/enrollment and
// exposes no public route. Index allocation belongs to the primary transaction.
func (s *Service) SubmitHumanIssueCreate(ctx context.Context, installation string, q sdk.HumanIssueCreateRequest, admit HumanIssueCreateAdmission) (sdk.OperationRecord, error) {
	submission, record, err := s.beginHumanSubmission(ctx, installation, admit, func() (*ValidIntent, error) { return humanIssueCreateIntent(q) })
	if submission == nil {
		return record, err
	}
	intent := submission.intent
	scope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: intent.RepositoryID}
	return submission.run(scope,
		func(ctx context.Context, op *nativeoperation_model.Operation, _ *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
			return s.executeHumanIssueCreate(ctx, op, q, admit, execution)
		})
}

func humanIssueCreateTarget(ctx context.Context, q sdk.HumanIssueCreateRequest, admit HumanIssueCreateAdmission) (*repo_model.Repository, *user_model.User, error) {
	if ctx.Err() != nil || admit(ctx) != nil {
		return nil, nil, ErrAuthorityLost
	}
	actor, err := user_model.GetUserByID(ctx, nativeCommentID(q.Expected.ActorID))
	if err != nil || !actor.IsAccessAllowed(ctx) {
		return nil, nil, ErrAuthorityLost
	}
	repo, err := repo_model.GetRepositoryByID(ctx, nativeCommentID(q.Expected.RepositoryID))
	if err != nil || repo.IsArchived || user_model.IsBlocked(ctx, repo.OwnerID, actor.ID) {
		return nil, nil, ErrAuthorityLost
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, actor)
	if err != nil || unit.TypeIssues.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, unit.TypeIssues) || !permission.CanRead(unit.TypeIssues) {
		return nil, nil, ErrAuthorityLost
	}
	return repo, actor, nil
}

func (s *Service) executeHumanIssueCreate(ctx context.Context, op *nativeoperation_model.Operation, q sdk.HumanIssueCreateRequest, admit HumanIssueCreateAdmission, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owned := execcontext.NewContext(ctx, execution)
	release := context.WithoutCancel(ctx)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		record, err := nativeoperation_model.SetTerminal(release, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState: nativeoperation_model.EffectNotCommitted, Reason: reason, Cancellation: nativeoperation_model.CancellationNone}, execution.Owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(record), terminalResult{}, nil
	}
	if err := TestCrashBarrier(CrashPointHumanIssueCreateBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	var issue *issues_model.Issue
	var actor *user_model.User
	committed, err := nativeoperation_model.CommitHumanIssueCreatePrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		repo, current, err := humanIssueCreateTarget(tx, q, admit)
		if err != nil {
			return "", err
		}
		if s.clock() >= op.NotAfter {
			return "", nativeoperation_model.ErrOperationExpired
		}
		if tx.Err() != nil {
			return "", ErrAuthorityLost
		}
		actor = current
		issue = &issues_model.Issue{RepoID: repo.ID, Repo: repo, PosterID: actor.ID, Poster: actor, Title: q.Title, Content: q.Body}
		if err := issues_model.NewIssue(tx, repo, issue, nil, nil); err != nil {
			return "", err
		}
		sum := sha256.Sum256([]byte(issue.Content))
		raw, err := json.Marshal(sdk.HumanIssueCreateReceipt{RepositoryID: repo.ID, ActorID: actor.ID, IssueID: issue.ID, Index: issue.Index,
			Title: issue.Title, BodyDigest: hex.EncodeToString(sum[:]), ContentVersion: int64(issue.ContentVersion)})
		return string(raw), err
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrAuthorityLost):
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			current, readErr := nativeoperation_model.LookupOperation(release, op.InstallationID, op.OperationID)
			if readErr != nil {
				return sdk.OperationRecord{}, terminalResult{}, readErr
			}
			if current != nil && current.Revoked {
				return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
			}
		}
		// An unknown SQL/commit result keeps the reservation for offline recovery.
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if err := TestCrashBarrier(CrashPointHumanIssueCreateAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	completion := nativeoperation_model.CompletionComplete
	mentions, err := issues_model.FindAndUpdateIssueMentions(owned, issue, actor, issue.Content)
	if err != nil {
		completion = nativeoperation_model.CompletionNeedsIntervention
	} else {
		notify_service.NewIssue(owned, issue, mentions)
	}
	record, err := nativeoperation_model.SetCompletionAndRelease(release, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(record), terminalResult{}, nil
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later
package nativeoperation

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"io"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	notify_service "forgejo.org/services/notify"
)

func parseIssueGraphPayload(data []byte) (*sdk.IssueGraphPayload, error) {
	var p sdk.IssueGraphPayload
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil {
		return nil, ErrInvalidIntent
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidIntent
	}
	if sdk.ValidateIssueGraphPayload(p) != nil {
		return nil, ErrInvalidIntent
	}
	return &p, nil
}

func (s *Service) submitIssueGraph(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent) (sdk.OperationRecord, error) {
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if intent.IssueGraph == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: intent.RepositoryID}
	return s.claimBackgroundSubmit(ctx, decision, installationID, intent, scope)
}

// executeIssueGraph keeps native SQL changes and their receipt atomic. Mention
// delivery follows the primary commit; failure cannot erase committed issues.
func (s *Service) executeIssueGraph(ctx context.Context, op *nativeoperation_model.Operation, intent *ValidIntent, _ Scope, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	releaseCtx := context.WithoutCancel(ctx)
	owned := execcontext.NewContext(ctx, execution)
	refuse := conditionalRefusal(ctx, op, execution)
	repo, err := repo_model.GetRepositoryByID(owned, op.RepositoryID)
	if err != nil {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	actor, err := user_model.GetUserByID(owned, op.ActorID)
	if err != nil {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	permission, err := access_model.GetUserRepoPermission(owned, repo, actor)
	if err != nil || !permission.CanWrite(unit.TypeIssues) || repo.IsArchived || repo.IsMirror || user_model.IsBlocked(owned, repo.OwnerID, actor.ID) {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	if len(intent.IssueGraph.Dependencies) > 0 && !repo.IsDependenciesEnabled(owned) {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	if err := TestCrashBarrier(CrashPointIssueGraphBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	var changed []*issues_model.Issue
	committed, err := nativeoperation_model.CommitIssueGraphPrimary(owned, op.InstallationID, op.OperationID, execution.Owner, s.clock(), func(tx context.Context) (string, error) {
		if err := s.revalidateSubmission(tx, op); err != nil {
			return "", err
		}
		receipt, issues, err := applyIssueGraph(tx, repo, actor, intent.IssueGraph)
		if err != nil {
			return "", err
		}
		changed = issues
		encoded, err := json.Marshal(receipt)
		return string(encoded), err
	})
	if err != nil {
		switch {
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			current, readErr := nativeoperation_model.LookupOperation(releaseCtx, op.InstallationID, op.OperationID)
			if readErr != nil {
				return sdk.OperationRecord{}, terminalResult{}, readErr
			}
			if current != nil && current.Revoked {
				return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
			}
			return sdk.OperationRecord{}, terminalResult{}, err
		default:
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
	}
	if err := TestCrashBarrier(CrashPointIssueGraphAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	completion := nativeoperation_model.CompletionComplete
	for i, issue := range changed {
		mentions, err := issues_model.FindAndUpdateIssueMentions(owned, issue, actor, issue.Content)
		if err != nil {
			completion = nativeoperation_model.CompletionNeedsIntervention
			break
		}
		// Native notification services receive the real submitting actor; no
		// generated issue impersonates the accepted objective's human author.
		if i < len(intent.IssueGraph.Creates) {
			notify_service.NewIssue(owned, issue, mentions)
		} else {
			edit := intent.IssueGraph.Edits[i-len(intent.IssueGraph.Creates)]
			if edit.Title != edit.ExpectedTitle {
				notify_service.IssueChangeTitle(owned, actor, issue, edit.ExpectedTitle)
			}
			if edit.Body != edit.ExpectedBody {
				notify_service.IssueChangeContent(owned, actor, issue, edit.ExpectedBody)
			}
		}
	}
	recorded, err := nativeoperation_model.SetCompletionAndRelease(releaseCtx, committed.InstallationID, committed.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(recorded), terminalResult{}, nil
}

const (
	CrashPointIssueGraphBeforePrimary = "issue-graph-before-primary"
	CrashPointIssueGraphAfterPrimary  = "issue-graph-after-primary"
)

// applyIssueGraph is SQL-only and runs inside the atomic conditional primary.
// It invokes upstream native models for history, index/counter updates and
// dependency audit comments. It never closes an issue or declares an outcome.
func applyIssueGraph(ctx context.Context, repo *repo_model.Repository, actor *user_model.User, p *sdk.IssueGraphPayload) (*sdk.IssueGraphReceipt, []*issues_model.Issue, error) {
	receipt := &sdk.IssueGraphReceipt{ActorID: actor.ID, RepositoryID: repo.ID, Issues: []sdk.IssueGraphIssueReceipt{}, Dependencies: []sdk.IssueGraphDependencyReceipt{}}
	newByKey := map[string]*issues_model.Issue{}
	oldByID := map[int64]*issues_model.Issue{}
	changed := []*issues_model.Issue{}
	get := func(id int64) (*issues_model.Issue, error) {
		if issue := oldByID[id]; issue != nil {
			return issue, nil
		}
		issue, err := issues_model.GetIssueByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if issue.RepoID != repo.ID || issue.IsPull || issue.IsLocked {
			return nil, ErrInvalidIntent
		}
		if user_model.IsBlocked(ctx, issue.PosterID, actor.ID) {
			return nil, ErrAuthorityLost
		}
		issue.Repo = repo
		oldByID[id] = issue
		return issue, nil
	}
	// Validate every existing anchor before creating anything. Transaction
	// rollback also protects against errors discovered while applying changes.
	for _, e := range p.Edits {
		issue, err := get(e.IssueID)
		if err != nil {
			return nil, nil, err
		}
		if issue.IsClosed || issue.Title != e.ExpectedTitle || issue.Content != e.ExpectedBody || issue.ContentVersion != e.ExpectedContentVersion {
			return nil, nil, ErrInvalidIntent
		}
	}
	for _, e := range p.Dependencies {
		for _, r := range []sdk.IssueGraphRef{e.Issue, e.Prerequisite} {
			if r.IssueID > 0 {
				if _, err := get(r.IssueID); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	for _, c := range p.Creates {
		issue := &issues_model.Issue{RepoID: repo.ID, Repo: repo, PosterID: actor.ID, Poster: actor, Title: c.Title, Content: c.Body}
		if err := issues_model.NewIssue(ctx, repo, issue, nil, nil); err != nil {
			return nil, nil, err
		}
		newByKey[c.Key] = issue
		changed = append(changed, issue)
		receipt.Issues = append(receipt.Issues, sdk.IssueGraphIssueReceipt{Key: c.Key, IssueID: issue.ID, IssueIndex: issue.Index, ContentVersion: issue.ContentVersion})
	}
	for _, e := range p.Edits {
		issue := oldByID[e.IssueID]
		if issue.Title != e.Title {
			old := issue.Title
			issue.Title = e.Title
			if err := issues_model.ChangeIssueTitle(ctx, issue, actor, old); err != nil {
				return nil, nil, err
			}
		}
		if issue.Content != e.Body {
			if err := issues_model.ChangeIssueContent(ctx, issue, actor, e.Body, e.ExpectedContentVersion); err != nil {
				return nil, nil, err
			}
		}
		changed = append(changed, issue)
		receipt.Issues = append(receipt.Issues, sdk.IssueGraphIssueReceipt{IssueID: issue.ID, IssueIndex: issue.Index, ContentVersion: issue.ContentVersion})
	}
	resolve := func(r sdk.IssueGraphRef) *issues_model.Issue {
		if r.Key != "" {
			return newByKey[r.Key]
		}
		return oldByID[r.IssueID]
	}
	// Remove first so an edge replacement is evaluated against the final
	// graph rather than transient ordering. Missing removals fail atomically.
	for _, e := range p.Dependencies {
		if e.Remove {
			if err := issues_model.RemoveIssueDependency(ctx, actor, resolve(e.Issue), resolve(e.Prerequisite), issues_model.DependencyTypeBlockedBy); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, e := range p.Dependencies {
		issue, dep := resolve(e.Issue), resolve(e.Prerequisite)
		if !e.Remove {
			cycle, err := issueGraphReaches(ctx, dep.ID, issue.ID)
			if err != nil {
				return nil, nil, err
			}
			if cycle {
				return nil, nil, issues_model.ErrCircularDependency{}
			}
			if err := issues_model.CreateIssueDependency(ctx, actor, issue, dep); err != nil {
				return nil, nil, err
			}
		}
		receipt.Dependencies = append(receipt.Dependencies, sdk.IssueGraphDependencyReceipt{IssueID: issue.ID, PrerequisiteID: dep.ID, Removed: e.Remove})
	}
	return receipt, changed, nil
}

// issueGraphReaches follows native prerequisite edges, including intermediate
// issues outside this request. A cycle of any length rejects the whole commit.
func issueGraphReaches(ctx context.Context, start, target int64) (bool, error) {
	pending := []int64{start}
	seen := map[int64]bool{}
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if id == target {
			return true, nil
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		var edges []*issues_model.IssueDependency
		if err := db.GetEngine(ctx).Where("issue_id=?", id).Find(&edges); err != nil {
			return false, err
		}
		for _, edge := range edges {
			pending = append(pending, edge.DependencyID)
		}
	}
	return false, nil
}

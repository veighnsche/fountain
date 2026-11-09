// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"bytes"
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"io"
	"os"
	"strings"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	issue_service "forgejo.org/services/issue"
	notify_service "forgejo.org/services/notify"
	pull_service "forgejo.org/services/pull"
)

// PRCreateIntent carries the validated pull_request.create payload: one
// same-repository native PR over exact head/base refs and OIDs with final
// title and body. The first interface has no labels, assignees, milestone,
// attachments, fork source, update or retarget operation.
type PRCreateIntent struct {
	HeadRepositoryID    int64  `json:"head_repository_id"`
	HeadRef             string `json:"head_ref"`
	BaseRef             string `json:"base_ref"`
	ExpectedHeadOID     string `json:"expected_head_oid"`
	ExpectedBaseOID     string `json:"expected_base_oid"`
	Title               string `json:"title"`
	Body                string `json:"body"`
	AllowMaintainerEdit bool   `json:"allow_maintainer_edit"`
}

// maxPRCreateTitle is the native issue-title width. Over-limit titles reject
// rather than silently truncating the intent.
const maxPRCreateTitle = 255

// parsePRCreatePayload validates the complete immutable PR-create intent.
// Unknown fields, conflicting values and out-of-scope requests fail validation
// rather than silently broadening the request.
func parsePRCreatePayload(payload []byte, repositoryID int64) (*PRCreateIntent, error) {
	if len(payload) == 0 {
		return nil, ErrInvalidIntent
	}
	var pr PRCreateIntent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pr); err != nil {
		return nil, ErrInvalidIntent
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidIntent
	}
	// The first interface creates same-repository PRs only.
	if pr.HeadRepositoryID != repositoryID {
		return nil, ErrInvalidIntent
	}
	if !validBranchRef(pr.HeadRef) || !validBranchRef(pr.BaseRef) || pr.HeadRef == pr.BaseRef {
		return nil, ErrInvalidIntent
	}
	if !validOID(pr.ExpectedHeadOID) || !validOID(pr.ExpectedBaseOID) {
		return nil, ErrInvalidIntent
	}
	if strings.EqualFold(pr.ExpectedHeadOID, pr.ExpectedBaseOID) {
		return nil, ErrInvalidIntent
	}
	pr.ExpectedHeadOID = strings.ToLower(pr.ExpectedHeadOID)
	pr.ExpectedBaseOID = strings.ToLower(pr.ExpectedBaseOID)
	pr.Title = strings.TrimSpace(pr.Title)
	if pr.Title == "" || len(pr.Title) > maxPRCreateTitle {
		return nil, ErrInvalidIntent
	}
	if pr.AllowMaintainerEdit {
		return nil, ErrInvalidIntent
	}
	return &pr, nil
}

// PRCreateReceipt attributes one committed PR creation to its operation.
type PRCreateReceipt struct {
	PRID         int64  `json:"pr_id"`
	IssueID      int64  `json:"issue_id"`
	PRNumber     int64  `json:"pr_number"`
	AuthorID     int64  `json:"author_id"`
	RepositoryID int64  `json:"repository_id"`
	HeadRef      string `json:"head_ref"`
	BaseRef      string `json:"base_ref"`
	HeadOID      string `json:"head_oid"`
	BaseOID      string `json:"base_oid"`
}

// errPRCreateDuplicate rolls the primary SQL attempt back when the atomic
// in-transaction duplicate recheck finds an existing unmerged PR, so the
// caller refuses without a partial effect.
var errPRCreateDuplicate = errors.New("pull request already exists for head and base")

// submitPRCreate claims the operation ID and reservation, verifies the exact
// refs and native authority under the held owner, commits the primary issue/PR
// records with the operation receipt in one SQL transaction, and completes the
// derived ref and follow-up records under bounded completion ownership. There
// is no unguarded fallback: every refusal path records not_committed or
// retains the fence.
func (s *Service) submitPRCreate(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent) (sdk.OperationRecord, error) {
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if intent.PRCreate == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	dir, err := s.execDir()
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	capabilityPath, secret, err := execcontext.WriteCapabilityFile(dir)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	retire := func() { _ = os.Remove(capabilityPath) }
	owner := conditionalOwner(installationID, intent.OperationID)
	// The internal PR ref is allocated at the primary commit, so the claim
	// binds the verified head/base refs and OIDs; the exact derived-ref
	// tuple persists under this owner before derived Git writes.
	scope := Scope{
		Kind:         nativeoperation_model.OwnerConditional,
		RepositoryID: intent.RepositoryID,
		Ref:          intent.PRCreate.BaseRef,
		OldOID:       intent.PRCreate.ExpectedBaseOID,
		HeadRef:      intent.PRCreate.HeadRef,
		HeadOID:      intent.PRCreate.ExpectedHeadOID,
	}
	encoded, err := encodeScope(scope)
	if err != nil {
		retire()
		return sdk.OperationRecord{}, err
	}
	op := operationForSubmission(decision, installationID, intent)
	claimed, err := nativeoperation_model.ClaimConditional(ctx, op, owner, encoded, execcontext.Verifier(secret))
	if err != nil {
		retire()
		if errors.Is(err, nativeoperation_model.ErrBusy) {
			return sdk.OperationRecord{}, ErrBusy
		}
		if errors.Is(err, nativeoperation_model.ErrInhibited) {
			return sdk.OperationRecord{}, err
		}
		if errors.Is(err, nativeoperation_model.ErrStaleRevision) {
			return s.recordStaleRevision(ctx, op)
		}
		// A concurrent identical submit may have won the insert; reread
		// and reconcile rather than launching a second execution.
		existing, lookupErr := nativeoperation_model.LookupOperation(ctx, installationID, intent.OperationID)
		if lookupErr != nil {
			return sdk.OperationRecord{}, lookupErr
		}
		if existing != nil {
			return s.reconcileSubmission(existing, decision, intent)
		}
		return sdk.OperationRecord{}, err
	}
	execution := &execcontext.Execution{Owner: owner, Generation: claimed.Generation, CapabilityPath: capabilityPath}
	record, terminal, err := s.executePRCreate(ctx, op, intent, scope, execution)
	if err != nil {
		// Infrastructure failure before reconciliation: fail closed with
		// the owner held and the capability kept for later recovery.
		return sdk.OperationRecord{}, err
	}
	if !terminal.retain {
		// The terminal record already released the owner; retire the file.
		retire()
	}
	return record, nil
}

// executePRCreate runs the guarded creation and reconciles its result. It
// returns the SDK record and whether the owner stays held for an unresolved
// effect. All native work runs on the owned context so nested participating
// writers attribute to this execution; terminal records and the owner release
// run detached so a caller disconnect cannot stick the reservation.
func (s *Service) executePRCreate(ctx context.Context, op *nativeoperation_model.Operation, intent *ValidIntent, scope Scope, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owner := execution.Owner
	pr := intent.PRCreate
	// Terminal records and the owner release must complete even when the
	// caller disconnects mid-write; native work below keeps the live
	// context so cancellation still stops it.
	releaseCtx := context.WithoutCancel(ctx)
	owned := execcontext.NewContext(ctx, execution)
	refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
		recorded, err := nativeoperation_model.SetTerminal(releaseCtx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState:  nativeoperation_model.EffectNotCommitted,
			Reason:       reason,
			Cancellation: nativeoperation_model.CancellationNone,
		}, owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(recorded), terminalResult{}, nil
	}
	if err := s.revalidateSubmission(owned, op); err != nil {
		if errors.Is(err, ErrAuthorityLost) {
			return refuse(nativeoperation_model.ReasonAuthorityLost)
		}
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	repository, err := repo_model.GetRepositoryByID(owned, op.RepositoryID)
	if err != nil {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}
	if s.clock() >= op.NotAfter {
		return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
	}
	head, err := s.readRef(owned, repository.RepoPath(), pr.HeadRef)
	if err != nil || !strings.EqualFold(head, pr.ExpectedHeadOID) {
		return refuse(nativeoperation_model.ReasonStaleHead)
	}
	base, err := s.readRef(owned, repository.RepoPath(), pr.BaseRef)
	if err != nil || !strings.EqualFold(base, pr.ExpectedBaseOID) {
		return refuse(nativeoperation_model.ReasonStaleBaseOrResult)
	}
	doer, err := user_model.GetUserByID(owned, op.ActorID)
	if err != nil {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	// The ordinary service refuses authors the repository owner blocks;
	// the conditional path preserves that account check.
	if user_model.IsBlocked(owned, repository.OwnerID, doer.ID) {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	// Mirror the API layer's creation permissions, refreshed under the
	// held owner: pulls-unit and code-unit read on the base repository.
	// The submission binding already requires repository write, which
	// subsumes head code-unit read for this same-repository request.
	permission, err := access_model.GetUserRepoPermission(owned, repository, doer)
	if err != nil {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	if !permission.CanReadIssuesOrPulls(true) || !permission.CanRead(unit.TypeCode) {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	headBranch := strings.TrimPrefix(pr.HeadRef, git.BranchPrefix)
	baseBranch := strings.TrimPrefix(pr.BaseRef, git.BranchPrefix)
	existing, err := issues_model.GetUnmergedPullRequest(owned, op.RepositoryID, op.RepositoryID, headBranch, baseBranch, issues_model.PullRequestFlowGithub)
	if err != nil && !issues_model.IsErrPullRequestNotExist(err) {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if existing != nil {
		// An existing PR is a conflict even when its author, branch or
		// text matches; only this operation's receipt proves creation.
		return refuse(nativeoperation_model.ReasonDuplicatePullRequest)
	}

	issue := &issues_model.Issue{
		RepoID:   op.RepositoryID,
		Title:    pr.Title,
		PosterID: doer.ID,
		Poster:   doer,
		IsPull:   true,
		Content:  pr.Body,
	}
	pull := &issues_model.PullRequest{
		HeadRepoID: op.RepositoryID,
		BaseRepoID: op.RepositoryID,
		HeadBranch: headBranch,
		BaseBranch: baseBranch,
		HeadRepo:   repository,
		BaseRepo:   repository,
		Type:       issues_model.PullRequestGitea,
	}
	// Native comparison preparation under the held owner, shared with
	// the ordinary service path: merge base, head commit, mergeability
	// status and divergence. It performs Git reads only.
	if err := pull_service.PreparePullRequestComparison(owned, pull); err != nil {
		return refuse(nativeoperation_model.ReasonNativeRefused)
	}

	// Test-only barrier for the transaction/cancel race proof: with the
	// claim held, the driver revokes the operation here so the atomic
	// primary commit below must observe the cancellation and refuse.
	if err := TestCrashBarrier(CrashPointPRCreateBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}

	// Primary effect: the native issue/PR rows and the operation's
	// committed receipt in one SQL transaction, ordered against
	// cancellation and expiry on the same operation row. No SQL
	// transaction spans Git or wraps the entire service.
	committed, err := nativeoperation_model.CommitPRCreatePrimary(owned, op.InstallationID, op.OperationID, owner, s.clock(), func(tx context.Context) (string, error) {
		if err := s.revalidateSubmission(tx, op); err != nil {
			return "", err
		}
		// The duplicate check re-runs inside the commit as
		// defense-in-depth: ordinary writers are fenced while this
		// owner holds the reservation, so a match refuses rather than
		// inserting a duplicate.
		duplicate, err := issues_model.GetUnmergedPullRequest(tx, op.RepositoryID, op.RepositoryID, headBranch, baseBranch, issues_model.PullRequestFlowGithub)
		if err != nil && !issues_model.IsErrPullRequestNotExist(err) {
			return "", err
		}
		if duplicate != nil {
			return "", errPRCreateDuplicate
		}
		if err := issues_model.NewPullRequest(tx, repository, issue, nil, nil, pull); err != nil {
			return "", err
		}
		pull.Issue = issue
		issue.PullRequest = pull
		receipt, _ := json.Marshal(PRCreateReceipt{
			PRID:         pull.ID,
			IssueID:      issue.ID,
			PRNumber:     pull.Index,
			AuthorID:     doer.ID,
			RepositoryID: op.RepositoryID,
			HeadRef:      pr.HeadRef,
			BaseRef:      pr.BaseRef,
			HeadOID:      pr.ExpectedHeadOID,
			BaseOID:      pr.ExpectedBaseOID,
		})
		return string(receipt), nil
	})
	if err != nil {
		switch {
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, errPRCreateDuplicate):
			return refuse(nativeoperation_model.ReasonDuplicatePullRequest)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			// A lost primary commit rereads the ordering truth: a
			// concurrent cancellation wins prevention, anything else
			// fails closed with the owner held.
			current, lookupErr := nativeoperation_model.LookupOperation(releaseCtx, op.InstallationID, op.OperationID)
			if lookupErr != nil {
				return sdk.OperationRecord{}, terminalResult{}, lookupErr
			}
			if current != nil && current.Revoked {
				return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
			}
			return sdk.OperationRecord{}, terminalResult{}, err
		default:
			return refuse(nativeoperation_model.ReasonNativeRefused)
		}
	}
	op = committed

	// Test-only crash barrier for offline-recovery proof: with the
	// primary records committed and the owner still held, the driver
	// fails here to simulate a crash before bounded completion.
	if err := TestCrashBarrier(CrashPointPRCreateAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}

	// Before derived Git writes, persist their exact internal-ref tuple
	// and allowed completion phase under this owner. A failure here
	// leaves the primary committed with completion unattempted, which
	// finalizes as needs_intervention rather than guessing.
	scope.CompletionRef = pull.GetGitRefName()
	scope.CompletionOldOID = prCreateOldTip(s.prCreateRefTip(owned, repository.RepoPath(), scope.CompletionRef))
	scope.CompletionNewOID = pr.ExpectedHeadOID
	scope.CompletionPhase = PRCreateCompletionPhase
	if encoded, err := encodeScope(scope); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	} else if err := nativeoperation_model.UpdateScopeWhere(releaseCtx, owner, encoded); err != nil {
		return s.finalizePRCreate(releaseCtx, op, execution, nativeoperation_model.CompletionNeedsIntervention)
	}

	// Bounded completion under the held owner: the exact internal PR ref,
	// push-history comment, code-owner requests, mentions and notifications
	// share the ordinary completion. The git children carry this
	// execution's proof; hooks admit exactly the persisted tuple as
	// completion, never as another primary write. A PR can be visible
	// while its derived state is incomplete: completion failure reports
	// without rolling the durable primary back.
	notifiers, err := pull_service.CompletePullRequestCreation(owned, repository, pull)
	if err != nil {
		if !s.prCreateCompletionKnown(owned, repository.RepoPath(), scope) {
			// The internal ref is unexpected or unreadable: fail
			// closed with the owner held so offline recovery takes
			// the authoritative second look under quiescence. The
			// committed primary stands; only completion is unknown.
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return s.finalizePRCreate(releaseCtx, op, execution, nativeoperation_model.CompletionNeedsIntervention)
	}
	issue_service.ReviewRequestNotify(owned, issue, issue.Poster, notifiers)
	mentions, err := issues_model.FindAndUpdateIssueMentions(owned, issue, issue.Poster, issue.Content)
	if err != nil {
		return s.finalizePRCreate(releaseCtx, op, execution, nativeoperation_model.CompletionNeedsIntervention)
	}
	notify_service.NewPullRequest(owned, pull, mentions)
	return s.finalizePRCreate(releaseCtx, op, execution, nativeoperation_model.CompletionComplete)
}

// finalizePRCreate records the completion state of one committed creation
// and releases its exact owner. A finalize failure keeps the owner held for
// offline recovery; it never downgrades the committed primary.
func (s *Service) finalizePRCreate(ctx context.Context, op *nativeoperation_model.Operation, execution *execcontext.Execution, completion string) (sdk.OperationRecord, terminalResult, error) {
	recorded, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(recorded), terminalResult{}, nil
}

// prCreateRefTip reads one internal-ref tip for completion binding. A
// missing ref reports absent; an unreadable ref reports not-OK and the
// caller treats the state as uncertain.
func (s *Service) prCreateRefTip(ctx context.Context, repoPath, ref string) (tip string, absent, ok bool) {
	value, err := s.readRef(ctx, repoPath, ref)
	if err != nil {
		if git.IsErrNotExist(err) {
			return "", true, true
		}
		return "", false, false
	}
	if value == "" {
		return "", true, true
	}
	return strings.ToLower(value), false, true
}

// prCreateOldTip renders a bound old tip for the persisted completion
// tuple. An unreadable ref binds no tuple: the empty result fails the
// exact-tuple hook check and completion reports needs_intervention rather
// than writing against an uncertain base.
func prCreateOldTip(tip string, absent, ok bool) string {
	if !ok || absent {
		return ""
	}
	return tip
}

// prCreateCompletionKnown reports whether the derived ref holds a known
// state after a completion failure: still at its pre-completion tip (or
// still absent), or exactly at the realized tuple. Any other value, or an
// unreadable ref, is uncertain and retains the fence.
func (s *Service) prCreateCompletionKnown(ctx context.Context, repoPath string, scope Scope) bool {
	tip, absent, ok := s.prCreateRefTip(ctx, repoPath, scope.CompletionRef)
	if !ok {
		return false
	}
	if absent {
		return scope.CompletionOldOID == ""
	}
	if scope.CompletionOldOID != "" && tip == strings.ToLower(scope.CompletionOldOID) {
		return true
	}
	return tip == strings.ToLower(scope.CompletionNewOID)
}

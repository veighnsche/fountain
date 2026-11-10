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
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	api "forgejo.org/modules/structs"
	pull_service "forgejo.org/services/pull"
)

// ReviewSubmitComment carries one keyed inline finding anchored to the
// candidate diff. The key is opaque client correlation; the host stores
// key plus prose in the native comment and reports the native id per key
// in the receipt.
type ReviewSubmitComment struct {
	Key  string `json:"key"`
	Path string `json:"path"`
	Side string `json:"side"`
	Line int64  `json:"line"`
	Body string `json:"body"`
}

// ReviewSubmitIntent carries the validated pull_request.review.submit
// payload: one final review bound to its exact candidate, with an
// optional summary body and zero or more inline comments. The first
// interface covers same-repository PRs with commit_id equal to the
// expected head and a final event; attachments, pending/draft creation,
// submission of an existing draft, editing and dismissal are outside
// this operation.
type ReviewSubmitIntent struct {
	PullRequestNumber int64                 `json:"pull_request_number"`
	PRAuthorID        int64                 `json:"pr_author_id"`
	HeadRepositoryID  int64                 `json:"head_repository_id"`
	HeadRef           string                `json:"head_ref"`
	BaseRef           string                `json:"base_ref"`
	ExpectedHeadOID   string                `json:"expected_head_oid"`
	ExpectedBaseOID   string                `json:"expected_base_oid"`
	CommitID          string                `json:"commit_id"`
	Event             string                `json:"event"`
	Body              string                `json:"body"`
	Comments          []ReviewSubmitComment `json:"comments,omitempty"`
}

// parseReviewSubmitPayload validates the complete immutable review-submit
// intent. Unknown fields, conflicting values and out-of-scope requests fail
// validation rather than silently broadening the request.
func parseReviewSubmitPayload(payload []byte, repositoryID int64) (*ReviewSubmitIntent, error) {
	if len(payload) == 0 {
		return nil, ErrInvalidIntent
	}
	// The complete encoded intent, inline comments included, stays
	// within the native-review payload ceiling.
	if len(payload) > sdk.MaxReviewPayloadBytes {
		return nil, ErrInvalidIntent
	}
	var review ReviewSubmitIntent
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&review); err != nil {
		return nil, ErrInvalidIntent
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidIntent
	}
	if review.PullRequestNumber <= 0 || review.PRAuthorID <= 0 {
		return nil, ErrInvalidIntent
	}
	// The first interface submits reviews on same-repository PRs only.
	if review.HeadRepositoryID != repositoryID {
		return nil, ErrInvalidIntent
	}
	if !validBranchRef(review.HeadRef) || !validBranchRef(review.BaseRef) || review.HeadRef == review.BaseRef {
		return nil, ErrInvalidIntent
	}
	if !validOID(review.ExpectedHeadOID) || !validOID(review.ExpectedBaseOID) || !validOID(review.CommitID) {
		return nil, ErrInvalidIntent
	}
	// No omitted-commit default: the bound commit is always explicit and
	// must equal the expected head.
	if !strings.EqualFold(review.CommitID, review.ExpectedHeadOID) {
		return nil, ErrInvalidIntent
	}
	switch review.Event {
	case string(api.ReviewStateApproved):
	case string(api.ReviewStateComment):
		if strings.TrimSpace(review.Body) == "" {
			return nil, ErrInvalidIntent
		}
	case string(api.ReviewStateRequestChanges):
		if strings.TrimSpace(review.Body) == "" {
			return nil, ErrInvalidIntent
		}
	default:
		return nil, ErrInvalidIntent
	}
	// The canonical structural comment rules live in the model so the
	// host and direct model callers refuse identically; here any breach
	// is a malformed intent.
	modelComments := make([]issues_model.ExactReviewComment, 0, len(review.Comments))
	for _, comment := range review.Comments {
		modelComments = append(modelComments, issues_model.ExactReviewComment{
			Key:  comment.Key,
			Path: comment.Path,
			Side: comment.Side,
			Line: comment.Line,
			Body: comment.Body,
		})
	}
	if err := issues_model.ValidateExactReviewComments(modelComments); err != nil {
		return nil, ErrInvalidIntent
	}
	review.ExpectedHeadOID = strings.ToLower(review.ExpectedHeadOID)
	review.ExpectedBaseOID = strings.ToLower(review.ExpectedBaseOID)
	review.CommitID = strings.ToLower(review.CommitID)
	return &review, nil
}

// reviewSubmitType maps the validated final event to its native review type.
func reviewSubmitType(event string) issues_model.ReviewType {
	if event == string(api.ReviewStateComment) {
		return issues_model.ReviewTypeComment
	}
	if event == string(api.ReviewStateRequestChanges) {
		return issues_model.ReviewTypeReject
	}
	return issues_model.ReviewTypeApprove
}

// ReviewSubmitReceipt is the canonical SDK wire shape for one
// committed review submission: the backend emits exactly
// sdk.ReviewSubmitReceipt, never a divergent duplicate.
type ReviewSubmitReceipt = sdk.ReviewSubmitReceipt

// mapExactReviewRefusal maps a deterministic exact-review refusal to its
// bounded operation reason. Changed candidates refuse as stale; identity,
// source, target and lifecycle mismatches refuse as PR mismatches; denied
// native review eligibility refuses as authority lost; a conflicting pending
// draft refuses as pending. Anything unrecognized fails closed as a native
// refusal.
func mapExactReviewRefusal(err error) string {
	var refused issues_model.ErrExactReviewRefused
	if !errors.As(err, &refused) {
		if issues_model.IsContentEmptyErr(err) {
			return nativeoperation_model.ReasonNativeRefused
		}
		return nativeoperation_model.ReasonNativeRefused
	}
	switch refused.Reason {
	case "head changed":
		return nativeoperation_model.ReasonStaleHead
	case "base changed":
		return nativeoperation_model.ReasonStaleBaseOrResult
	case "pending draft exists":
		return nativeoperation_model.ReasonPendingReviewExists
	case "cannot review your own pull request",
		"reviewer is blocked",
		"reviewer cannot read this pull request":
		return nativeoperation_model.ReasonAuthorityLost
	case "invalid object id",
		"commit must equal the expected head",
		"unsupported final review type",
		"invalid inline comment",
		"invalid inline anchor",
		"binary-only inline anchor",
		"duplicate inline anchor",
		"duplicate inline comment key",
		"too many inline comments",
		"merge base unavailable":
		// Unreachable through the guarded path: intent parsing or the
		// diff-membership proof already rejects these. Fail closed as
		// a native refusal rather than a PR mismatch.
		return nativeoperation_model.ReasonNativeRefused
	default:
		return nativeoperation_model.ReasonPRMismatch
	}
}

// submitReviewSubmit claims the operation ID and reservation, verifies the
// exact refs and native authority under the held owner, commits the primary
// review records with the operation receipt in one SQL transaction, and
// completes mentions and notifications under bounded completion ownership.
// There is no unguarded fallback: every refusal path records not_committed
// or retains the fence.
func (s *Service) submitReviewSubmit(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent) (sdk.OperationRecord, error) {
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if intent.ReviewSubmit == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := Scope{
		Kind:         nativeoperation_model.OwnerConditional,
		RepositoryID: intent.RepositoryID,
		Ref:          intent.ReviewSubmit.BaseRef,
		OldOID:       intent.ReviewSubmit.ExpectedBaseOID,
		HeadRef:      intent.ReviewSubmit.HeadRef,
		HeadOID:      intent.ReviewSubmit.ExpectedHeadOID,
		PRNumber:     intent.ReviewSubmit.PullRequestNumber,
	}
	return s.claimConditionalSubmit(ctx, decision, installationID, intent, scope, s.executeReviewSubmit)
}

// claimConditionalSubmit runs the shared conditional-submit prologue for
// review-submit and thread-resolve: capability file, conditional owner,
// scope claim with busy/inhibited/stale/lookup/reconcile handling, then
// the caller-selected bounded terminal executor with retain-on-infraerror
// retirement. The kind-specific Scope is built by the caller and the
// executor is fixed per caller; there is no kind fallback or dispatch.
func (s *Service) claimConditionalSubmit(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent, scope Scope, execute func(context.Context, *nativeoperation_model.Operation, *ValidIntent, *execcontext.Execution) (sdk.OperationRecord, terminalResult, error)) (sdk.OperationRecord, error) {
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
	record, terminal, err := execute(ctx, op, intent, execution)
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

// executeReviewSubmit runs the guarded submission and reconciles its result.
// It returns the SDK record and whether the owner stays held for an
// unresolved effect. All native work runs on the owned context so nested
// participating writers attribute to this execution; terminal records and
// the owner release run detached so a caller disconnect cannot stick the
// reservation.
func (s *Service) executeReviewSubmit(ctx context.Context, op *nativeoperation_model.Operation, intent *ValidIntent, execution *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
	owner := execution.Owner
	submit := intent.ReviewSubmit
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
	// The exact tips are read under the held reservation; the atomic
	// primary re-verifies them without touching Git, and refuses on any
	// change with no diff-equivalence substitution.
	head, err := s.readRef(owned, repository.RepoPath(), submit.HeadRef)
	if err != nil || !strings.EqualFold(head, submit.ExpectedHeadOID) {
		return refuse(nativeoperation_model.ReasonStaleHead)
	}
	base, err := s.readRef(owned, repository.RepoPath(), submit.BaseRef)
	if err != nil || !strings.EqualFold(base, submit.ExpectedBaseOID) {
		return refuse(nativeoperation_model.ReasonStaleBaseOrResult)
	}
	doer, err := user_model.GetUserByID(owned, op.ActorID)
	if err != nil {
		return refuse(nativeoperation_model.ReasonAuthorityLost)
	}
	pr, err := issues_model.GetPullRequestByIndex(owned, op.RepositoryID, submit.PullRequestNumber)
	if err != nil {
		if issues_model.IsErrPullRequestNotExist(err) {
			return refuse(nativeoperation_model.ReasonPRMismatch)
		}
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if err := pr.LoadIssue(owned); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	issue := pr.Issue
	issue.PullRequest = pr
	target := issues_model.ExactReviewTarget{
		PRIndex:        submit.PullRequestNumber,
		AuthorID:       submit.PRAuthorID,
		HeadRepoID:     submit.HeadRepositoryID,
		HeadBranch:     strings.TrimPrefix(submit.HeadRef, git.BranchPrefix),
		BaseBranch:     strings.TrimPrefix(submit.BaseRef, git.BranchPrefix),
		HeadOID:        submit.ExpectedHeadOID,
		BaseOID:        submit.ExpectedBaseOID,
		CommitID:       submit.CommitID,
		CurrentHeadOID: strings.ToLower(head),
		CurrentBaseOID: strings.ToLower(base),
	}
	reviewType := reviewSubmitType(submit.Event)

	// Test-only barrier for the transaction/cancel race proof: with the
	// claim held, the driver revokes the operation here so the atomic
	// primary commit below must observe the cancellation and refuse.
	if err := TestCrashBarrier(CrashPointReviewSubmitBeforePrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}

	// Diff-membership proof for inline comments, before any write: the
	// host derives the merge base from the verified expected head/base
	// and proves every anchor against the live candidate diff. Added,
	// deleted, context and rename-side lines anchor; binary-only files,
	// paths without hunks and lines outside the claimed side coverage
	// refuse with no partial rows. Comment-free submissions prove no
	// anchors but still derive and record the merge base, so the receipt
	// certifies the exact admitted tuple instead of an empty base the
	// consumer must refuse.
	mergeBase, err := deriveReviewMergeBase(owned, repository.RepoPath(), submit.ExpectedBaseOID, submit.ExpectedHeadOID)
	if err != nil {
		return refuse(mapExactReviewRefusal(err))
	}
	target.DiffBaseOID = mergeBase
	var modelComments []issues_model.ExactReviewComment
	if len(submit.Comments) > 0 {
		anchors := make([]inlineAnchor, 0, len(submit.Comments))
		paths := make([]string, 0, len(submit.Comments))
		seen := make(map[string]struct{}, len(submit.Comments))
		for _, comment := range submit.Comments {
			anchors = append(anchors, inlineAnchor{Key: comment.Key, Path: comment.Path, Side: comment.Side, Line: comment.Line})
			if _, dup := seen[comment.Path]; !dup {
				seen[comment.Path] = struct{}{}
				paths = append(paths, comment.Path)
			}
		}
		diffs := make(map[string][]byte, len(paths))
		for _, path := range paths {
			diff, err := fetchReviewFileDiff(owned, repository.RepoPath(), mergeBase, submit.ExpectedHeadOID, path)
			if err != nil {
				return refuse(nativeoperation_model.ReasonNativeRefused)
			}
			diffs[path] = diff
		}
		if err := proveInlineAnchors(diffs, anchors); err != nil {
			return refuse(mapExactReviewRefusal(err))
		}
		modelComments = make([]issues_model.ExactReviewComment, 0, len(submit.Comments))
		for i, comment := range submit.Comments {
			modelComments = append(modelComments, issues_model.ExactReviewComment{
				Key:   comment.Key,
				Path:  comment.Path,
				Side:  comment.Side,
				Line:  comment.Line,
				Body:  comment.Body,
				Patch: cutInlineExcerpt(diffs[comment.Path], anchors[i]),
			})
		}
	}

	// Primary effect: the native review/comment rows with official-state
	// and request changes, plus the operation's committed receipt, in one
	// SQL transaction ordered against cancellation and expiry on the same
	// operation row. No SQL transaction spans Git or wraps the service.
	// A pending draft for this actor refuses without adoption or deletion.
	// The summary review and every inline comment commit atomically; a
	// stale, cancelled or anchor-refused submit writes no partial rows,
	// and replay returns the recorded native ids unchanged.
	var review *issues_model.Review
	var comm *issues_model.Comment
	committed, err := nativeoperation_model.CommitReviewSubmitPrimary(owned, op.InstallationID, op.OperationID, owner, s.clock(), func(tx context.Context) (string, error) {
		if err := s.revalidateSubmission(tx, op); err != nil {
			return "", err
		}
		submitted, comment, inline, err := issues_model.SubmitExactReviewInline(tx, doer, issue, reviewType, submit.Body, target, modelComments)
		if err != nil {
			return "", err
		}
		review, comm = submitted, comment
		refs := make([]sdk.ReviewCommentReceipt, 0, len(inline))
		for i, code := range inline {
			refs = append(refs, sdk.ReviewCommentReceipt{Key: submit.Comments[i].Key, CommentID: code.ID})
		}
		receipt, _ := json.Marshal(ReviewSubmitReceipt{
			ReviewID:     submitted.ID,
			CommentID:    comment.ID,
			ReviewerID:   doer.ID,
			PRID:         pr.ID,
			IssueID:      issue.ID,
			PRNumber:     submit.PullRequestNumber,
			CommitID:     submit.CommitID,
			Event:        submit.Event,
			HeadOID:      submit.ExpectedHeadOID,
			BaseOID:      submit.ExpectedBaseOID,
			Comments:     refs,
			MergeBaseOID: mergeBase,
		})
		return string(receipt), nil
	})
	if err != nil {
		switch {
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
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
			return refuse(mapExactReviewRefusal(err))
		}
	}
	op = committed

	// Test-only crash barrier for offline-recovery proof: with the
	// primary records committed and the owner still held, the driver
	// fails here to simulate a crash before bounded completion.
	if err := TestCrashBarrier(CrashPointReviewSubmitAfterPrimary); err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}

	// Bounded completion under the held owner: mentions and notifications
	// share the ordinary completion. A review can be durable while its
	// completion is incomplete: completion failure reports
	// needs_intervention without rolling the committed primary back.
	// There is no derived Git write, so no completion tuple persists.
	if err := pull_service.CompleteReviewSubmission(owned, doer, issue, review, comm); err != nil {
		return s.finalizeReviewSubmit(releaseCtx, op, execution, nativeoperation_model.CompletionNeedsIntervention)
	}
	return s.finalizeReviewSubmit(releaseCtx, op, execution, nativeoperation_model.CompletionComplete)
}

// finalizeReviewSubmit records the completion state of one committed
// submission and releases its exact owner. A finalize failure keeps the
// owner held for offline recovery; it never downgrades the committed
// primary.
func (s *Service) finalizeReviewSubmit(ctx context.Context, op *nativeoperation_model.Operation, execution *execcontext.Execution, completion string) (sdk.OperationRecord, terminalResult, error) {
	recorded, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, execution.Owner, execution.Generation, completion)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	return ToRecord(recorded), terminalResult{}, nil
}

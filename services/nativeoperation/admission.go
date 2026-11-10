// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/desktopgrant"
	"forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"
)

// ErrAuthorityLost reports that the bound credential generation, binding or
// native authority no longer validates against authoritative state.
var ErrAuthorityLost = errors.New("authority_lost")

// RefLine is one reference-transaction input line.
type RefLine = execcontext.RefTransactionLine

// Reference-transaction hook phases.
const (
	PhasePrepared  = "prepared"
	PhaseCommitted = "committed"
	PhaseAborted   = "aborted"
)

// TransactionRequest classifies one reference-transaction callback from a
// generated native hook.
type TransactionRequest struct {
	OwnerName string
	RepoName  string
	Origin    execcontext.GitExecutionOrigin
	Phase     string
	Lines     []RefLine
	Proof     string
}

// TransactionDecision is the hook verdict. Refusals carry a bounded reason
// safe for hook output; secrets never appear.
type TransactionDecision struct {
	Allowed bool
	Reason  string
}

// GateState reports whether the reservation is idle, for hook callbacks that
// carry no repository environment. Held means every participating writer is
// fenced until the owner resolves.
func (s *Service) GateState(ctx context.Context) (bool, error) {
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return false, err
	}
	return reservation.Owner == "", nil
}

// ClassifyTransaction enforces the reservation at Git's
// reference-transaction checkpoint. An idle reservation allows the ordinary
// unintegrated path. A held reservation requires the owner's execution proof
// and effects within its recorded scope; a conditional merge additionally
// passes prepared admission. Unknown infrastructure failures return an error
// and the hook fails closed.
func (s *Service) ClassifyTransaction(ctx context.Context, req TransactionRequest) (TransactionDecision, error) {
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return TransactionDecision{}, err
	}
	if reservation.Owner == "" {
		return TransactionDecision{Allowed: true}, nil
	}
	if !execcontext.VerifyProof(reservation.Verifier, req.Proof) {
		return TransactionDecision{Reason: "native operation in progress"}, nil
	}
	scope, err := decodeScope(reservation.Scope)
	if err != nil {
		return TransactionDecision{}, err
	}
	ownerKind, installationID, operationID := splitOwner(reservation.Owner)
	if scope.NativeRename != nil {
		plan := scope.NativeRename
		repoName := plan.RepositoryName
		if plan.Target == execcontext.TargetWiki {
			repoName += ".wiki"
		}
		if ownerKind != nativeoperation_model.OwnerOrdinary || req.Origin != execcontext.GitOriginRefRename ||
			plan.Validate() != nil || plan.RepositoryID != scope.RepositoryID || req.OwnerName != plan.OwnerName || req.RepoName != repoName ||
			!plan.PermitsTransaction(req.Phase, req.Lines) {
			return TransactionDecision{Reason: "operation scope mismatch"}, nil
		}
		return TransactionDecision{Allowed: true}, nil
	}
	if req.Origin == execcontext.GitOriginRefRename {
		return TransactionDecision{Reason: "native rename launch is not recorded"}, nil
	}

	wiki := strings.HasSuffix(req.RepoName, ".wiki")
	if !transactionTargetMatches(reservation.Owner, ownerKind, scope, wiki) {
		return TransactionDecision{Reason: "operation scope mismatch"}, nil
	}
	repository, err := repo_model.GetRepositoryByOwnerAndName(ctx, req.OwnerName, strings.TrimSuffix(req.RepoName, ".wiki"))
	if err != nil {
		return TransactionDecision{}, err
	}
	if ownerKind == nativeoperation_model.OwnerOrdinary {
		family, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
		if ok && family == FamilyCollaboration && resource == execcontext.CollabBatchResource("pr-sweep") && scope.Family == FamilyCollaboration {
			allowed, err := sweepPermitsPRRefs(ctx, scope, repository.ID, req.Lines)
			if err != nil {
				return TransactionDecision{}, err
			}
			if !allowed {
				return TransactionDecision{Reason: "operation scope mismatch"}, nil
			}
			return TransactionDecision{Allowed: true}, nil
		}
		if repository.ID != scope.RepositoryID {
			return TransactionDecision{Reason: "operation scope mismatch"}, nil
		}
		for _, line := range req.Lines {
			target := execcontext.TargetCode
			if wiki {
				target = execcontext.TargetWiki
			}
			if !scopePermitsRepositoryRef(scope, target, line.Ref) {
				return TransactionDecision{Reason: "operation scope mismatch"}, nil
			}
		}
		return TransactionDecision{Allowed: true}, nil
	}
	if repository.ID != scope.RepositoryID {
		return TransactionDecision{Reason: "operation scope mismatch"}, nil
	}
	if ownerKind != nativeoperation_model.OwnerConditional {
		return TransactionDecision{Reason: "operation scope mismatch"}, nil
	}
	switch req.Phase {
	case PhaseCommitted, PhaseAborted:
		return TransactionDecision{Allowed: true}, nil
	case PhasePrepared:
		return s.admitPrepared(ctx, reservation, scope, repository, installationID, operationID, req.Lines)
	default:
		return TransactionDecision{Reason: "unknown transaction phase"}, nil
	}
}

// A PR sweep owns the pushed head repository, while a fork's derived head
// ref lives in its base repository. Resolve that exact private ref through
// the authoritative PR row; this grants no branch, tag, merge or wiki write.
// Batch recovery remains fenced for intervention, including these effects.
func sweepPermitsPRRefs(ctx context.Context, scope Scope, baseRepoID int64, lines []RefLine) (bool, error) {
	if scope.RepositoryID <= 0 || !strings.HasPrefix(scope.HeadRef, git.BranchPrefix) || scope.HeadRef == git.BranchPrefix || len(lines) == 0 {
		return false, nil
	}
	for _, line := range lines {
		indexText, ok := strings.CutPrefix(line.Ref, "refs/pull/")
		if !ok || isZeroOID(line.New) {
			return false, nil
		}
		indexText, ok = strings.CutSuffix(indexText, "/head")
		if !ok {
			return false, nil
		}
		index, err := strconv.ParseInt(indexText, 10, 64)
		if err != nil || index <= 0 || strconv.FormatInt(index, 10) != indexText {
			return false, nil
		}
		pr, err := issues_model.GetPullRequestByIndex(ctx, baseRepoID, index)
		if err != nil {
			if issues_model.IsErrPullRequestNotExist(err) {
				return false, nil
			}
			return false, err
		}
		if pr.HasMerged || pr.HeadRepoID != scope.RepositoryID || git.BranchPrefix+pr.HeadBranch != scope.HeadRef {
			return false, nil
		}
	}
	return true, nil
}

// Main and wiki repositories share a native repository ID but have separate
// refs. A main-repository proof must never authorize a wiki transaction.
func transactionTargetMatches(owner, kind string, scope Scope, wiki bool) bool {
	if kind != nativeoperation_model.OwnerOrdinary {
		return !wiki
	}
	family, resource, ok := execcontext.SplitOrdinaryResource(owner)
	if !ok {
		return !wiki
	}
	target := execcontext.TargetCode
	var err error
	switch family {
	case FamilyRefWrite:
		target, err = execcontext.RefWriteRepositoryTarget(scope.RepositoryTarget, resource)
	case FamilyReceiveHTTP, FamilyReceiveSSH:
		target, _, err = execcontext.ReceiveRepositoryTarget(scope.RepositoryTarget, resource)
	case FamilyMirrorSync:
		return scope.Family == FamilyMirrorSync
	default:
		target, err = execcontext.ResolveRepositoryTarget(scope.RepositoryTarget, execcontext.TargetCode)
	}
	return err == nil && wiki == (target == execcontext.TargetWiki)
}

func splitOwner(owner string) (kind, installationID, operationID string) {
	rest, ok := strings.CutPrefix(owner, "cond:")
	if !ok {
		if strings.HasPrefix(owner, "ord:") {
			return nativeoperation_model.OwnerOrdinary, "", ""
		}
		return "", "", ""
	}
	var cut bool
	installationID, operationID, cut = strings.Cut(rest, "/")
	if !cut || installationID == "" || operationID == "" {
		return "", "", ""
	}
	return nativeoperation_model.OwnerConditional, installationID, operationID
}

func (s *Service) admitPrepared(ctx context.Context, reservation *nativeoperation_model.Reservation, scope Scope, repository *repo_model.Repository, installationID, operationID string, lines []RefLine) (TransactionDecision, error) {
	op, err := nativeoperation_model.LookupOperation(ctx, installationID, operationID)
	if err != nil {
		return TransactionDecision{}, err
	}
	if op != nil && op.Kind == nativeoperation_model.KindHumanPullCreate {
		return s.admitHumanPullCreateRef(ctx, reservation, scope, op, lines)
	}
	if op != nil && op.Kind == nativeoperation_model.KindPRCreate {
		// PR creation commits its primary effect in SQL, so its only
		// Git write is bounded completion after the commit: the single
		// internal-ref write must match the persisted tuple exactly.
		// This admits no primary effect and records no admission.
		return s.checkPRCreateCompletion(scope, op, lines), nil
	}
	if op == nil || !op.Submitted || op.IsTerminal() || op.EffectState != nativeoperation_model.EffectPending {
		return TransactionDecision{Reason: "operation is not pending"}, nil
	}
	// The disclosed test barrier pauses here, before any check, so races
	// can be staged deterministically against the atomic decision below.
	if err := testAdmissionBarrier(); err != nil {
		return TransactionDecision{}, err
	}
	if op.Kind == nativeoperation_model.KindHumanMerge {
		return s.admitHumanMergePrepared(ctx, reservation, scope, op, lines)
	}
	var reason string
	var checkErr error
	switch op.Kind {
	case nativeoperation_model.KindMerge:
		reason, checkErr = s.checkMergePrepared(ctx, scope, repository, lines)
	case nativeoperation_model.KindRefPublish:
		reason, checkErr = s.checkPublishPrepared(ctx, scope, repository, lines)
	default:
		return TransactionDecision{Reason: "unsupported conditional kind"}, nil
	}
	if checkErr != nil {
		return TransactionDecision{}, checkErr
	}
	var recorded *nativeoperation_model.Operation
	var admitted bool
	err = db.WithTx(ctx, func(ctx context.Context) error {
		if reason == "" {
			if err := s.revalidateSubmission(ctx, op); err != nil {
				if errors.Is(err, ErrAuthorityLost) {
					reason = nativeoperation_model.ReasonAuthorityLost
				} else {
					return err
				}
			}
		}
		if reason == "" && s.clock() >= op.NotAfter {
			reason = nativeoperation_model.ReasonExpiredBeforeAdmission
		}
		var err error
		recorded, admitted, err = nativeoperation_model.RecordAdmissionAttempt(ctx, installationID, operationID, reservation.Owner, reason == "", reason)
		return err
	})
	if err != nil {
		if errors.Is(err, nativeoperation_model.ErrWrongOwner) {
			return TransactionDecision{Reason: nativeoperation_model.ReasonWrongOwner}, nil
		}
		if errors.Is(err, nativeoperation_model.ErrAdmissionLost) {
			return TransactionDecision{Reason: "operation is not pending"}, nil
		}
		return TransactionDecision{}, err
	}
	if !admitted {
		return TransactionDecision{Reason: lostAdmissionReason(recorded)}, nil
	}
	return TransactionDecision{Allowed: true}, nil
}

// checkMergePrepared enforces the exact merge tuple, the live source head
// and the PR binding at prepared time. It returns the refusal reason, or
// empty when the request passes this kind's checks.
func (s *Service) checkMergePrepared(ctx context.Context, scope Scope, repository *repo_model.Repository, lines []RefLine) (string, error) {
	switch {
	case len(lines) != 1:
		return nativeoperation_model.ReasonUnexpectedRefEffects, nil
	case lines[0].Ref != scope.Ref ||
		!strings.EqualFold(lines[0].Old, scope.OldOID) ||
		!strings.EqualFold(lines[0].New, scope.NewOID):
		return nativeoperation_model.ReasonStaleBaseOrResult, nil
	}
	head, err := s.readRef(ctx, repository.RepoPath(), scope.HeadRef)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(head, scope.HeadOID) {
		return nativeoperation_model.ReasonStaleHead, nil
	}
	// Retargeting the PR after preparation invalidates the request
	// even if an object ID happens to match.
	pr, err := issues_model.GetPullRequestByIndex(ctx, scope.RepositoryID, scope.PRNumber)
	if err != nil {
		return "", err
	}
	if pr.HasMerged || pr.HeadRepoID != scope.RepositoryID ||
		pr.HeadBranch != strings.TrimPrefix(scope.HeadRef, git.BranchPrefix) ||
		pr.BaseBranch != strings.TrimPrefix(scope.Ref, git.BranchPrefix) ||
		pr.BaseRepoID != scope.RepositoryID {
		return nativeoperation_model.ReasonPRMismatch, nil
	}
	return "", nil
}

// checkPublishPrepared enforces the exact publish tuple, the unchanged
// comparison base and the correction PR binding at prepared time. Scope
// HeadRef/HeadOID carry the comparison ref and tip; PRNumber and
// CorrectionAuthorID carry the correction PR when the intent binds one. It
// returns the refusal reason, or empty when the request passes.
func (s *Service) checkPublishPrepared(ctx context.Context, scope Scope, repository *repo_model.Repository, lines []RefLine) (string, error) {
	switch {
	case len(lines) != 1:
		return nativeoperation_model.ReasonUnexpectedRefEffects, nil
	case lines[0].Ref != scope.Ref ||
		!strings.EqualFold(lines[0].Old, scope.OldOID) ||
		!strings.EqualFold(lines[0].New, scope.NewOID):
		return nativeoperation_model.ReasonStaleBaseOrResult, nil
	}
	comparison, err := s.readRef(ctx, repository.RepoPath(), scope.HeadRef)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(comparison, scope.HeadOID) {
		return nativeoperation_model.ReasonStaleBaseOrResult, nil
	}
	if scope.PRNumber > 0 {
		pr, err := issues_model.GetPullRequestByIndex(ctx, scope.RepositoryID, scope.PRNumber)
		if err != nil {
			if issues_model.IsErrPullRequestNotExist(err) {
				return nativeoperation_model.ReasonPRMismatch, nil
			}
			return "", err
		}
		if err := pr.LoadIssue(ctx); err != nil {
			return "", err
		}
		if pr.HasMerged || pr.Issue.IsClosed ||
			pr.HeadRepoID != scope.RepositoryID || pr.BaseRepoID != scope.RepositoryID ||
			pr.HeadBranch != strings.TrimPrefix(scope.Ref, git.BranchPrefix) ||
			pr.BaseBranch != strings.TrimPrefix(scope.HeadRef, git.BranchPrefix) ||
			pr.Issue.PosterID != scope.CorrectionAuthorID {
			return nativeoperation_model.ReasonPRMismatch, nil
		}
		if scope.CorrectionPRID != 0 && (pr.ID != scope.CorrectionPRID || pr.IssueID != scope.IssueID) {
			return nativeoperation_model.ReasonPRMismatch, nil
		}
	}
	return "", nil
}

// checkPRCreateCompletion admits the single derived PR-ref write of a
// conditional PR creation as bounded completion. It requires the committed
// primary receipt plus the exact persisted internal-ref tuple and phase;
// anything else refuses, and an unexpected or uncertain internal ref keeps
// the fence. This records no admission: the primary commit already ordered
// cancellation, expiry and the receipt.
func (s *Service) checkPRCreateCompletion(scope Scope, op *nativeoperation_model.Operation, lines []RefLine) TransactionDecision {
	if !op.Submitted || op.EffectState != nativeoperation_model.EffectCommitted || op.Receipt == "" {
		return TransactionDecision{Reason: "operation is not pending"}
	}
	if scope.CompletionPhase != PRCreateCompletionPhase || scope.CompletionRef == "" || scope.CompletionNewOID == "" {
		return TransactionDecision{Reason: nativeoperation_model.ReasonUnexpectedRefEffects}
	}
	if len(lines) != 1 || lines[0].Ref != scope.CompletionRef {
		return TransactionDecision{Reason: nativeoperation_model.ReasonUnexpectedRefEffects}
	}
	wantOld := scope.CompletionOldOID
	if wantOld == "" {
		wantOld = git.Sha1ObjectFormat.EmptyObjectID().String()
	}
	if !strings.EqualFold(lines[0].Old, wantOld) || !strings.EqualFold(lines[0].New, scope.CompletionNewOID) {
		return TransactionDecision{Reason: nativeoperation_model.ReasonStaleBaseOrResult}
	}
	return TransactionDecision{Allowed: true}
}

func lostAdmissionReason(op *nativeoperation_model.Operation) string {
	if op == nil {
		return nativeoperation_model.ReasonWrongOwner
	}
	if op.Admitted {
		return nativeoperation_model.ReasonDuplicateAdmission
	}
	if op.Revoked {
		return nativeoperation_model.ReasonCancelledBeforeAdmission
	}
	if op.Reason != "" {
		return op.Reason
	}
	return nativeoperation_model.ReasonWrongOwner
}

// PublishPreReceiveGit carries the repository path and the hook's object
// environment for fast-forward verification against the quarantined pack
// objects. The environment comes from the hook options, which forward the
// hook child's object directories; without it the pushed candidate objects
// are invisible.
type PublishPreReceiveGit struct {
	RepoPath string
	Env      []string
}

// ClassifyPreReceive enforces the exact publish command set before any ref
// can commit. An idle reservation or an ordinary owner keeps existing
// behavior, as do conditional owners of other kinds. A held conditional
// publish owner for this repository requires exactly the authorized
// old/new tuple on the authorized ref with no push options, and for updates
// the new commit must fast-forward from the old tip; any other command set
// refuses and records the ordering reason for reconciliation. Recording
// failures never flip a refusal into an allowance. Fast-forward order is
// enforced here because only the hook environment sees the quarantined
// candidate objects; like prepared admission it relies on the installed
// native hooks.
func (s *Service) ClassifyPreReceive(ctx context.Context, repositoryID int64, lines []RefLine, hasPushOptions bool, hookGit PublishPreReceiveGit) (TransactionDecision, error) {
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return TransactionDecision{}, err
	}
	if reservation.Owner == "" {
		return TransactionDecision{Allowed: true}, nil
	}
	if reservation.OwnerKind != nativeoperation_model.OwnerConditional {
		return TransactionDecision{Allowed: true}, nil
	}
	_, installationID, operationID := splitOwner(reservation.Owner)
	op, err := nativeoperation_model.LookupOperation(ctx, installationID, operationID)
	if err != nil {
		return TransactionDecision{}, err
	}
	if op == nil || !op.Submitted || op.Kind != nativeoperation_model.KindRefPublish {
		return TransactionDecision{Allowed: true}, nil
	}
	scope, err := decodeScope(reservation.Scope)
	if err != nil {
		return TransactionDecision{}, err
	}
	if repositoryID != scope.RepositoryID {
		return TransactionDecision{Reason: "operation scope mismatch"}, nil
	}
	if op.IsTerminal() {
		return TransactionDecision{Reason: "operation is not pending"}, nil
	}
	reason := ""
	switch {
	case hasPushOptions:
		reason = nativeoperation_model.ReasonPublishOptionsRejected
	case len(lines) != 1:
		reason = nativeoperation_model.ReasonUnexpectedRefEffects
	case lines[0].Ref != scope.Ref ||
		!strings.EqualFold(lines[0].Old, scope.OldOID) ||
		!strings.EqualFold(lines[0].New, scope.NewOID):
		reason = nativeoperation_model.ReasonStaleBaseOrResult
	}
	if reason == "" && !isZeroOID(scope.OldOID) {
		force, err := publishForcePush(ctx, hookGit, scope.OldOID, scope.NewOID)
		if err != nil {
			return TransactionDecision{}, err
		}
		if force {
			reason = nativeoperation_model.ReasonNotFastForward
		}
	}
	if reason == "" {
		return TransactionDecision{Allowed: true}, nil
	}
	// Record the refusal ordering for reconciliation; the refusal stands
	// even when recording loses its race.
	_, _, _ = nativeoperation_model.RecordAdmissionAttempt(ctx, installationID, operationID, reservation.Owner, false, reason)
	return TransactionDecision{Reason: reason}, nil
}

// publishForcePush reports whether updating oldOID to newOID is a forced
// (non-fast-forward) update, resolving the candidate objects through the
// hook's quarantined object environment. A zero old OID marks a creation,
// which seeds from any commit and needs no ancestry.
func publishForcePush(ctx context.Context, hookGit PublishPreReceiveGit, oldOID, newOID string) (bool, error) {
	_, _, err := git.NewCommand(ctx, "merge-base", "--is-ancestor").AddDynamicArguments(oldOID, newOID).RunStdString(&git.RunOpts{Dir: hookGit.RepoPath, Env: hookGit.Env})
	if err == nil {
		return false, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		if exitError.ExitCode() == 1 && len(exitError.Stderr) == 0 {
			return true, nil
		}
	}
	return false, err
}

// ClassifyCompletion binds synchronous post-receive bookkeeping to the held
// owner. An idle reservation keeps existing behavior. A held reservation
// requires the owner's execution proof and effects within its scope, and
// permits validated push options only for the bound ordinary main receive. A held
// conditional owner additionally requires a submitted operation of a kind
// that permits push-originated completion.
func (s *Service) ClassifyCompletion(ctx context.Context, repositoryID int64, refNames []string, proof string, hasPushOptions bool) (TransactionDecision, error) {
	reservation, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return TransactionDecision{}, err
	}
	if reservation.Owner == "" {
		return TransactionDecision{Allowed: true}, nil
	}
	if !execcontext.VerifyProof(reservation.Verifier, proof) {
		return TransactionDecision{Reason: "native operation in progress"}, nil
	}
	scope, err := decodeScope(reservation.Scope)
	if err != nil {
		return TransactionDecision{}, err
	}
	if reservation.OwnerKind == nativeoperation_model.OwnerConditional {
		_, installationID, operationID := splitOwner(reservation.Owner)
		op, err := nativeoperation_model.LookupOperation(ctx, installationID, operationID)
		if err != nil {
			return TransactionDecision{}, err
		}
		if op == nil || !op.Submitted || (op.Kind != nativeoperation_model.KindRefPublish && op.Kind != nativeoperation_model.KindMerge) {
			return TransactionDecision{Reason: "operation kind mismatch"}, nil
		}
	}
	if repositoryID != scope.RepositoryID {
		return TransactionDecision{Reason: "operation scope mismatch"}, nil
	}
	for _, ref := range refNames {
		if !scopePermitsRef(scope, ref) {
			return TransactionDecision{Reason: "operation scope mismatch"}, nil
		}
	}
	if hasPushOptions {
		family, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
		if reservation.OwnerKind != nativeoperation_model.OwnerOrdinary || !ok ||
			(family != FamilyReceiveHTTP && family != FamilyReceiveSSH) || scope.Family != family ||
			resource != strconv.FormatInt(repositoryID, 10)+"/main" {
			return TransactionDecision{Reason: "push options are not permitted under operation ownership"}, nil
		}
		// The native pre-receive permission checks already validated these
		// options. Persist uncertainty before their setting effects: recovery
		// must never release this owner from Git ref evidence alone.
		document, err := execcontext.DecodeScopeDocument(reservation.Scope)
		if err != nil {
			return TransactionDecision{}, err
		}
		encoded, err := document.WithReceivePushOptions(true)
		if err != nil {
			return TransactionDecision{}, err
		}
		if err := nativeoperation_model.UpdateScopeExact(ctx, reservation.Owner, reservation.Generation, document.Original(), encoded); err != nil {
			return TransactionDecision{}, err
		}
	}
	return TransactionDecision{Allowed: true}, nil
}

func scopePermitsRepositoryRef(scope Scope, target execcontext.RepositoryTarget, ref string) bool {
	if scope.Family != FamilyMirrorSync {
		return scopePermitsRef(scope, ref)
	}
	encoded := execcontext.EncodeScopedRefTarget(target, ref)
	matched := false
	for _, tuple := range scope.Refs {
		actual, plain, err := execcontext.ScopedRefRepositoryTarget(tuple)
		if err != nil {
			return false
		}
		if actual == target && plain == ref && tuple.Ref == encoded {
			matched = true
		}
	}
	return matched
}

// scopePermitsRef reports whether an ordinary scope covers a proven ref:
// the single permitted ref, or any listed member of a multi-ref batch
// scope. Scopes without a ref list behave exactly as before.
func scopePermitsRef(scope Scope, ref string) bool {
	if ref == scope.Ref {
		return true
	}
	for _, listed := range scope.Refs {
		if ref == listed.Ref {
			return true
		}
	}
	return false
}

// revalidateSubmission reloads the bound credential generation, binding and
// current native authority from authoritative state without the submitted
// secret, which was discarded after verification. It mirrors the submission
// verifier's tail checks; any failure is ErrAuthorityLost.
func (s *Service) revalidateSubmission(ctx context.Context, op *nativeoperation_model.Operation) error {
	if op.ControllerGrantID != "" {
		if _, err := extensionauth.CheckControllerUse(ctx, extensionauth.ControllerUse{GrantID: op.ControllerGrantID, InstallationID: op.InstallationID,
			PackageID: op.PackageID, ActorID: op.ActorID, RepositoryID: op.RepositoryID, TokenID: op.TokenID, CredentialFingerprint: op.CredentialFingerprint, Kind: op.Kind, NotAfter: op.NotAfter}); err != nil {
			return ErrAuthorityLost
		}
	} else if op.TokenID > 0 {
		if setting.OAuth2.FactoryAppliancePackageID != "" && op.PackageID == setting.OAuth2.FactoryAppliancePackageID {
			return ErrAuthorityLost
		}
		if op.PackageID == "" {
			err := desktopgrant.WithPlacement(ctx, op.RepositoryID, func(_ context.Context, p *desktopgrant.Placement) error {
				if p.Generation > 0 {
					return ErrAuthorityLost
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
	}
	if op.ControllerGrantID != "" && op.TokenID == 0 {
		return nil
	}
	if op.TokenID == 0 && op.Kind == nativeoperation_model.KindIssueGraph {
		return s.revalidateIssueIntake(ctx, op)
	}
	lost := func() error { return ErrAuthorityLost }
	token := new(auth_model.AccessToken)
	has, err := db.GetEngine(ctx).ID(op.TokenID).NoAutoCondition().Get(token)
	if err != nil {
		return err
	}
	if !has {
		return lost()
	}
	if extensionauth.CredentialFingerprint(token.TokenHash, token.TokenSalt) != op.CredentialFingerprint {
		return lost()
	}
	if token.UID != op.ActorID {
		return lost()
	}
	binding, err := extensionauth.FindBinding(ctx, op.InstallationID, token.ID, op.ActorID, op.RepositoryID, op.Kind)
	if err != nil {
		return err
	}
	if binding == nil {
		return lost()
	}
	granted, err := token.Scope.HasScope(auth_model.AccessTokenScopeWriteRepository)
	if err != nil || !granted {
		return lost()
	}
	if !token.ResourceAllRepos {
		resources, err := auth_model.GetRepositoriesAccessibleWithToken(ctx, token.ID)
		if err != nil {
			return err
		}
		allowed := false
		for _, resource := range resources {
			if resource.RepoID == op.RepositoryID {
				allowed = true
				break
			}
		}
		if !allowed {
			return lost()
		}
	}
	user, err := user_model.GetUserByID(ctx, op.ActorID)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			return lost()
		}
		return err
	}
	if !user.IsActive || user.ProhibitLogin {
		return lost()
	}
	repository, err := repo_model.GetRepositoryByID(ctx, op.RepositoryID)
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return lost()
		}
		return err
	}
	publicOnly, err := token.Scope.PublicOnly()
	if err != nil || (publicOnly && repository.IsPrivate) {
		return lost()
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repository, user)
	if err != nil {
		return err
	}
	if !permission.CanWrite(unit.TypeCode) {
		return lost()
	}
	return nil
}

// testAdmissionBarrier pauses before the atomic admission decision when the
// disclosed test instrument NATIVEOP_TEST_ADMISSION_BARRIER names a
// directory: it writes admission.entered and waits for admission.release.
// Production never sets this variable; the bounded pause and the rename
// interposer are test instruments only.
func testAdmissionBarrier() error {
	dir := os.Getenv("NATIVEOP_TEST_ADMISSION_BARRIER")
	if dir == "" {
		return nil
	}
	if err := os.WriteFile(filepath.Join(dir, "admission.entered"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "admission.release")); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("admission barrier timeout")
}

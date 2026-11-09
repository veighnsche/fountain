// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"os"
	"strings"

	sdk "forgejo.org/extension-sdk"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/desktopgrant"
	extensionauth_model "forgejo.org/models/extensionauth"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	execcontext "forgejo.org/modules/nativeoperation"
)

// submitPublish durably registers a git.ref.publish intent in
// pending/awaiting_receive. Registration launches no Git and occupies no
// reservation; the publisher's bound receive claims execution later. The
// same ID and identical intent return the existing record, never a second
// registration. Different content under the same ID is intent_conflict.
func (s *Service) submitPublish(ctx context.Context, decision extensionauth_model.SubmissionDecision, installationID string, intent *ValidIntent) (sdk.OperationRecord, error) {
	if err := ctx.Err(); err != nil {
		return sdk.OperationRecord{}, err
	}
	if intent.Publish == nil {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	op := operationForSubmission(decision, installationID, intent)
	recorded, err := nativeoperation_model.InsertWaitingOperation(ctx, op)
	if err != nil {
		if errors.Is(err, nativeoperation_model.ErrDuplicateOperation) {
			return s.reconcileSubmission(recorded, decision, intent)
		}
		return sdk.OperationRecord{}, err
	}
	return ToRecord(recorded), nil
}

// PublishReceiveRequest carries the bound receive facts the route resolved:
// the host-derived installation from the installation admission, the
// operation ID from the binding, the PAT-authenticated route actor, the
// presented PAT secret and the route's resolved repository ID. The secret
// must never be logged or recorded.
type PublishReceiveRequest struct {
	InstallationID string
	OperationID    string
	ActorID        int64
	TokenSecret    string
	RepositoryID   int64
}

// PreparedPublishReceive is one claimed publish execution ready for its
// receiver launch. The reservation is held by the conditional owner and the
// capability file backs the execution proof for hook binding.
type PreparedPublishReceive struct {
	Operation  *nativeoperation_model.Operation
	Intent     *PublishIntent
	Target     *PublishTarget
	Scope      Scope
	Execution  *execcontext.Execution
	Repository *repo_model.Repository
}

// RetireCapability removes the execution capability file after the owner is
// released. Retained owners keep their file for offline recovery.
func (p *PreparedPublishReceive) RetireCapability() {
	if p != nil && p.Execution != nil && p.Execution.CapabilityPath != "" {
		_ = os.Remove(p.Execution.CapabilityPath)
	}
}

// PublishReceiveOutcome is what the receive route reports when no receiver
// launches. Record carries a recorded terminal refusal or the replayed
// earlier outcome; a nil Record means the operation row was untouched.
type PublishReceiveOutcome struct {
	Status int
	Code   string
	Record *sdk.OperationRecord
}

// Bounded receive-route refusal codes. They name the failed check without
// exposing secrets or native internals.
const (
	PublishReceiveNotObserved         = "not_observed"
	PublishReceiveCancelledBeforeSeen = "cancelled_before_submit"
	PublishReceiveReplayCommitted     = "already_committed"
	PublishReceiveReplayNotCommitted  = "already_not_committed"
	PublishReceiveKindMismatch        = "operation_kind_mismatch"
	PublishReceiveRepositoryMismatch  = "operation_repository_mismatch"
	PublishReceiveActorMismatch       = "operation_actor_mismatch"
	PublishReceiveCredentialMismatch  = "operation_credential_mismatch"
	PublishReceiveBusy                = "native_operation_busy"
	PublishReceiveInhibited           = "native_operation_inhibited"
)

// PreparePublishReceive verifies one bound receive against the waiting
// registration and atomically claims its execution: installation, kind,
// recorded actor and credential generation, route repository, fresh native
// authority, the exact live target and the atomic waiting-to-started claim
// with its revision, cancellation and deadline ordering. A claimed execution
// launches exactly one receiver; every other outcome refuses without one.
// Terminal refusals record not_committed; authentication and route mismatches
// leave the operation untouched; contention and infrastructure failures
// return retryable outcomes or errors with nothing recorded.
//
//nolint:nilnil // Prepared requests and reported outcomes are mutually exclusive.
func (s *Service) PreparePublishReceive(ctx context.Context, req PublishReceiveRequest) (*PreparedPublishReceive, *PublishReceiveOutcome, error) {
	return s.preparePublishReceive(ctx, req, func(ctx context.Context, op *nativeoperation_model.Operation) error {
		return s.verifyReceiveCredential(ctx, op, req.TokenSecret)
	})
}

// PrepareDesktopPublishReceive authenticates the desktop's original operation
// grant and native profile proof. It shares the conditional receive owner and
// journal with appliance publication, without accepting appliance admission.
func (s *Service) PrepareDesktopPublishReceive(ctx context.Context, grantID, operationID, proof string, actorID, repositoryID, oauthGrantID int64) (*PreparedPublishReceive, *PublishReceiveOutcome, error) {
	if !validHex(grantID, 16) || !validHex(proof, 32) {
		return nil, &PublishReceiveOutcome{Status: 403, Code: PublishReceiveCredentialMismatch}, nil
	}
	grant, err := desktopgrant.GetOperationGrant(ctx, grantID)
	digest := sha256.Sum256([]byte(proof))
	if err != nil || grant == nil || grant.CredentialKind != desktopgrant.CredentialOAuth || grant.OAuthGrantID != oauthGrantID || oauthGrantID <= 0 ||
		grant.ActorID != actorID || grant.RepositoryID != repositoryID || !validHex(proof, 32) ||
		subtle.ConstantTimeCompare([]byte(hex.EncodeToString(digest[:])), []byte(grant.ProofSHA256)) != 1 {
		return nil, &PublishReceiveOutcome{Status: 403, Code: PublishReceiveCredentialMismatch}, nil
	}
	req := PublishReceiveRequest{InstallationID: grant.InstallationID, OperationID: operationID, ActorID: actorID, RepositoryID: repositoryID}
	return s.preparePublishReceive(ctx, req, func(ctx context.Context, op *nativeoperation_model.Operation) error {
		if op.ControllerGrantID != grant.ID || op.TokenID != 0 || op.PackageID != "" || op.CredentialFingerprint != grant.Fingerprint() {
			return ErrAuthorityLost
		}
		// The shared owner rechecks current grant, placement, OAuth application,
		// permission, expiry and withdrawal again before its conditional claim.
		return s.revalidateSubmission(ctx, op)
	})
}

func (s *Service) preparePublishReceive(ctx context.Context, req PublishReceiveRequest, verifyCredential func(context.Context, *nativeoperation_model.Operation) error) (*PreparedPublishReceive, *PublishReceiveOutcome, error) {
	now := s.clock()
	op, err := nativeoperation_model.LookupOperation(ctx, req.InstallationID, req.OperationID)
	if err != nil {
		return nil, nil, err
	}
	if op == nil {
		return nil, &PublishReceiveOutcome{Status: 404, Code: PublishReceiveNotObserved}, nil
	}
	if !op.Submitted {
		record := ToRecord(op)
		return nil, &PublishReceiveOutcome{Status: 409, Code: PublishReceiveCancelledBeforeSeen, Record: &record}, nil
	}
	if op.IsTerminal() {
		record := ToRecord(op)
		code := PublishReceiveReplayNotCommitted
		if op.EffectState == nativeoperation_model.EffectCommitted {
			code = PublishReceiveReplayCommitted
		}
		// A replay returns its earlier outcome without launching another
		// receiver and without touching refs.
		return nil, &PublishReceiveOutcome{Status: 409, Code: code, Record: &record}, nil
	}
	if op.Kind != nativeoperation_model.KindRefPublish {
		return nil, &PublishReceiveOutcome{Status: 400, Code: PublishReceiveKindMismatch}, nil
	}
	if op.RepositoryID != req.RepositoryID {
		return nil, &PublishReceiveOutcome{Status: 403, Code: PublishReceiveRepositoryMismatch}, nil
	}
	if op.ActorID != req.ActorID {
		return nil, &PublishReceiveOutcome{Status: 403, Code: PublishReceiveActorMismatch}, nil
	}
	if err := verifyCredential(ctx, op); err != nil {
		if errors.Is(err, ErrAuthorityLost) {
			return nil, &PublishReceiveOutcome{Status: 403, Code: PublishReceiveCredentialMismatch}, nil
		}
		return nil, nil, err
	}
	if op.Revoked {
		return s.refusePublishReceive(ctx, op, nativeoperation_model.ReasonCancelledBeforeAdmission)
	}
	if op.Admitted {
		// Admitted but neither terminal nor holding its owner is
		// inconsistent; fail closed without recording.
		return nil, nil, errors.New("publish operation is admitted without an execution")
	}
	if err := s.revalidateSubmission(ctx, op); err != nil {
		if errors.Is(err, ErrAuthorityLost) {
			return s.refusePublishReceive(ctx, op, nativeoperation_model.ReasonAuthorityLost)
		}
		return nil, nil, err
	}
	if now >= op.NotAfter {
		return s.refusePublishReceive(ctx, op, nativeoperation_model.ReasonExpiredBeforeAdmission)
	}
	intent, err := publishIntentFromCanonical(op.Intent)
	if err != nil {
		return nil, nil, err
	}
	repository, err := repo_model.GetRepositoryByID(ctx, op.RepositoryID)
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return s.refusePublishReceive(ctx, op, nativeoperation_model.ReasonNativeRefused)
		}
		return nil, nil, err
	}
	gitRepo, err := gitrepo.OpenRepository(ctx, repository)
	if err != nil {
		return nil, nil, err
	}
	target, err := VerifyPublishLaunchTarget(ctx, repository, gitRepo, intent)
	_ = gitRepo.Close()
	if err != nil {
		var refused ErrExactPublishRefused
		if errors.As(err, &refused) {
			return s.refusePublishReceive(ctx, op, publishRefusalReason(refused.Reason))
		}
		return nil, nil, err
	}
	dir, err := s.execDir()
	if err != nil {
		return nil, nil, err
	}
	capabilityPath, secret, err := execcontext.WriteCapabilityFile(dir)
	if err != nil {
		return nil, nil, err
	}
	retire := func() { _ = os.Remove(capabilityPath) }
	scope := Scope{
		Kind:         nativeoperation_model.OwnerConditional,
		RepositoryID: op.RepositoryID,
		Ref:          intent.Ref,
		OldOID:       target.OldOID,
		NewOID:       target.NewOID,
		HeadRef:      intent.ComparisonRef,
		HeadOID:      intent.ExpectedComparisonOID,
	}
	if intent.Correction != nil {
		scope.PRNumber = intent.Correction.Number
		scope.CorrectionAuthorID = intent.Correction.ExpectedAuthorID
	}
	encoded, err := encodeScope(scope)
	if err != nil {
		retire()
		return nil, nil, err
	}
	owner := conditionalOwner(req.InstallationID, req.OperationID)
	claimedOp, claimed, err := nativeoperation_model.ClaimPublishReceive(ctx, req.InstallationID, req.OperationID, owner, encoded, execcontext.Verifier(secret), now)
	if err != nil {
		retire()
		switch {
		case errors.Is(err, nativeoperation_model.ErrBusy):
			return nil, &PublishReceiveOutcome{Status: 503, Code: PublishReceiveBusy}, nil
		case errors.Is(err, nativeoperation_model.ErrInhibited):
			return nil, &PublishReceiveOutcome{Status: 503, Code: PublishReceiveInhibited}, nil
		case errors.Is(err, nativeoperation_model.ErrStaleRevision):
			return s.refusePublishReceive(ctx, op, nativeoperation_model.ReasonStaleNativeRevision)
		case errors.Is(err, nativeoperation_model.ErrOperationExpired):
			return s.refusePublishReceive(ctx, op, nativeoperation_model.ReasonExpiredBeforeAdmission)
		case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
			return s.mapLostPublishClaim(ctx, req)
		default:
			return nil, nil, err
		}
	}
	_ = claimedOp
	return &PreparedPublishReceive{
		Operation:  op,
		Intent:     intent,
		Target:     target,
		Scope:      scope,
		Execution:  &execcontext.Execution{Owner: owner, Generation: claimed.Generation, CapabilityPath: capabilityPath},
		Repository: repository,
	}, nil, nil
}

// verifyReceiveCredential requires the presented PAT secret to be the exact
// bound token generation: the secret must verify against the current
// authoritative hash/salt of the recorded token row. Any other credential
// is ErrAuthorityLost; only database failures are errors.
func (s *Service) verifyReceiveCredential(ctx context.Context, op *nativeoperation_model.Operation, secret string) error {
	if secret == "" {
		return ErrAuthorityLost
	}
	token, err := auth_model.GetAccessTokenBySHA(ctx, secret)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if auth_model.IsErrAccessTokenNotExist(err) || auth_model.IsErrAccessTokenEmpty(err) {
			return ErrAuthorityLost
		}
		return err
	}
	expected := auth_model.HashToken(secret, token.TokenSalt)
	if subtle.ConstantTimeCompare([]byte(token.TokenHash), []byte(expected)) != 1 {
		return ErrAuthorityLost
	}
	if token.ID != op.TokenID || token.UID != op.ActorID {
		return ErrAuthorityLost
	}
	if extensionauth_model.CredentialFingerprint(token.TokenHash, token.TokenSalt) != op.CredentialFingerprint {
		return ErrAuthorityLost
	}
	return nil
}

// refusePublishReceive terminally records a pre-launch publish refusal. No
// owner is held on these paths, so nothing is released.
//
//nolint:nilnil // Prepared requests and reported outcomes are mutually exclusive.
func (s *Service) refusePublishReceive(ctx context.Context, op *nativeoperation_model.Operation, reason string) (*PreparedPublishReceive, *PublishReceiveOutcome, error) {
	recorded, err := nativeoperation_model.SetTerminal(context.WithoutCancel(ctx), op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{
		EffectState:  nativeoperation_model.EffectNotCommitted,
		Reason:       reason,
		Cancellation: nativeoperation_model.CancellationNone,
	}, "")
	if err != nil {
		return nil, nil, err
	}
	record := ToRecord(recorded)
	return nil, &PublishReceiveOutcome{Status: 409, Code: reason, Record: &record}, nil
}

// mapLostPublishClaim reconciles a claim that lost its atomic race: a
// concurrent cancellation records cancelled_before_admission, a completed
// operation replays its outcome, and anything else fails closed without
// recording.
//
//nolint:nilnil // Prepared requests and reported outcomes are mutually exclusive.
func (s *Service) mapLostPublishClaim(ctx context.Context, req PublishReceiveRequest) (*PreparedPublishReceive, *PublishReceiveOutcome, error) {
	op, err := nativeoperation_model.LookupOperation(ctx, req.InstallationID, req.OperationID)
	if err != nil {
		return nil, nil, err
	}
	if op == nil || !op.Submitted {
		return nil, &PublishReceiveOutcome{Status: 404, Code: PublishReceiveNotObserved}, nil
	}
	if op.IsTerminal() {
		record := ToRecord(op)
		code := PublishReceiveReplayNotCommitted
		if op.EffectState == nativeoperation_model.EffectCommitted {
			code = PublishReceiveReplayCommitted
		}
		return nil, &PublishReceiveOutcome{Status: 409, Code: code, Record: &record}, nil
	}
	if op.Revoked {
		return s.refusePublishReceive(ctx, op, nativeoperation_model.ReasonCancelledBeforeAdmission)
	}
	return nil, nil, errors.New("publish operation lost its receive claim without an owner")
}

// publishIntentFromCanonical extracts the validated publish payload from a
// recorded canonical intent envelope.
func publishIntentFromCanonical(canonical string) (*PublishIntent, error) {
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(canonical), &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Payload) == 0 {
		return nil, errors.New("publish operation intent carries no payload")
	}
	intent, err := ParsePublishPayload(envelope.Payload)
	if err != nil {
		return nil, err
	}
	return intent, nil
}

// publishRefusalReason maps exact-publish target/state refusals to the
// bounded terminal reasons. Result refusals never reach this mapping: they
// resolve in reconciliation as indeterminate with the fence retained.
func publishRefusalReason(reason string) string {
	switch reason {
	case ExactPublishRefusedBranchExists,
		ExactPublishRefusedMissingBranch,
		ExactPublishRefusedStaleOld,
		ExactPublishRefusedMissingCommit,
		ExactPublishRefusedNotFastForward:
		return nativeoperation_model.ReasonStaleHead
	case ExactPublishRefusedMissingCompare,
		ExactPublishRefusedStaleCompare:
		return nativeoperation_model.ReasonStaleBaseOrResult
	case ExactPublishRefusedMissingPR,
		ExactPublishRefusedClosedPR,
		ExactPublishRefusedPRMismatch,
		ExactPublishRefusedStalePRHead:
		return nativeoperation_model.ReasonPRMismatch
	default:
		return nativeoperation_model.ReasonNativeRefused
	}
}

// ReconcilePublish resolves one claimed publish execution after its
// receiver exits. Attribution comes from the recorded admission plus the
// authoritative ref tips, never from the receiver's exit status alone: a
// known effect releases the owner with its receipt and completion state,
// and an unknown effect retains the fence. ReceiveErr carries the
// receive-pack result; a committed effect with a failed receiver reports
// needs_intervention completion rather than guessing that bookkeeping ran.
//
//nolint:revive // Callers consume the operation completion callback without constructing it.
func (s *Service) ReconcilePublish(ctx context.Context, prepared *PreparedPublishReceive, receiveErr error) (sdk.OperationRecord, terminalResult, error) {
	owner := prepared.Execution.Owner
	releaseCtx := context.WithoutCancel(ctx)
	retain := func() (sdk.OperationRecord, terminalResult, error) {
		recorded, err := nativeoperation_model.SetTerminal(releaseCtx, prepared.Operation.InstallationID, prepared.Operation.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState:  nativeoperation_model.EffectIndeterminate,
			Cancellation: nativeoperation_model.CancellationNone,
		}, "")
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(recorded), terminalResult{retain: true}, nil
	}
	fresh, err := nativeoperation_model.LookupOperation(releaseCtx, prepared.Operation.InstallationID, prepared.Operation.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, terminalResult{}, err
	}
	if fresh == nil {
		return sdk.OperationRecord{}, terminalResult{}, errors.New("operation vanished during execution")
	}
	repoPath := prepared.Repository.RepoPath()
	tip, tipAbsent, tipOK := s.publishTip(releaseCtx, repoPath, prepared.Target.Ref)
	if !tipOK {
		return retain()
	}
	comparison, comparisonOK := s.publishTipValue(releaseCtx, repoPath, prepared.Target.ComparisonRef)
	if !comparisonOK {
		return retain()
	}
	intent := prepared.Intent
	target := prepared.Target
	committed := fresh.Admitted && !tipAbsent &&
		strings.EqualFold(tip, target.NewOID) &&
		!strings.EqualFold(tip, target.OldOID) &&
		strings.EqualFold(comparison, target.ComparisonOID)
	noEffect := !fresh.Admitted && strings.EqualFold(comparison, target.ComparisonOID) &&
		((intent.IsCreation() && tipAbsent) ||
			(!intent.IsCreation() && !tipAbsent && strings.EqualFold(tip, target.OldOID)))
	switch {
	case committed:
		receipt, err := BuildPublishReceipt(intent, target, prepared.Operation.RepositoryID, prepared.Operation.ActorID, tip, comparison)
		if err != nil {
			return retain()
		}
		raw, _ := json.Marshal(receipt)
		completion := nativeoperation_model.CompletionComplete
		if receiveErr != nil {
			completion = nativeoperation_model.CompletionNeedsIntervention
		}
		recorded, err := nativeoperation_model.SetTerminal(releaseCtx, prepared.Operation.InstallationID, prepared.Operation.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState:  nativeoperation_model.EffectCommitted,
			Cancellation: nativeoperation_model.CancellationNone,
			Completion:   completion,
			Receipt:      string(raw),
		}, owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(recorded), terminalResult{}, nil
	case noEffect:
		reason := fresh.Reason
		if reason == "" {
			reason = nativeoperation_model.ReasonNativeRefused
		}
		recorded, err := nativeoperation_model.SetTerminal(releaseCtx, prepared.Operation.InstallationID, prepared.Operation.OperationID, nativeoperation_model.TerminalOutcome{
			EffectState:  nativeoperation_model.EffectNotCommitted,
			Reason:       reason,
			Cancellation: nativeoperation_model.CancellationNone,
		}, owner)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return ToRecord(recorded), terminalResult{}, nil
	default:
		return retain()
	}
}

// publishTip reads one reconcile ref tip. A missing ref reports absent; an
// unreadable ref reports not-OK and the caller retains the fence.
func (s *Service) publishTip(ctx context.Context, repoPath, ref string) (tip string, absent, ok bool) {
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

// publishTipValue reads one reconcile ref tip that must be present.
func (s *Service) publishTipValue(ctx context.Context, repoPath, ref string) (string, bool) {
	value, err := s.readRef(ctx, repoPath, ref)
	if err != nil || value == "" {
		return "", false
	}
	return strings.ToLower(value), true
}

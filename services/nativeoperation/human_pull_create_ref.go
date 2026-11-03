// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	repo_module "forgejo.org/modules/repository"

	"github.com/google/uuid"
)

const HumanPullCreateRefPrepared = "human-pull-create-ref-prepared"
const HumanPullCreateRefDone = "human-pull-create-ref-done"

var ErrHumanPullCreateEvidence = errors.New("native human pull creation evidence differs")
var ErrHumanPullCreateRefClaimed = errors.New("native human pull creation ref was already claimed")

// SQL-only evidence, shared by the phase transaction and native Git admission.
// This consumes committed truth rather than reauthorizing creation as its author.
func readHumanPullCreateReceipt(ctx context.Context, q sdk.HumanPullCreateRequest, raw string) (*issues_model.PullRequest, error) {
	var r sdk.HumanPullCreateReceipt
	if json.Unmarshal([]byte(raw), &r) != nil || !sdk.ValidHumanPullCreateReceipt(r, q) {
		return nil, ErrHumanPullCreateEvidence
	}
	encoded, err := json.Marshal(r)
	if err != nil || string(encoded) != raw {
		return nil, ErrHumanPullCreateEvidence
	}
	pr, err := issues_model.GetPullRequestByID(ctx, r.PullID)
	if err != nil {
		return nil, err
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return nil, err
	}
	issue := pr.Issue
	body := sha256.Sum256([]byte(issue.Content))
	if pr.BaseRepoID != r.RepositoryID || pr.HeadRepoID != r.HeadRepositoryID || pr.IssueID != r.IssueID || pr.Index != r.Index || pr.BaseBranch != r.BaseBranch || pr.HeadBranch != r.HeadBranch || pr.MergeBase != r.MergeBaseOID || pr.AllowMaintainerEdit != r.AllowMaintainerEdit || pr.Type != issues_model.PullRequestGitea || pr.Flow != issues_model.PullRequestFlowGithub || pr.HasMerged ||
		!issue.IsPull || issue.IsClosed || issue.RepoID != r.RepositoryID || issue.ID != r.IssueID || issue.Index != r.Index || issue.PosterID != r.ActorID || issue.Title != r.Title || int64(issue.ContentVersion) != r.ContentVersion || hex.EncodeToString(body[:]) != r.BodyDigest {
		return nil, ErrHumanPullCreateEvidence
	}
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return nil, err
	}
	if err := pr.LoadHeadRepo(ctx); err != nil {
		return nil, err
	}
	pr.HeadCommitID = r.HeadOID // Native field is computed, not a SQL column.
	issue.Repo, issue.PullRequest = pr.BaseRepo, pr
	return pr, nil
}

func checkHumanPullCreateScope(q sdk.HumanPullCreateRequest, scope Scope, raw string, pr *issues_model.PullRequest) error {
	exact := humanPullCreateScope(q)
	switch scope.CompletionPhase {
	case "":
	case HumanPullCreateRefPrepared, HumanPullCreateRefDone, HumanPullCreateFinishStarted, HumanPullCreateFinishDone:
		exact.CompletionRef, exact.CompletionNewOID, exact.CompletionPhase = pr.GetGitRefName(), q.Expected.HeadOID, scope.CompletionPhase
	default:
		return ErrHumanPullCreateEvidence
	}
	encoded, err := encodeScope(exact)
	if err != nil || encoded != raw {
		return ErrHumanPullCreateEvidence
	}
	return nil
}

func readHumanPullCreateHeld(ctx context.Context, installation string, q sdk.HumanPullCreateRequest) (*ValidIntent, *nativeoperation_model.Reservation, Scope, *issues_model.PullRequest, *nativeoperation_model.Operation, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation {
		return nil, nil, Scope{}, nil, nil, ErrInvalidIntent
	}
	v, err := humanPullCreateIntent(q)
	if err != nil {
		return nil, nil, Scope{}, nil, nil, err
	}
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return nil, nil, Scope{}, nil, nil, err
	}
	x := execcontext.FromContext(ctx)
	if x == nil || x.Owner != held.Owner || x.Generation != held.Generation || held.Owner != conditionalOwner(installation, q.OperationID) || held.OwnerKind != nativeoperation_model.OwnerConditional || held.Revision != v.ExpectedNativeRevision+1 {
		return nil, nil, Scope{}, nil, nil, nativeoperation_model.ErrWrongOwner
	}
	proof, err := execcontext.ReadCapabilityFile(x.CapabilityPath)
	if err != nil || !execcontext.VerifyProof(held.Verifier, proof) {
		return nil, nil, Scope{}, nil, nil, nativeoperation_model.ErrWrongOwner
	}
	op, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return nil, nil, Scope{}, nil, nil, err
	}
	if !humanMergeCaptureMatches(op, v) || !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted || op.Completion != nativeoperation_model.CompletionPending || op.Receipt == "" {
		return nil, nil, Scope{}, nil, nil, ErrHumanPullCreateEvidence
	}
	pr, err := readHumanPullCreateReceipt(ctx, q, op.Receipt)
	if err != nil {
		return nil, nil, Scope{}, nil, nil, err
	}
	scope, err := decodeScope(held.Scope)
	if err == nil {
		err = checkHumanPullCreateScope(q, scope, held.Scope, pr)
	}
	return v, held, scope, pr, op, err
}

// Git reads must never run in the primary/phase SQL transaction.
func checkHumanPullCreateTips(ctx context.Context, q sdk.HumanPullCreateRequest, pr *issues_model.PullRequest) error {
	if db.InTransaction(ctx) {
		return ErrInvalidIntent
	}
	for _, item := range []struct{ path, ref, oid string }{
		{pr.BaseRepo.RepoPath(), git.BranchPrefix + q.Expected.BaseBranch, q.Expected.BaseOID},
		{pr.HeadRepo.RepoPath(), git.BranchPrefix + q.Expected.HeadBranch, q.Expected.HeadOID},
	} {
		native, err := git.OpenRepository(ctx, item.path)
		if err != nil {
			return err
		}
		oid, err := native.GetRefCommitID(item.ref)
		native.Close()
		if err != nil || oid != item.oid {
			return ErrHumanPullCreateEvidence
		}
	}
	return nil
}

// CompleteHumanPullCreateRef consumes the committed SQL receipt to establish
// exactly its new internal PR head ref. One durable claim precedes Git; another
// caller never resumes an uncertain writer. Completion/notifications and owner
// release remain separate. No caller SQL transaction spans object transfer/Git.
func (s *Service) CompleteHumanPullCreateRef(ctx context.Context, installation string, q sdk.HumanPullCreateRequest) (sdk.OperationRecord, error) {
	if db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	v, held, scope, pr, op, err := readHumanPullCreateHeld(ctx, installation, q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if err := checkHumanPullCreateTips(ctx, q, pr); err != nil {
		return sdk.OperationRecord{}, err
	}
	tip, absent, known := s.prCreateRefTip(ctx, pr.BaseRepo.RepoPath(), pr.GetGitRefName())
	if scope.CompletionPhase == HumanPullCreateRefDone {
		if !known || absent || tip != q.Expected.HeadOID {
			return sdk.OperationRecord{}, ErrHumanPullCreateEvidence
		}
		return ToRecord(op), nil
	}
	if scope.CompletionPhase != "" {
		return sdk.OperationRecord{}, ErrHumanPullCreateRefClaimed
	}
	if !known || !absent {
		return sdk.OperationRecord{}, ErrHumanPullCreateEvidence
	}
	started := scope
	started.CompletionRef, started.CompletionNewOID, started.CompletionPhase = pr.GetGitRefName(), q.Expected.HeadOID, HumanPullCreateRefPrepared
	startedJSON, err := encodeScope(started)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	want := humanPullCreateOperation(installation, v)
	transition := func(c context.Context, before, after string) (*nativeoperation_model.Operation, error) {
		var recorded *nativeoperation_model.Operation
		err := nativeoperation_model.WithHumanPullCreateReceipt(c, want, held.Owner, held.Generation, before, op.Receipt, func(tx context.Context, current *nativeoperation_model.Operation) error {
			if _, err := readHumanPullCreateReceipt(tx, q, current.Receipt); err != nil {
				return err
			}
			var err error
			recorded, err = nativeoperation_model.ChangeHumanPullCreatePhase(tx, want, held.Owner, held.Generation, before, after, current.Receipt)
			return err
		})
		return recorded, err
	}
	if _, err := transition(ctx, held.Scope, startedJSON); err != nil {
		return sdk.OperationRecord{}, err
	}
	// Fetch only the captured object, without local refs, tags or FETCH_HEAD.
	// This also handles private forks whose commits do not exist in the base.
	env := execcontext.AppendExecEnv(repo_module.RefWriteEnvironment(pr.BaseRepo), execcontext.FromContext(ctx))
	if _, _, err := git.NewCommand(ctx, "fetch", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--no-recurse-submodules", "--end-of-options").AddDynamicArguments("file://"+pr.HeadRepo.RepoPath(), q.Expected.HeadOID).RunStdString(&git.RunOpts{Dir: pr.BaseRepo.RepoPath(), Env: env}); err != nil {
		return sdk.OperationRecord{}, err
	}
	if err := checkHumanPullCreateTips(ctx, q, pr); err != nil {
		return sdk.OperationRecord{}, err
	}
	// An allocated PR ref must be absent. CAS and the generated native hook
	// independently enforce the exact zero->captured-head tuple.
	zero := strings.Repeat("0", len(q.Expected.HeadOID))
	if _, _, err := git.NewCommand(ctx, "update-ref").AddDynamicArguments(pr.GetGitRefName(), q.Expected.HeadOID, zero).RunStdString(&git.RunOpts{Dir: pr.BaseRepo.RepoPath(), Env: env}); err != nil {
		return sdk.OperationRecord{}, err
	}
	tip, absent, known = s.prCreateRefTip(ctx, pr.BaseRepo.RepoPath(), pr.GetGitRefName())
	if !known || absent || tip != q.Expected.HeadOID {
		return sdk.OperationRecord{}, ErrHumanPullCreateEvidence
	}
	done := started
	done.CompletionPhase = HumanPullCreateRefDone
	doneJSON, err := encodeScope(done)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	recorded, err := transition(context.WithoutCancel(ctx), startedJSON, doneJSON)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	return ToRecord(recorded), nil
}

// Called only by prepared native reference-transaction admission after the
// verifier, generation and target repository have already matched. SQL receipt,
// exact capture/scope and both branch tips are re-read before admitting one ref.
func (s *Service) admitHumanPullCreateRef(ctx context.Context, held *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation, lines []RefLine) (TransactionDecision, error) {
	deny := TransactionDecision{Reason: nativeoperation_model.ReasonUnexpectedRefEffects}
	var captured struct {
		Payload sdk.HumanPullCreateRequest `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &captured) != nil {
		return TransactionDecision{Reason: nativeoperation_model.ReasonWrongOwner}, nil
	}
	q := captured.Payload
	v, err := humanPullCreateIntent(q)
	if err != nil || !humanMergeCaptureMatches(op, v) || !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted || op.Completion != nativeoperation_model.CompletionPending || held.Revision != v.ExpectedNativeRevision+1 || scope.CompletionPhase != HumanPullCreateRefPrepared {
		return TransactionDecision{Reason: nativeoperation_model.ReasonWrongOwner}, nil
	}
	pr, err := readHumanPullCreateReceipt(ctx, q, op.Receipt)
	if err != nil {
		return TransactionDecision{Reason: nativeoperation_model.ReasonPRMismatch}, nil
	}
	if checkHumanPullCreateScope(q, scope, held.Scope, pr) != nil {
		return deny, nil
	}
	if err := checkHumanPullCreateTips(ctx, q, pr); err != nil {
		return TransactionDecision{Reason: nativeoperation_model.ReasonStaleBaseOrResult}, nil
	}
	// Object-only fetch can invoke an empty ref transaction. It grants no
	// write, but still requires the exact committed capture and prepared scope.
	if len(lines) == 0 {
		return TransactionDecision{Allowed: true}, nil
	}
	if len(lines) != 1 || lines[0].Ref != pr.GetGitRefName() || lines[0].Old != strings.Repeat("0", len(q.Expected.HeadOID)) || lines[0].New != q.Expected.HeadOID {
		return deny, nil
	}
	return TransactionDecision{Allowed: true}, nil
}

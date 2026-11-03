// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	extensionauth_model "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
	pull_service "forgejo.org/services/pull"

	"github.com/google/uuid"
)

var errHumanPullCreateChanged = errors.New("human pull creation preparation changed")
var errHumanPullCreateComparison = errors.New("native human pull comparison refused")

func humanPullCreateIntent(q sdk.HumanPullCreateRequest) (*ValidIntent, error) {
	if !sdk.ValidHumanPullCreateRequest(q) {
		return nil, ErrInvalidIntent
	}
	p := q.Expected
	v := &ValidIntent{OperationID: q.OperationID, ActorID: nativeCommentID(p.ActorID), RepositoryID: nativeCommentID(p.RepositoryID), Kind: nativeoperation_model.KindHumanPullCreate, AuthRevision: sdk.HumanPullCreateAuthorizationRevision, ExpectedNativeRevision: p.NativeRevision, NotAfter: q.NotAfter}
	var err error
	v.Canonical, v.Digest, err = sdk.CanonicalHumanPullCreateIntent(q)
	return v, err
}

func humanPullCreateScope(q sdk.HumanPullCreateRequest) Scope {
	p := q.Expected
	return Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: nativeCommentID(p.RepositoryID), HeadRepositoryID: nativeCommentID(p.HeadRepositoryID), Ref: git.BranchPrefix + p.BaseBranch, OldOID: p.BaseOID, HeadRef: git.BranchPrefix + p.HeadBranch, HeadOID: p.HeadOID}
}

func humanPullCreateOperation(installation string, v *ValidIntent) *nativeoperation_model.Operation {
	return &nativeoperation_model.Operation{InstallationID: installation, OperationID: v.OperationID, Kind: v.Kind, ActorID: v.ActorID, RepositoryID: v.RepositoryID, AuthRevision: v.AuthRevision, ExpectedNativeRevision: v.ExpectedNativeRevision, NotAfter: v.NotAfter, Intent: v.Canonical, IntentDigest: v.Digest}
}

// No Git runs here: these current policy reads also run inside primary SQL.
// Explicit maintainer delegation follows native SetAllowEdits: the PR's author
// must currently be able to write source Code before delegating that access.
func humanPullCreateParticipants(ctx context.Context, q sdk.HumanPullCreateRequest, admit HumanReviewAdmission) (*repo_model.Repository, *repo_model.Repository, *user_model.User, error) {
	if ctx.Err() != nil || admit(ctx) != nil {
		return nil, nil, nil, ErrAuthorityLost
	}
	p := q.Expected
	if err := requireHumanPrimaryOwner(ctx, p.NativeRevision); err != nil {
		return nil, nil, nil, err
	}
	actor, err := user_model.GetUserByID(ctx, nativeCommentID(p.ActorID))
	if err != nil || !actor.IsAccessAllowed(ctx) {
		return nil, nil, nil, ErrAuthorityLost
	}
	base, err := repo_model.GetRepositoryByID(ctx, nativeCommentID(p.RepositoryID))
	if err != nil || base.IsArchived || !base.AllowsPulls(ctx) || user_model.IsBlocked(ctx, base.OwnerID, actor.ID) {
		return nil, nil, nil, ErrAuthorityLost
	}
	head, err := repo_model.GetRepositoryByID(ctx, nativeCommentID(p.HeadRepositoryID))
	if err != nil {
		return nil, nil, nil, ErrAuthorityLost
	}
	basePermission, err := access_model.GetUserRepoPermission(ctx, base, actor)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
		if kind.UnitGlobalDisabled() || !base.UnitEnabled(ctx, kind) || !basePermission.CanRead(kind) {
			return nil, nil, nil, ErrAuthorityLost
		}
	}
	headPermission, err := access_model.GetUserRepoPermission(ctx, head, actor)
	if err != nil {
		return nil, nil, nil, err
	}
	if !head.UnitEnabled(ctx, unit.TypeCode) || !headPermission.CanRead(unit.TypeCode) || q.AllowMaintainerEdit && !headPermission.CanWrite(unit.TypeCode) {
		return nil, nil, nil, ErrAuthorityLost
	}
	if base.ObjectFormatName != head.ObjectFormatName {
		return nil, nil, nil, errHumanPullCreateChanged
	}
	return base, head, actor, nil
}

// Ref reads and native comparison belong outside primary SQL. The held native
// owner fences participating ref writers through the transaction; this kind has
// no Git write admission. The separate completion consumer will bind its own
// derived ref to the committed receipt before any such write.
func checkHumanPullCreateSource(ctx context.Context, q sdk.HumanPullCreateRequest, admit HumanReviewAdmission) (*repo_model.Repository, *repo_model.Repository, *user_model.User, error) {
	if db.InTransaction(ctx) {
		return nil, nil, nil, ErrInvalidIntent
	}
	base, head, actor, err := humanPullCreateParticipants(ctx, q, admit)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, item := range []struct {
		repo        *repo_model.Repository
		branch, oid string
	}{{base, q.Expected.BaseBranch, q.Expected.BaseOID}, {head, q.Expected.HeadBranch, q.Expected.HeadOID}} {
		native, err := git.OpenRepository(ctx, item.repo.RepoPath())
		if err != nil {
			return nil, nil, nil, err
		}
		oid, err := native.GetRefCommitID(git.BranchPrefix + item.branch)
		native.Close()
		if err != nil || oid != item.oid {
			return nil, nil, nil, errHumanPullCreateChanged
		}
	}
	if err := humanPullCreateDuplicate(ctx, q); err != nil {
		return nil, nil, nil, err
	}
	return base, head, actor, nil
}

func humanPullCreateDuplicate(ctx context.Context, q sdk.HumanPullCreateRequest) error {
	p := q.Expected
	_, err := issues_model.GetUnmergedPullRequest(ctx, nativeCommentID(p.HeadRepositoryID), nativeCommentID(p.RepositoryID), p.HeadBranch, p.BaseBranch, issues_model.PullRequestFlowGithub)
	if err == nil {
		return errPRCreateDuplicate
	}
	if !issues_model.IsErrPullRequestNotExist(err) {
		return err
	}
	return nil
}

func (s *Service) checkHumanPullCreateCapture(ctx context.Context, installation string, v *ValidIntent, scope string) error {
	if ctx.Err() != nil {
		return ErrAuthorityLost
	}
	if s.clock() >= v.NotAfter {
		return nativeoperation_model.ErrOperationExpired
	}
	op, err := nativeoperation_model.LookupOperation(ctx, installation, v.OperationID)
	if err != nil {
		return err
	}
	if !humanMergeCaptureMatches(op, v) || op.Admitted || op.EffectState != nativeoperation_model.EffectPending || op.Receipt != "" || op.Completion != "" || op.Revoked {
		return nativeoperation_model.ErrAdmissionLost
	}
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return err
	}
	x := execcontext.FromContext(ctx)
	if x == nil || held.Owner != conditionalOwner(installation, v.OperationID) || x.Owner != held.Owner || x.Generation != held.Generation || held.Scope != scope || held.OwnerKind != nativeoperation_model.OwnerConditional {
		return nativeoperation_model.ErrWrongOwner
	}
	proof, err := execcontext.ReadCapabilityFile(x.CapabilityPath)
	if err != nil || !execcontext.VerifyProof(held.Verifier, proof) {
		return nativeoperation_model.ErrWrongOwner
	}
	return requireHumanPrimaryOwner(ctx, v.ExpectedNativeRevision)
}

// SubmitHumanPullCreatePrimary captures one human request and commits native
// issue/index/PR plus receipt atomically. It leaves its exact owner held and
// completion pending. It never establishes derived refs, notifies, or resumes
// existing writing. Finishing/recovery and public transport are separate owners.
func (s *Service) SubmitHumanPullCreatePrimary(ctx context.Context, installation string, q sdk.HumanPullCreateRequest, admit HumanReviewAdmission) (sdk.OperationRecord, error) {
	return s.submitHumanPullCreate(ctx, installation, q, admit, false)
}

const (
	CrashPointHumanPullCreateBeforePrimary = "human-pull-create-before-primary"
	CrashPointHumanPullCreateInPrimary     = "human-pull-create-in-primary"
	CrashPointHumanPullCreateAfterPrimary  = "human-pull-create-after-primary"
	CrashPointHumanPullCreateAfterRef      = "human-pull-create-after-ref"
	CrashPointHumanPullCreateAfterFinish   = "human-pull-create-after-finish"
	CrashPointHumanPullCreateAfterRelease  = "human-pull-create-after-release"
)

// SubmitHumanPullCreate composes one captured human creation, exact derived ref,
// one-shot native finishing and atomic release. Existing operations return saved
// truth without resuming any writer. Post-commit completion survives request
// loss; current human admission is still required to receive the result.
func (s *Service) SubmitHumanPullCreate(ctx context.Context, installation string, q sdk.HumanPullCreateRequest, admit HumanReviewAdmission) (sdk.OperationRecord, error) {
	return s.submitHumanPullCreate(ctx, installation, q, admit, true)
}

func (s *Service) submitHumanPullCreate(ctx context.Context, installation string, q sdk.HumanPullCreateRequest, admit HumanReviewAdmission, complete bool) (sdk.OperationRecord, error) {
	id, err := uuid.Parse(installation)
	if err != nil || id.String() != installation || admit == nil || db.InTransaction(ctx) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	v, err := humanPullCreateIntent(q)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if ctx.Err() != nil {
		return sdk.OperationRecord{}, ctx.Err()
	}
	if admit(ctx) != nil {
		return sdk.OperationRecord{}, ErrAuthorityLost
	}
	finish := func(r sdk.OperationRecord, err error) (sdk.OperationRecord, error) {
		if err == nil || errors.Is(err, ErrCancelledBeforeSubmit) {
			if ctx.Err() != nil || admit(ctx) != nil {
				return sdk.OperationRecord{}, ErrAuthorityLost
			}
		}
		return r, err
	}
	existing, err := nativeoperation_model.LookupOperation(ctx, installation, q.OperationID)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	if existing != nil {
		r, e := s.reconcileExisting(existing, v)
		return finish(r, e)
	}
	if q.NotAfter <= s.clock() || q.NotAfter > s.clock()+int64(MaxOperationLifetime/time.Second) {
		return sdk.OperationRecord{}, ErrInvalidIntent
	}
	scope := humanPullCreateScope(q)
	rawScope, err := encodeScope(scope)
	if err != nil {
		return sdk.OperationRecord{}, err
	}
	r, err := s.claimConditionalSubmit(ctx, extensionauth_model.SubmissionDecision{ActorID: v.ActorID}, installation, v, scope, func(ctx context.Context, _ *nativeoperation_model.Operation, _ *ValidIntent, x *execcontext.Execution) (sdk.OperationRecord, terminalResult, error) {
		owned, settle := execcontext.NewContext(ctx, x), context.WithoutCancel(ctx)
		refuse := func(reason string) (sdk.OperationRecord, terminalResult, error) {
			r, err := nativeoperation_model.RefuseHumanPullCreate(settle, humanPullCreateOperation(installation, v), x.Owner, x.Generation, rawScope, reason)
			if err != nil {
				return sdk.OperationRecord{}, terminalResult{}, err
			}
			return ToRecord(r), terminalResult{}, nil
		}
		perform := func() (*nativeoperation_model.Operation, error) {
			if complete {
				if err := TestCrashBarrier(CrashPointHumanPullCreateBeforePrimary); err != nil {
					return nil, err
				}
			}
			if err := s.checkHumanPullCreateCapture(owned, installation, v, rawScope); err != nil {
				return nil, err
			}
			base, head, actor, err := checkHumanPullCreateSource(owned, q, admit)
			if err != nil {
				return nil, err
			}
			pull := &issues_model.PullRequest{HeadRepoID: head.ID, BaseRepoID: base.ID, HeadBranch: q.Expected.HeadBranch, BaseBranch: q.Expected.BaseBranch, HeadRepo: head, BaseRepo: base, Type: issues_model.PullRequestGitea, Flow: issues_model.PullRequestFlowGithub}
			if err := pull_service.PreparePullRequestComparison(owned, pull); err != nil {
				return nil, fmt.Errorf("%w: %v", errHumanPullCreateComparison, err)
			}
			if pull.HeadCommitID != q.Expected.HeadOID || !sdk.ValidPullDiffCommit(pull.MergeBase) {
				return nil, errHumanPullCreateChanged
			}
			base, head, actor, err = checkHumanPullCreateSource(owned, q, admit)
			if err != nil {
				return nil, err
			}
			pull.BaseRepo, pull.HeadRepo = base, head
			if err := s.checkHumanPullCreateCapture(owned, installation, v, rawScope); err != nil {
				return nil, err
			}
			return nativeoperation_model.CommitHumanPullCreatePrimary(owned, humanPullCreateOperation(installation, v), x.Owner, x.Generation, rawScope, s.clock(), func(tx context.Context) (string, error) {
				if err := s.checkHumanPullCreateCapture(tx, installation, v, rawScope); err != nil {
					return "", err
				}
				base, _, current, err := humanPullCreateParticipants(tx, q, admit)
				if err != nil {
					return "", err
				}
				if err := humanPullCreateDuplicate(tx, q); err != nil {
					return "", err
				}
				issue := &issues_model.Issue{RepoID: base.ID, Repo: base, PosterID: current.ID, Poster: current, IsPull: true, Title: q.Title, Content: q.Body}
				if err := issues_model.NewPullRequest(tx, base, issue, nil, nil, pull); err != nil {
					return "", err
				}
				pull.Issue, issue.PullRequest = issue, pull
				if q.AllowMaintainerEdit {
					if err := pull_service.SetAllowEdits(tx, current, pull, true); err != nil {
						return "", err
					}
				}
				if _, _, _, err := humanPullCreateParticipants(tx, q, admit); err != nil {
					return "", err
				}
				if err := s.checkHumanPullCreateCapture(tx, installation, v, rawScope); err != nil {
					return "", err
				}
				body := sha256.Sum256([]byte(issue.Content))
				receipt := sdk.HumanPullCreateReceipt{RepositoryID: base.ID, HeadRepositoryID: head.ID, ActorID: actor.ID, PullID: pull.ID, IssueID: issue.ID, Index: pull.Index, BaseBranch: pull.BaseBranch, HeadBranch: pull.HeadBranch, BaseOID: q.Expected.BaseOID, HeadOID: pull.HeadCommitID, MergeBaseOID: pull.MergeBase, Title: issue.Title, BodyDigest: hex.EncodeToString(body[:]), ContentVersion: int64(issue.ContentVersion), AllowMaintainerEdit: pull.AllowMaintainerEdit, IntentDigest: v.Digest}
				if !sdk.ValidHumanPullCreateReceipt(receipt, q) {
					return "", errors.New("native human pull creation receipt differs")
				}
				raw, err := json.Marshal(receipt)
				if err == nil && complete {
					err = TestCrashBarrier(CrashPointHumanPullCreateInPrimary)
				}
				return string(raw), err
			})
		}
		committed, err := perform()
		if err != nil {
			switch {
			case errors.Is(err, ErrAuthorityLost), errors.Is(err, pull_service.ErrUserHasNoPermissionForAction):
				return refuse(nativeoperation_model.ReasonAuthorityLost)
			case errors.Is(err, errHumanPullCreateChanged):
				return refuse(nativeoperation_model.ReasonStaleBaseOrResult)
			case errors.Is(err, errPRCreateDuplicate):
				return refuse(nativeoperation_model.ReasonDuplicatePullRequest)
			case errors.Is(err, errHumanPullCreateComparison):
				return refuse(nativeoperation_model.ReasonNativeRefused)
			case errors.Is(err, nativeoperation_model.ErrOperationExpired):
				return refuse(nativeoperation_model.ReasonExpiredBeforeAdmission)
			case errors.Is(err, nativeoperation_model.ErrAdmissionLost):
				op, readErr := nativeoperation_model.LookupOperation(settle, installation, q.OperationID)
				if readErr != nil {
					return sdk.OperationRecord{}, terminalResult{}, readErr
				}
				if op != nil && op.Revoked {
					return refuse(nativeoperation_model.ReasonCancelledBeforeAdmission)
				}
			}
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if !complete {
			return ToRecord(committed), terminalResult{retain: true}, nil
		}
		if err := TestCrashBarrier(CrashPointHumanPullCreateAfterPrimary); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		// Only the committed capture grants this bounded completion. A lost
		// request cannot erase it or authorize another primary/notification run.
		completionCtx := context.WithoutCancel(owned)
		if _, err := s.CompleteHumanPullCreateRef(completionCtx, installation, q); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if err := TestCrashBarrier(CrashPointHumanPullCreateAfterRef); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		record, err := s.FinishHumanPullCreate(completionCtx, installation, q)
		if record.CompletionState == nativeoperation_model.CompletionNeedsIntervention {
			return record, terminalResult{retain: true}, nil
		}
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if err := TestCrashBarrier(CrashPointHumanPullCreateAfterFinish); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		record, err = s.CompleteHumanPullCreate(completionCtx, installation, q)
		if err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		if err := TestCrashBarrier(CrashPointHumanPullCreateAfterRelease); err != nil {
			return sdk.OperationRecord{}, terminalResult{}, err
		}
		return record, terminalResult{}, nil
	})
	return finish(r, err)
}

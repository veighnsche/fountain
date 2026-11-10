// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"forgejo.org/extension-sdk/operationcontract"
)

const OperationKindHumanPullCreate = "pull_request.human_create.apply"
const HumanPullCreateAuthorizationRevision = "native-human-request"
const MaxHumanPullCreateTitleBytes = 255
const MaxHumanPullCreateBodyBytes = 64 << 10
const MaxHumanPullCreateCallbackBytes = 512 << 10
const OperationHumanPullCreateSubmit = "repository.pull.create.submit"
const OperationHumanPullCreateGet = "repository.pull.create.get"
const OperationHumanPullCreateCancel = "repository.pull.create.cancel"

var ErrHumanPullCreateQuery = errors.New("invalid human pull creation request")
var ErrHumanPullCreateAdmission = errors.New("current human pull creation admission is required")
var ErrHumanPullCreateConflict = errors.New("human pull creation operation conflicts")
var ErrHumanPullCreateUnavailable = errors.New("native human pull creation service is unavailable")

// This is immutable human intent, not a credential or creation grant. Native
// comparison is computed under the claim; the request cannot select its result.
type HumanPullCreateRequest struct {
	OperationID         string                       `json:"operation_id"`
	Expected            RepositoryPullCreateSnapshot `json:"expected"`
	NotAfter            int64                        `json:"not_after"`
	Title               string                       `json:"title"`
	Body                string                       `json:"body"`
	AllowMaintainerEdit bool                         `json:"allow_maintainer_edit"`
}

func ValidHumanPullCreateRequest(q HumanPullCreateRequest) bool {
	p := q.Expected
	return ValidRepositoryPullCreateSnapshot(&p, p.RepositoryID, p.ActorID, RepositoryPullCreateSnapshotRequest{HeadRepositoryID: p.HeadRepositoryID, BaseBranch: p.BaseBranch, HeadBranch: p.HeadBranch}) &&
		p.NativeRevision >= 1 && p.Action.State == "allowed" && !(p.RepositoryID == p.HeadRepositoryID && p.BaseBranch == p.HeadBranch) &&
		ValidHumanPullCreateOperationID(q.OperationID, p.ActorID, p.RepositoryID) && q.NotAfter > 0 &&
		q.Title != "" && strings.TrimSpace(q.Title) == q.Title && len(q.Title) <= MaxHumanPullCreateTitleBytes && utf8.ValidString(q.Title) && !strings.ContainsRune(q.Title, 0) &&
		len(q.Body) <= MaxHumanPullCreateBodyBytes && utf8.ValidString(q.Body) && !strings.ContainsRune(q.Body, 0)
}

func CanonicalHumanPullCreateIntent(q HumanPullCreateRequest) (string, string, error) {
	if !ValidHumanPullCreateRequest(q) {
		return "", "", ErrHumanPullCreateQuery
	}
	p := q.Expected
	actor, _ := strconv.ParseInt(p.ActorID, 10, 64)
	repo, _ := strconv.ParseInt(p.RepositoryID, 10, 64)
	raw, err := json.Marshal(struct {
		OperationID            string                 `json:"operation_id"`
		ActorID                int64                  `json:"actor_id"`
		RepositoryID           int64                  `json:"repository_id"`
		Kind                   string                 `json:"kind"`
		AuthorizationRevision  string                 `json:"authorization_revision"`
		ExpectedNativeRevision int64                  `json:"expected_native_revision"`
		NotAfter               int64                  `json:"not_after"`
		Payload                HumanPullCreateRequest `json:"payload"`
	}{q.OperationID, actor, repo, OperationKindHumanPullCreate, HumanPullCreateAuthorizationRevision, p.NativeRevision, q.NotAfter, q})
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

func HumanPullCreateOperationID(actor, repository, command string) (string, error) {
	return humanOperationID(operationcontract.HumanPullCreatePrefix, actor, repository, command, ErrHumanPullCreateQuery)
}

func ValidHumanPullCreateOperationID(id, actor, repository string) bool {
	return validHumanOperationID(operationcontract.HumanPullCreatePrefix, id, actor, repository)
}

// The SQL primary exists durably; derived PR refs and notification/completion
// remain separate. Private fork locators and prose are retained server-side.
type HumanPullCreateReceipt struct {
	RepositoryID        int64  `json:"repository_id"`
	HeadRepositoryID    int64  `json:"head_repository_id"`
	ActorID             int64  `json:"actor_id"`
	PullID              int64  `json:"pull_id"`
	IssueID             int64  `json:"issue_id"`
	Index               int64  `json:"index"`
	BaseBranch          string `json:"base_branch"`
	HeadBranch          string `json:"head_branch"`
	BaseOID             string `json:"base_oid"`
	HeadOID             string `json:"head_oid"`
	MergeBaseOID        string `json:"merge_base_oid"`
	Title               string `json:"title"`
	BodyDigest          string `json:"body_digest"`
	ContentVersion      int64  `json:"content_version"`
	AllowMaintainerEdit bool   `json:"allow_maintainer_edit"`
	IntentDigest        string `json:"intent_digest"`
}

func ValidHumanPullCreateReceipt(r HumanPullCreateReceipt, q HumanPullCreateRequest) bool {
	p := q.Expected
	_, digest, err := CanonicalHumanPullCreateIntent(q)
	body := sha256.Sum256([]byte(q.Body))
	return err == nil && r.IntentDigest == digest && strconv.FormatInt(r.RepositoryID, 10) == p.RepositoryID && strconv.FormatInt(r.HeadRepositoryID, 10) == p.HeadRepositoryID && strconv.FormatInt(r.ActorID, 10) == p.ActorID &&
		r.PullID > 0 && r.IssueID > 0 && r.Index > 0 && r.BaseBranch == p.BaseBranch && r.HeadBranch == p.HeadBranch && r.BaseOID == p.BaseOID && r.HeadOID == p.HeadOID &&
		ValidPullDiffCommit(r.MergeBaseOID) && len(r.MergeBaseOID) == len(p.HeadOID) && r.Title == q.Title && r.BodyDigest == hex.EncodeToString(body[:]) && r.ContentVersion == 0 && r.AllowMaintainerEdit == q.AllowMaintainerEdit
}

func ValidHumanPullCreateInstallationID(installation string) bool {
	return validInstallationID(installation)
}

func (a Authority) SubmitHumanPullCreate(ctx context.Context, installation string, q HumanPullCreateRequest) (OperationRecord, error) {
	if !ValidHumanPullCreateInstallationID(installation) || !ValidHumanPullCreateRequest(q) || q.Expected.ActorID != a.Actor.ID {
		return OperationRecord{}, ErrHumanPullCreateQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanPullCreateSubmit, RepositoryID: q.Expected.RepositoryID, HumanPullCreate: &q, PullCreateInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanPullCreate.Record, nil
}

// Lookup reports the live installation even for absence. Compare it with the
// saved namespace before interpreting a retained command's outcome.
func (a Authority) GetHumanPullCreate(ctx context.Context, repository, operation string) (OperationLookup, error) {
	if !ValidHumanPullCreateOperationID(operation, a.Actor.ID, repository) {
		return OperationLookup{}, ErrHumanPullCreateQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanPullCreateGet, RepositoryID: repository, PullCreateOperationID: operation})
	if err != nil {
		return OperationLookup{}, err
	}
	return *r.HumanPullCreate, nil
}

func (a Authority) CancelHumanPullCreate(ctx context.Context, installation, repository, operation string) (OperationRecord, error) {
	if !ValidHumanPullCreateInstallationID(installation) || !ValidHumanPullCreateOperationID(operation, a.Actor.ID, repository) {
		return OperationRecord{}, ErrHumanPullCreateQuery
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationHumanPullCreateCancel, RepositoryID: repository, PullCreateOperationID: operation, PullCreateInstallationID: installation})
	if err != nil {
		return OperationRecord{}, err
	}
	return *r.HumanPullCreate.Record, nil
}

func validHumanPullCreateResult(result *OperationLookup, request CallbackRequest) bool {
	id := request.PullCreateOperationID
	if request.HumanPullCreate != nil {
		id = request.HumanPullCreate.OperationID
	}
	r, valid := validHumanOperationResult(result, request, humanOperationResult{
		operationID: id, installationID: request.PullCreateInstallationID,
		kind: OperationKindHumanPullCreate, getOperation: OperationHumanPullCreateGet,
		strictDigest: true, allowIntervention: true,
	}, request.HumanPullCreate, CanonicalHumanPullCreateIntent)
	if !valid || r == nil {
		return valid
	}

	var receipt HumanPullCreateReceipt
	if json.Unmarshal(r.Receipt, &receipt) != nil || strconv.FormatInt(receipt.RepositoryID, 10) != request.RepositoryID || strconv.FormatInt(receipt.ActorID, 10) != request.Authority.Actor.ID || receipt.HeadRepositoryID < 1 || receipt.PullID < 1 || receipt.IssueID < 1 || receipt.Index < 1 || receipt.ContentVersion != 0 || receipt.IntentDigest != r.IntentDigest || !validHumanReviewDigest(receipt.BodyDigest) ||
		!ValidRepositoryPullCreateSnapshotRequest(RepositoryPullCreateSnapshotRequest{HeadRepositoryID: strconv.FormatInt(receipt.HeadRepositoryID, 10), BaseBranch: receipt.BaseBranch, HeadBranch: receipt.HeadBranch}) || receipt.RepositoryID == receipt.HeadRepositoryID && receipt.BaseBranch == receipt.HeadBranch ||
		!ValidPullDiffCommit(receipt.BaseOID) || !ValidPullDiffCommit(receipt.HeadOID) || !ValidPullDiffCommit(receipt.MergeBaseOID) || len(receipt.BaseOID) != len(receipt.HeadOID) || len(receipt.MergeBaseOID) != len(receipt.HeadOID) || receipt.Title == "" || strings.TrimSpace(receipt.Title) != receipt.Title || len(receipt.Title) > MaxHumanPullCreateTitleBytes || !utf8.ValidString(receipt.Title) || strings.ContainsRune(receipt.Title, 0) {
		return false
	}
	raw, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(raw, r.Receipt) {
		return false
	}
	if q := request.HumanPullCreate; q != nil {
		return ValidHumanPullCreateReceipt(receipt, *q)
	}
	return true
}

func humanPullCreateCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		badRequest:  ErrHumanPullCreateQuery,
		admission:   ErrHumanPullCreateAdmission,
		notFound:    ErrRepositoryNotVisible,
		conflict:    ErrHumanPullCreateConflict,
		unavailable: ErrHumanPullCreateUnavailable,
	}).forStatus(status)
}

package nativeoperation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json" //nolint:depguard
	"errors"
	"strconv"
	"strings"
	"time"

	sdk "forgejo.org/extension-sdk"
	extensionauth "forgejo.org/models/extensionauth"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
)

func intakeSignature(claims sdk.IssueIntakeClaims) string {
	raw, _ := json.Marshal(claims)
	mac := hmac.New(sha256.New, []byte(setting.SecretKey))
	_, _ = mac.Write([]byte("native-issue-intake-v1\x00"))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func checkIntakeAuthority(ctx context.Context, claims sdk.IssueIntakeClaims, now int64) error {
	if setting.SecretKey == "" || claims.Installation == "" || claims.Expires <= now || !sdk.ValidIssueIntakeRequest(sdk.IssueIntakeAuthorizationRequest{Source: claims.Source, Revision: claims.Revision, Digest: claims.Digest}) {
		return ErrAuthorityLost
	}
	actorID, err := strconv.ParseInt(claims.Actor, 10, 64)
	if err != nil || actorID <= 0 {
		return ErrAuthorityLost
	}
	repositoryID, err := strconv.ParseInt(claims.Repository, 10, 64)
	if err != nil || repositoryID <= 0 {
		return ErrAuthorityLost
	}
	actor, err := user_model.GetUserByID(ctx, actorID)
	if err != nil || !actor.IsActive || actor.ProhibitLogin {
		return ErrAuthorityLost
	}
	repository, err := repo_model.GetRepositoryByID(ctx, repositoryID)
	if err != nil || repository.IsArchived || repository.IsMirror || user_model.IsBlocked(ctx, repository.OwnerID, actor.ID) {
		return ErrAuthorityLost
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repository, actor)
	if err != nil || !permission.CanWrite(unit.TypeIssues) || !permission.CanWrite(unit.TypeCode) {
		return ErrAuthorityLost
	}
	return nil
}

// MintIssueIntakeGrant is called only after a live native POST admission has
// resolved its human, repository and installation. A retained grant authorizes
// 16 issue slots and 32 conditional refinements, never execution or Git writes.
func MintIssueIntakeGrant(ctx context.Context, claims sdk.IssueIntakeClaims) (sdk.IssueIntakeGrant, error) {
	claims.Expires = time.Now().Add(time.Hour).Unix()
	if err := checkIntakeAuthority(ctx, claims, time.Now().Unix()); err != nil {
		return sdk.IssueIntakeGrant{}, err
	}
	raw, _ := json.Marshal(claims)
	return sdk.IssueIntakeGrant{Claims: claims, Token: base64.RawURLEncoding.EncodeToString(raw) + "." + intakeSignature(claims)}, nil
}

func verifiedIntakeClaims(ctx context.Context, installation, token string, now int64) (sdk.IssueIntakeClaims, error) {
	var claims sdk.IssueIntakeClaims
	encoded, signature, ok := strings.Cut(token, ".")
	if !ok || len(token) > 4096 {
		return claims, ErrAuthorityLost
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || json.Unmarshal(raw, &claims) != nil || claims.Installation != installation || !hmac.Equal([]byte(signature), []byte(intakeSignature(claims))) {
		return claims, ErrAuthorityLost
	}
	return claims, checkIntakeAuthority(ctx, claims, now)
}

func intakeOperationID(claims sdk.IssueIntakeClaims, action string, slot, step int) string {
	raw := claims.Source + ":" + strconv.FormatInt(claims.Revision, 10) + ":" + action + ":"
	if action == "create" {
		raw += strconv.Itoa(slot)
	} else {
		raw += strconv.Itoa(step)
	}
	sum := sha256.Sum256([]byte(raw))
	return "issue-intake-" + hex.EncodeToString(sum[:16])
}

func intakeAttemptID(base string, attempt int) string {
	if attempt == 0 {
		return base
	}
	return base + ".a" + strconv.Itoa(attempt)
}

func committedIntakeCreation(ctx context.Context, installation string, claims sdk.IssueIntakeClaims, slot int) (*nativeoperation_model.Operation, error) {
	for attempt := 0; attempt < 6; attempt++ {
		op, err := nativeoperation_model.LookupOperation(ctx, installation, intakeAttemptID(intakeOperationID(claims, "create", slot, 0), attempt))
		if err != nil {
			return nil, err
		}
		if op == nil {
			return nil, ErrInvalidIntent
		}
		if op.EffectState == sdk.EffectCommitted {
			return op, nil
		}
		if op.EffectState != sdk.EffectNotCommitted {
			return nil, ErrBusy
		}
	}
	return nil, ErrInvalidIntent
}

func storedIntakeGraph(op *nativeoperation_model.Operation) (sdk.IssueGraphPayload, error) {
	var envelope struct {
		Payload sdk.IssueGraphPayload `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &envelope) != nil || envelope.Payload.Intake == nil {
		return sdk.IssueGraphPayload{}, ErrInvalidIntent
	}
	return envelope.Payload, nil
}

func intakeReceipt(record sdk.OperationRecord, request sdk.IssueIntakeMutation) (sdk.IssueIntakeReceipt, error) {
	if record.EffectState != sdk.EffectCommitted {
		return sdk.IssueIntakeReceipt{}, errors.New("native issue intake did not commit: " + record.ReasonCode)
	}
	var receipt sdk.IssueGraphReceipt
	if json.Unmarshal(record.Receipt, &receipt) != nil || len(receipt.Issues) != 1 {
		return sdk.IssueIntakeReceipt{}, ErrInvalidIntent
	}
	issue := receipt.Issues[0]
	digest := sha256.Sum256([]byte(request.Body))
	return sdk.IssueIntakeReceipt{Index: issue.IssueIndex, IssueID: issue.IssueID, Slot: request.Slot, Title: request.Title, BodyDigest: hex.EncodeToString(digest[:])}, nil
}

// SubmitIssueIntake executes the same native conditional issue transaction as
// graph writes, with a narrower live-session grant instead of a Factory PAT.
func (s *Service) SubmitIssueIntake(ctx context.Context, installation string, request sdk.IssueIntakeMutation) (sdk.IssueIntakeReceipt, error) {
	if !sdk.ValidIssueIntakeMutation(request) {
		return sdk.IssueIntakeReceipt{}, ErrInvalidIntent
	}
	claims, err := verifiedIntakeClaims(ctx, installation, request.Grant, s.clock())
	if err != nil {
		return sdk.IssueIntakeReceipt{}, err
	}
	request.Grant = ""
	raw, _ := json.Marshal(request)
	sum := sha256.Sum256(raw)
	requestHash := hex.EncodeToString(sum[:])
	base := intakeOperationID(claims, request.Action, request.Slot, request.Step)
	opID := ""
	for attempt := 0; attempt < 6; attempt++ {
		candidate := intakeAttemptID(base, attempt)
		existing, err := nativeoperation_model.LookupOperation(ctx, installation, candidate)
		if err != nil {
			return sdk.IssueIntakeReceipt{}, err
		}
		if existing == nil {
			opID = candidate
			break
		}
		graph, err := storedIntakeGraph(existing)
		if err != nil || graph.Intake.RequestHash != requestHash || graph.Intake.Claims.Source != claims.Source || graph.Intake.Claims.Revision != claims.Revision || graph.Intake.Claims.Digest != claims.Digest || graph.Intake.Claims.Actor != claims.Actor || graph.Intake.Claims.Repository != claims.Repository {
			return sdk.IssueIntakeReceipt{}, ErrInvalidIntent
		}
		if existing.EffectState == sdk.EffectCommitted {
			return intakeReceipt(ToRecord(existing), request)
		}
		if existing.EffectState != sdk.EffectNotCommitted {
			return sdk.IssueIntakeReceipt{}, ErrBusy
		}
	}
	if opID == "" {
		return sdk.IssueIntakeReceipt{}, errors.New("native issue intake attempts exhausted")
	}
	graph := sdk.IssueGraphPayload{Intake: &sdk.IssueIntakeAuthorization{Claims: claims, RequestHash: requestHash, Slot: request.Slot, Step: request.Step}}
	if request.Action == "create" {
		graph.Creates = []sdk.IssueGraphCreate{{Key: strconv.Itoa(request.Slot), Title: request.Title, Body: request.Body}}
	} else {
		creation, err := committedIntakeCreation(ctx, installation, claims, request.Slot)
		if err != nil {
			return sdk.IssueIntakeReceipt{}, ErrInvalidIntent
		}
		createdGraph, err := storedIntakeGraph(creation)
		if err != nil || createdGraph.Intake.Claims.Actor != claims.Actor || createdGraph.Intake.Claims.Repository != claims.Repository || createdGraph.Intake.Claims.Digest != claims.Digest {
			return sdk.IssueIntakeReceipt{}, ErrAuthorityLost
		}
		var receipt sdk.IssueGraphReceipt
		if json.Unmarshal([]byte(creation.Receipt), &receipt) != nil || len(receipt.Issues) != 1 {
			return sdk.IssueIntakeReceipt{}, ErrInvalidIntent
		}
		issue, err := issues_model.GetIssueByID(ctx, receipt.Issues[0].IssueID)
		if err != nil || issue.IsClosed || issue.IsPull || issue.RepoID != creation.RepositoryID {
			return sdk.IssueIntakeReceipt{}, ErrInvalidIntent
		}
		digest := sha256.Sum256([]byte(issue.Content))
		if issue.Title != request.ExpectedTitle || hex.EncodeToString(digest[:]) != request.ExpectedBodyDigest {
			return sdk.IssueIntakeReceipt{}, errors.New("issue changed since intake; preserving current content")
		}
		graph.Edits = []sdk.IssueGraphEdit{{IssueID: issue.ID, ExpectedTitle: issue.Title, ExpectedBody: issue.Content, ExpectedContentVersion: issue.ContentVersion, Title: request.Title, Body: request.Body}}
	}
	observation, err := s.ReadNativeRevision(ctx)
	if err != nil {
		return sdk.IssueIntakeReceipt{}, err
	}
	if !observation.Idle {
		return sdk.IssueIntakeReceipt{}, ErrBusy
	}
	payload, _ := json.Marshal(graph)
	actor, _ := strconv.ParseInt(claims.Actor, 10, 64)
	repository, _ := strconv.ParseInt(claims.Repository, 10, 64)
	intent, err := ValidateIntent(opID, actor, repository, nativeoperation_model.KindIssueGraph, "issue-intake/"+claims.Source+"/"+strconv.FormatInt(claims.Revision, 10), observation.Revision, min(claims.Expires, s.clock()+300), payload, s.clock())
	if err != nil {
		return sdk.IssueIntakeReceipt{}, err
	}
	record, err := s.Submit(ctx, extensionauth.SubmissionDecision{ActorID: actor, CredentialFingerprint: intakeSignature(claims)}, installation, intent)
	if err != nil {
		return sdk.IssueIntakeReceipt{}, err
	}
	return intakeReceipt(record, request)
}

func (s *Service) revalidateIssueIntake(ctx context.Context, op *nativeoperation_model.Operation) error {
	graph, err := storedIntakeGraph(op)
	if err != nil {
		return ErrAuthorityLost
	}
	claims := graph.Intake.Claims
	if claims.Installation != op.InstallationID || claims.Actor != strconv.FormatInt(op.ActorID, 10) || claims.Repository != strconv.FormatInt(op.RepositoryID, 10) || !hmac.Equal([]byte(op.CredentialFingerprint), []byte(intakeSignature(claims))) {
		return ErrAuthorityLost
	}
	return checkIntakeAuthority(ctx, claims, s.clock())
}

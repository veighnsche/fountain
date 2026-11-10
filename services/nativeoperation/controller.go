// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	sdk "forgejo.org/extension-sdk"
	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/desktopgrant"
	"forgejo.org/models/extensionauth"
	native_model "forgejo.org/models/nativeoperation"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
)

const kindRuntimeSelection = "runtime.controller.select"

// desktopControllerAuthority is shared by selection and grant issuance. It
// reads current host authority; an enrollment or a cached repository response
// alone cannot select a controller or authorize an operation.
func desktopControllerAuthority(ctx context.Context, enrollment string, admin bool) (*desktopgrant.Grant, *auth_model.OAuth2Grant, error) {
	g, err := desktopgrant.Get(ctx, enrollment)
	if err != nil {
		return nil, nil, err
	}
	if g.Revoked || g.ProofSHA256 == "" || !setting.FactoryControlConfigured() {
		return nil, nil, ErrAuthorityLost
	}
	authorization, err := auth_model.GetOAuth2GrantByID(ctx, g.OAuthGrantID)
	if err != nil || authorization == nil || authorization.UserID != g.ActorID {
		return nil, nil, ErrAuthorityLost
	}
	app, err := auth_model.GetOAuth2ApplicationByID(ctx, authorization.ApplicationID)
	if err != nil || app == nil || app.ConfidentialClient || setting.OAuth2.FactoryDesktopControlClientID == "" || app.ClientID != setting.OAuth2.FactoryDesktopControlClientID {
		return nil, nil, ErrAuthorityLost
	}
	// OAuth scopes are space-delimited; the PAT bitmap parser takes commas.
	if !authorization.ScopeContains(string(auth_model.AccessTokenScopeWriteRepository)) && !authorization.ScopeContains(string(auth_model.AccessTokenScopeAll)) {
		return nil, nil, ErrAuthorityLost
	}
	actor, err := user_model.GetUserByID(ctx, g.ActorID)
	if err != nil || !actor.IsActive || actor.ProhibitLogin {
		return nil, nil, ErrAuthorityLost
	}
	repository, err := repo_model.GetRepositoryByID(ctx, g.RepositoryID)
	if err != nil || repository.IsArchived || repository.IsMirror || user_model.IsBlocked(ctx, repository.OwnerID, actor.ID) {
		return nil, nil, ErrAuthorityLost
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repository, actor)
	if err != nil || !permission.CanWrite(unit.TypeCode) || admin && !permission.IsAdmin() {
		return nil, nil, ErrAuthorityLost
	}
	return g, authorization, nil
}

// controllerSettled never treats expiry or a missing provider reply as
// retirement. Legacy background operations with unknown package provenance
// must settle before activating a central controller too.
func controllerSettled(ctx context.Context, repository int64) (bool, error) {
	claims, err := db.GetEngine(ctx).Where("repository_id=? AND state=?", repository, desktopgrant.ClaimHeld).Exist(new(desktopgrant.ExecutionClaim))
	if err != nil || claims {
		return false, err
	}
	operations, err := db.GetEngine(ctx).Where("repository_id=? AND submitted=? AND (controller_grant_id<>? OR (token_id>0 AND (package_id=? OR package_id=?))) AND (effect_state NOT IN (?,?) OR (effect_state=? AND completion<>?))",
		repository, true, "", setting.OAuth2.FactoryAppliancePackageID, "", native_model.EffectCommitted, native_model.EffectNotCommitted, native_model.EffectCommitted, native_model.CompletionComplete).Exist(new(native_model.Operation))
	return !operations, err
}

// SelectDesktopRuntime records the exact command and resulting placement in
// the existing operation journal, in the same transaction as the CAS. It never
// takes the global native mutation reservation for a controller lifetime.
func (s *Service) SelectDesktopRuntime(ctx context.Context, enrollment string, request sdk.RuntimeSelection) (sdk.RuntimeSelectionReceipt, error) {
	var receipt sdk.RuntimeSelectionReceipt
	if !validHex(request.CommandID, 16) || (request.Action != "activate" && request.Action != "withdraw" && request.Action != "retire") {
		return receipt, ErrInvalidIntent
	}
	g, _, err := desktopControllerAuthority(ctx, enrollment, true)
	if err != nil {
		return receipt, err
	}
	raw, err := json.Marshal(struct {
		Enrollment string
		Request    sdk.RuntimeSelection
	}{enrollment, request})
	if err != nil {
		return receipt, err
	}
	return s.selectRuntime(ctx, controllerSelection{ActorID: g.ActorID, RepositoryID: g.RepositoryID, RuntimeID: g.RuntimeID, OwnerKind: desktopgrant.ControllerDesktop, OwnerID: g.ID, InstallationID: desktopgrant.DesktopInstallation(enrollment)}, request, raw, func(ctx context.Context) error {
		_, _, err := desktopControllerAuthority(ctx, enrollment, true)
		return err
	})
}

type controllerSelection struct {
	ActorID, RepositoryID                         int64
	RuntimeID, OwnerKind, OwnerID, InstallationID string
}

func (s *Service) selectRuntime(ctx context.Context, principal controllerSelection, request sdk.RuntimeSelection, raw []byte, admit func(context.Context) error) (sdk.RuntimeSelectionReceipt, error) {
	var receipt sdk.RuntimeSelectionReceipt
	if !validHex(request.CommandID, 16) || !validHex(principal.RuntimeID, 16) || (request.Action != "activate" && request.Action != "withdraw" && request.Action != "retire") {
		return receipt, ErrInvalidIntent
	}
	digest := sha256.Sum256(raw)
	installation := principal.InstallationID
	err := desktopgrant.WithPlacement(ctx, principal.RepositoryID, func(ctx context.Context, p *desktopgrant.Placement) error {
		old, err := native_model.LookupOperation(ctx, installation, request.CommandID)
		if err != nil {
			return err
		}
		if old != nil {
			if !old.Submitted || old.Kind != kindRuntimeSelection || old.IntentDigest != hex.EncodeToString(digest[:]) {
				return ErrIntentConflict
			}
			return json.Unmarshal([]byte(old.Receipt), &receipt)
		}
		if err := admit(ctx); err != nil {
			return err
		}
		record := func(applied bool, reason string) error {
			outcome, effect := "refused", native_model.EffectNotCommitted
			if applied {
				outcome, effect = "applied", native_model.EffectCommitted
			}
			receipt = sdk.RuntimeSelectionReceipt{CommandID: request.CommandID, Outcome: outcome, ReasonCode: reason, Placement: p.Projection()}
			body, err := json.Marshal(receipt)
			if err != nil {
				return err
			}
			return db.Insert(ctx, &native_model.Operation{InstallationID: installation, OperationID: request.CommandID, Kind: kindRuntimeSelection,
				ActorID: principal.ActorID, RepositoryID: principal.RepositoryID, NotAfter: request.NotAfter, IntentDigest: hex.EncodeToString(digest[:]), Intent: string(raw),
				Submitted: true, Admitted: applied, EffectState: effect, Reason: reason, Completion: native_model.CompletionComplete, Cancellation: native_model.CancellationNone, Receipt: string(body)})
		}
		if request.NotAfter <= s.clock() || request.NotAfter > s.clock()+300 || request.ExpectedRevision != p.Revision {
			return record(false, "expired_or_stale_controller")
		}
		switch request.Action {
		case "activate":
			if p.State != desktopgrant.ControllerInactive {
				return record(false, "controller_already_selected")
			}
			settled, err := controllerSettled(ctx, principal.RepositoryID)
			if err != nil {
				return err
			}
			if !settled {
				return record(false, "prior_controller_unsettled")
			}
			p.RuntimeID, p.OwnerKind, p.OwnerID = principal.RuntimeID, principal.OwnerKind, principal.OwnerID
			p.Generation++
			p.State = desktopgrant.ControllerActive
		case "withdraw":
			if p.State != desktopgrant.ControllerActive {
				return record(false, "controller_not_active")
			}
			p.State = desktopgrant.ControllerRetiring
		case "retire":
			if p.State != desktopgrant.ControllerRetiring {
				return record(false, "controller_not_retiring")
			}
			settled, err := controllerSettled(ctx, principal.RepositoryID)
			if err != nil {
				return err
			}
			if !settled {
				return record(false, "prior_controller_unsettled")
			}
			p.State = desktopgrant.ControllerInactive
		}
		p.SelectedBy = principal.ActorID
		if err := desktopgrant.SavePlacement(ctx, p); err != nil {
			return err
		}
		return record(true, "")
	})
	return receipt, err
}

func validHex(value string, bytes int) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == bytes && strings.ToLower(value) == value
}

// SelectApplianceRuntime is invoked only by the live human SDK admission.
// Installation/package and actor are resolved by that adapter, never from the
// browser body. A background PAT cannot select its own controller.
func (s *Service) SelectApplianceRuntime(ctx context.Context, installation, packageID, runtime string, actorID, repositoryID int64, request sdk.RuntimeSelection, admit func(context.Context) error) (sdk.RuntimeSelectionReceipt, error) {
	var empty sdk.RuntimeSelectionReceipt
	if admit == nil || packageID == "" || packageID != setting.OAuth2.FactoryAppliancePackageID || !sdk.ValidHumanCommentInstallationID(installation) || runtime != desktopgrant.ApplianceRuntime(installation) {
		return empty, ErrAuthorityLost
	}
	check := func(ctx context.Context) error {
		if err := admit(ctx); err != nil {
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
		if err != nil || !permission.IsAdmin() || !permission.CanWrite(unit.TypeCode) {
			return ErrAuthorityLost
		}
		return nil
	}
	if err := check(ctx); err != nil {
		return empty, err
	}
	raw, err := json.Marshal(struct {
		Installation, Package, Runtime string
		Actor, Repository              int64
		Request                        sdk.RuntimeSelection
	}{installation, packageID, runtime, actorID, repositoryID, request})
	if err != nil {
		return empty, err
	}
	return s.selectRuntime(ctx, controllerSelection{ActorID: actorID, RepositoryID: repositoryID, RuntimeID: runtime, OwnerKind: desktopgrant.ControllerAppliance, OwnerID: installation, InstallationID: installation}, request, raw, check)
}

// AuthorizeDesktopOperations is separate from read enrollment and runtime
// selection. Identical command replay returns the original immutable scope;
// neither replay nor refresh can extend its deadline or reactivate a revocation.
func (s *Service) AuthorizeDesktopOperations(ctx context.Context, enrollment string, request sdk.DesktopOperationAuthorization) (*desktopgrant.OperationGrant, error) {
	if !validHex(request.CommandID, 16) || len(request.Operations) == 0 || len(request.Operations) > 10 {
		return nil, ErrInvalidIntent
	}
	operations := slices.Clone(request.Operations)
	slices.Sort(operations)
	for i, kind := range operations {
		if !native_model.ValidKind(kind) && kind != desktopgrant.ExecutionLaunch || i > 0 && kind == operations[i-1] {
			return nil, ErrInvalidIntent
		}
	}
	g, authorization, err := desktopControllerAuthority(ctx, enrollment, false)
	if err != nil {
		return nil, err
	}
	requested := &desktopgrant.OperationGrant{ID: request.CommandID, CredentialKind: desktopgrant.CredentialOAuth, EnrollmentID: enrollment,
		OAuthGrantID: g.OAuthGrantID, ApplicationID: authorization.ApplicationID, InstallationID: desktopgrant.DesktopInstallation(enrollment),
		ActorID: g.ActorID, RepositoryID: g.RepositoryID, RuntimeID: g.RuntimeID, Generation: request.Generation, SelectionRevision: request.ExpectedPlacement,
		ProofSHA256: g.ProofSHA256, Operations: strings.Join(operations, ","), NotAfter: request.NotAfter}
	var result *desktopgrant.OperationGrant
	err = desktopgrant.WithPlacement(ctx, g.RepositoryID, func(ctx context.Context, p *desktopgrant.Placement) error {
		old, err := desktopgrant.GetOperationGrant(ctx, request.CommandID)
		if err == nil {
			if old.Fingerprint() != requested.Fingerprint() {
				return ErrIntentConflict
			}
			result = old
			return nil
		}
		if err != desktopgrant.ErrNotFound {
			return err
		}
		if _, _, err := desktopControllerAuthority(ctx, enrollment, false); err != nil {
			return err
		}
		if p.State != desktopgrant.ControllerActive || p.OwnerKind != desktopgrant.ControllerDesktop || p.OwnerID != enrollment || p.RuntimeID != g.RuntimeID ||
			p.Revision != request.ExpectedPlacement || p.Generation != request.Generation || request.NotAfter <= s.clock() || request.NotAfter > s.clock()+int64(MaxOperationLifetime.Seconds()) {
			return desktopgrant.ErrController
		}
		result, err = desktopgrant.EnrollOperationGrant(ctx, requested)
		return err
	})
	return result, err
}

// AuthorizeApplianceOperations is separate human consent for one previously
// enrolled role credential. The browser supplies IDs, never the credential.
// Current token, binding and permissions are checked inside the placement gate;
// transaction rollback removes a grant if any requested operation is refused.
func (s *Service) AuthorizeApplianceOperations(ctx context.Context, installation, packageID string, repository, humanID int64, q sdk.ApplianceOperationAuthorization, admit func(context.Context) error) (*desktopgrant.OperationGrant, error) {
	if humanID <= 0 || !sdk.ValidApplianceOperationAuthorization(q) || q.InstallationID != installation || q.RuntimeID != desktopgrant.ApplianceRuntime(installation) || packageID == "" || packageID != setting.OAuth2.FactoryAppliancePackageID || admit == nil {
		return nil, ErrInvalidIntent
	}
	operations := slices.Clone(q.Operations)
	slices.Sort(operations)
	for i, kind := range operations {
		if !extensionauth.ValidKind(kind) || i > 0 && kind == operations[i-1] {
			return nil, ErrInvalidIntent
		}
	}
	tokenID, _ := strconv.ParseInt(q.TokenID, 10, 64)
	actorID, _ := strconv.ParseInt(q.ActorID, 10, 64)
	var result *desktopgrant.OperationGrant
	err := desktopgrant.WithPlacement(ctx, repository, func(ctx context.Context, p *desktopgrant.Placement) error {
		if err := admit(ctx); err != nil {
			return ErrAuthorityLost
		}
		requested := &desktopgrant.OperationGrant{ID: q.CommandID, CredentialKind: desktopgrant.CredentialPAT, TokenID: tokenID, InstallationID: installation, PackageID: packageID, ActorID: actorID, RepositoryID: repository, RuntimeID: q.RuntimeID, Generation: q.Generation, SelectionRevision: q.ExpectedPlacement, Operations: strings.Join(operations, ","), NotAfter: q.NotAfter}
		old, err := desktopgrant.GetOperationGrant(ctx, q.CommandID)
		if err == nil {
			// History adoption uses original provenance, even after token removal.
			// It never re-enrolls or renews the original grant.
			requested.TokenFingerprint = old.TokenFingerprint
			if old.Fingerprint() != requested.Fingerprint() {
				return ErrIntentConflict
			}
			result = old
			return nil
		}
		if err != desktopgrant.ErrNotFound {
			return err
		}
		token := new(auth_model.AccessToken)
		found, err := db.GetEngine(ctx).ID(tokenID).NoAutoCondition().Get(token)
		if err != nil {
			return err
		}
		if !found || token.UID != actorID {
			return ErrAuthorityLost
		}
		requested.TokenFingerprint = extensionauth.CredentialFingerprint(token.TokenHash, token.TokenSalt)
		if p.State != desktopgrant.ControllerActive || p.OwnerKind != desktopgrant.ControllerAppliance || p.OwnerID != installation || p.RuntimeID != q.RuntimeID || p.Generation != q.Generation || p.Revision != q.ExpectedPlacement || q.NotAfter <= s.clock() || q.NotAfter > s.clock()+int64(MaxOperationLifetime.Seconds()) {
			return ErrAuthorityLost
		}
		result, err = desktopgrant.EnrollOperationGrant(ctx, requested)
		if err != nil {
			return err
		}
		for _, kind := range operations {
			if _, err := extensionauth.CheckControllerUse(ctx, extensionauth.ControllerUse{GrantID: result.ID, InstallationID: installation, PackageID: packageID, ActorID: actorID, RepositoryID: repository, TokenID: tokenID, CredentialFingerprint: result.TokenFingerprint, Kind: kind}); err != nil {
				return ErrAuthorityLost
			}
		}
		raw, err := json.Marshal(q)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		body, err := json.Marshal(struct {
			GrantID string `json:"grant_id"`
		}{result.ID})
		if err != nil {
			return err
		}
		return db.Insert(ctx, &native_model.Operation{InstallationID: installation, OperationID: q.CommandID, Kind: "runtime.controller.authorize", ActorID: humanID, RepositoryID: repository, PackageID: packageID, ControllerGrantID: result.ID, NotAfter: q.NotAfter, IntentDigest: hex.EncodeToString(digest[:]), Intent: string(raw), Submitted: true, Admitted: true, EffectState: native_model.EffectCommitted, Completion: native_model.CompletionComplete, Cancellation: native_model.CancellationNone, Receipt: string(body)})
	})
	return result, err
}

func ClaimProjection(c *desktopgrant.ExecutionClaim, acquired bool) sdk.ExecutionClaimReceipt {
	return sdk.ExecutionClaimReceipt{CommandID: c.ID, GrantID: c.GrantID, ManifestDigest: c.ManifestDigest, NotAfter: c.NotAfter, State: c.State, RetirementDigest: c.RetirementDigest, Acquired: acquired}
}

func (s *Service) ClaimDesktopExecution(ctx context.Context, grant *desktopgrant.OperationGrant, request sdk.ExecutionClaimRequest) (sdk.ExecutionClaimReceipt, error) {
	return s.claimExecution(ctx, grant, request, extensionauth.ControllerUse{GrantID: grant.ID, InstallationID: grant.InstallationID, ActorID: grant.ActorID, RepositoryID: grant.RepositoryID, CredentialFingerprint: grant.Fingerprint(), Kind: desktopgrant.ExecutionLaunch, NotAfter: request.NotAfter})
}

func (s *Service) ClaimApplianceExecution(ctx context.Context, grant *desktopgrant.OperationGrant, decision extensionauth.SubmissionDecision, request sdk.ExecutionClaimRequest) (sdk.ExecutionClaimReceipt, error) {
	if grant.CredentialKind != desktopgrant.CredentialPAT || decision.ControllerGrantID != grant.ID {
		return sdk.ExecutionClaimReceipt{}, ErrAuthorityLost
	}
	return s.claimExecution(ctx, grant, request, extensionauth.ControllerUse{GrantID: grant.ID, InstallationID: grant.InstallationID, PackageID: decision.PackageID, ActorID: grant.ActorID, RepositoryID: grant.RepositoryID, TokenID: decision.TokenID, CredentialFingerprint: decision.CredentialFingerprint, Kind: desktopgrant.ExecutionLaunch, NotAfter: request.NotAfter})
}

func (s *Service) claimExecution(ctx context.Context, grant *desktopgrant.OperationGrant, request sdk.ExecutionClaimRequest, use extensionauth.ControllerUse) (sdk.ExecutionClaimReceipt, error) {
	var receipt sdk.ExecutionClaimReceipt
	if !validHex(request.CommandID, 16) || !validHex(request.ManifestDigest, 32) || request.NotAfter <= 0 {
		return receipt, ErrInvalidIntent
	}
	err := desktopgrant.WithPlacement(ctx, grant.RepositoryID, func(ctx context.Context, _ *desktopgrant.Placement) error {
		old, err := desktopgrant.GetExecutionClaim(ctx, request.CommandID)
		if err == nil {
			if old.GrantID != grant.ID || old.ManifestDigest != request.ManifestDigest || old.NotAfter != request.NotAfter {
				return ErrIntentConflict
			}
			receipt = ClaimProjection(old, false)
			return nil
		}
		if err != desktopgrant.ErrNotFound {
			return err
		}
		if request.NotAfter <= s.clock() {
			return ErrAuthorityLost
		}
		if _, err := extensionauth.CheckControllerUse(ctx, use); err != nil {
			return ErrAuthorityLost
		}
		claim, acquired, err := desktopgrant.RecordExecutionClaim(ctx, &desktopgrant.ExecutionClaim{ID: request.CommandID, GrantID: grant.ID, InstallationID: grant.InstallationID,
			RepositoryID: grant.RepositoryID, ActorID: grant.ActorID, RuntimeID: grant.RuntimeID, Generation: grant.Generation, ManifestDigest: request.ManifestDigest, NotAfter: request.NotAfter, State: desktopgrant.ClaimHeld})
		if err == nil {
			receipt = ClaimProjection(claim, acquired)
		}
		return err
	})
	return receipt, err
}

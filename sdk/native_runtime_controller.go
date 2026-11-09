// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"slices"
	"strings"
)

func sameRuntimeOperations(a, b []string) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

const (
	CapabilityRuntimeControl            = "native.repository.runtime.control"
	OperationRuntimeControllerRead      = "repository.runtime.controller.read"
	OperationRuntimeControllerSelect    = "repository.runtime.controller.select"
	OperationRuntimeControllerAuthorize = "repository.runtime.controller.authorize"
	OperationRuntimeControllerGrant     = "repository.runtime.controller.grant"
)

type ApplianceRuntimeSelection struct {
	RuntimeSelection
	InstallationID string `json:"installation_id"`
	RuntimeID      string `json:"runtime_id"`
}

type RuntimeControllerView struct {
	InstallationID string           `json:"installation_id"`
	RuntimeID      string           `json:"runtime_id"`
	CanSelect      bool             `json:"can_select"`
	Placement      RuntimePlacement `json:"placement"`
	Actors         []RuntimeActor   `json:"actors"`
}

type RuntimeActor struct {
	TokenID    string   `json:"token_id"`
	ActorID    string   `json:"actor_id"`
	Operations []string `json:"operations"`
}

type ApplianceOperationAuthorization struct {
	DesktopOperationAuthorization
	InstallationID string `json:"installation_id"`
	RuntimeID      string `json:"runtime_id"`
	TokenID        string `json:"token_id"`
	ActorID        string `json:"actor_id"`
}

type ApplianceOperationGrant struct {
	ID                string   `json:"id"`
	InstallationID    string   `json:"installation_id"`
	RuntimeID         string   `json:"runtime_id"`
	RepositoryID      string   `json:"repository_id"`
	TokenID           string   `json:"token_id"`
	ActorID           string   `json:"actor_id"`
	Generation        int64    `json:"generation,string"`
	SelectionRevision string   `json:"selection_revision"`
	Operations        []string `json:"operations"`
	NotAfter          int64    `json:"not_after"`
	Revoked           bool     `json:"revoked"`
	Active            bool     `json:"active"`
}

func ValidApplianceOperationAuthorization(q ApplianceOperationAuthorization) bool {
	return validInstallationID(q.InstallationID) && runtimeHex(q.RuntimeID) && runtimeHex(q.CommandID) && runtimeHex(q.ExpectedPlacement) && decimalID(q.TokenID) && decimalID(q.ActorID) && q.Generation > 0 && q.NotAfter > 0 && len(q.Operations) > 0 && len(q.Operations) <= 10
}

func (a Authority) AuthorizeRuntimeOperations(ctx context.Context, repository string, q ApplianceOperationAuthorization) (ApplianceOperationGrant, error) {
	if !decimalID(repository) || !ValidApplianceOperationAuthorization(q) {
		return ApplianceOperationGrant{}, errors.New("invalid runtime authorization")
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRuntimeControllerAuthorize, RepositoryID: repository, RuntimeAuthorization: &q})
	if err != nil {
		return ApplianceOperationGrant{}, err
	}
	return *r.RuntimeGrant, nil
}

func (a Authority) RuntimeOperationGrant(ctx context.Context, repository, grant string) (ApplianceOperationGrant, error) {
	if !decimalID(repository) || !runtimeHex(grant) {
		return ApplianceOperationGrant{}, errors.New("invalid runtime grant reference")
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRuntimeControllerGrant, RepositoryID: repository, RuntimeGrantID: grant})
	if err != nil {
		return ApplianceOperationGrant{}, err
	}
	return *r.RuntimeGrant, nil
}

func runtimeHex(value string) bool {
	if len(value) != 32 {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) < 0
}

func ValidApplianceRuntimeSelection(q ApplianceRuntimeSelection) bool {
	return validInstallationID(q.InstallationID) && runtimeHex(q.RuntimeID) && runtimeHex(q.CommandID) &&
		(q.ExpectedRevision == "" || runtimeHex(q.ExpectedRevision)) && q.NotAfter > 0 && (q.Action == "activate" || q.Action == "withdraw" || q.Action == "retire")
}

func ValidRuntimePlacement(p RuntimePlacement, repository string) bool {
	if p.RepositoryID != repository || !decimalID(repository) || p.Generation < 0 || (p.State != "active" && p.State != "retiring" && p.State != "inactive") {
		return false
	}
	if p.Generation == 0 {
		return p.State == "inactive" && p.RuntimeID == "" && p.OwnerID == "" && p.OwnerKind == "" && p.Revision == ""
	}
	return runtimeHex(p.RuntimeID) && runtimeHex(p.Revision) && (p.OwnerKind == "desktop" && runtimeHex(p.OwnerID) || p.OwnerKind == "appliance" && validInstallationID(p.OwnerID))
}

// Runtime controller operations use live human Authority; BackgroundClient
// intentionally offers no selection method. Read/session admission is not an
// operation grant and cannot be reused as desktop execution authority.
func (a Authority) RuntimeController(ctx context.Context, repository string) (RuntimeControllerView, error) {
	if !decimalID(repository) {
		return RuntimeControllerView{}, errors.New("invalid controller repository")
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRuntimeControllerRead, RepositoryID: repository})
	if err != nil {
		return RuntimeControllerView{}, err
	}
	return *r.RuntimeController, nil
}

func (a Authority) SelectRuntimeController(ctx context.Context, repository string, q ApplianceRuntimeSelection) (RuntimeSelectionReceipt, error) {
	if !decimalID(repository) || !ValidApplianceRuntimeSelection(q) {
		return RuntimeSelectionReceipt{}, errors.New("invalid controller command")
	}
	r, err := a.nativeClient().call(ctx, CallbackRequest{Operation: OperationRuntimeControllerSelect, RepositoryID: repository, RuntimeSelection: &q})
	if err != nil {
		return RuntimeSelectionReceipt{}, err
	}
	return *r.RuntimeSelection, nil
}

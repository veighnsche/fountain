// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"strings"
)

const (
	CapabilityRuntimeControl         = "native.repository.runtime.control"
	OperationRuntimeControllerRead   = "repository.runtime.controller.read"
	OperationRuntimeControllerSelect = "repository.runtime.controller.select"
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

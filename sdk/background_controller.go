// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
)

func ValidControllerReference(r ControllerReference) bool {
	return runtimeHex(r.GrantID) && (r.CommandID == "" || runtimeHex(r.CommandID))
}

const (
	BackgroundControllerGrantPath    = "/v1/background/controller/grant"
	BackgroundControllerClaimPath    = "/v1/background/controller/claim"
	BackgroundControllerClaimGetPath = "/v1/background/controller/claim/get"
	BackgroundControllerRetirePath   = "/v1/background/controller/claim/retire"
)

// ControllerBackground is optional: ordinary background readers/publishers do
// not acquire selection or execution authority by implementing BackgroundClient.
type ControllerBackground interface {
	ReadControllerGrant(context.Context, string) (ApplianceOperationGrant, error)
	ClaimControllerExecution(context.Context, CredentialFile, string, ExecutionClaimRequest) (ExecutionClaimReceipt, error)
	ReadControllerExecution(context.Context, string, string) (ExecutionClaimReceipt, error)
	RetireControllerExecution(context.Context, string, string, ExecutionRetirement) (ExecutionClaimReceipt, error)
}

type ControllerReference struct {
	GrantID   string `json:"grant_id"`
	CommandID string `json:"command_id,omitempty"`
}

type ControllerClaim struct {
	ExecutionClaimRequest
	GrantID string `json:"grant_id"`
	Token   string `json:"token"`
}

type ControllerRetirement struct {
	ControllerReference
	ExecutionRetirement
}

func ValidateControllerGrant(g ApplianceOperationGrant, id, installation string) error {
	if g.ID != id || !runtimeHex(id) || g.InstallationID != installation || !validInstallationID(installation) || !runtimeHex(g.RuntimeID) || !runtimeHex(g.SelectionRevision) || !decimalID(g.RepositoryID) || !decimalID(g.ActorID) || !decimalID(g.TokenID) || g.Generation < 1 || g.NotAfter < 1 || len(g.Operations) < 1 || len(g.Operations) > 10 {
		return errors.New("invalid controller grant response")
	}
	return nil
}

func ValidateControllerClaim(r ExecutionClaimReceipt, ref ControllerReference) error {
	if !ValidControllerReference(ref) || !runtimeHex(ref.CommandID) || r.CommandID != ref.CommandID || r.GrantID != ref.GrantID || !validSHA256Hex(r.ManifestDigest) || r.NotAfter < 1 {
		return errors.New("invalid controller claim response")
	}
	switch r.State {
	case "claimed":
		if r.RetirementDigest != "" {
			return errors.New("claimed execution has retirement")
		}
	case "retired", "not_started":
		if !validSHA256Hex(r.RetirementDigest) || r.Acquired {
			return errors.New("invalid settled claim response")
		}
	default:
		return errors.New("unknown controller claim state")
	}
	return nil
}

func (c *backgroundClient) ReadControllerGrant(ctx context.Context, id string) (ApplianceOperationGrant, error) {
	var g ApplianceOperationGrant
	if !runtimeHex(id) {
		return g, errors.New("invalid controller grant")
	}
	if err := c.post(ctx, BackgroundControllerGrantPath, ControllerReference{GrantID: id}, &g); err != nil {
		return g, err
	}
	return g, ValidateControllerGrant(g, id, c.installation)
}

func (c *backgroundClient) ClaimControllerExecution(ctx context.Context, credential CredentialFile, grant string, q ExecutionClaimRequest) (ExecutionClaimReceipt, error) {
	var r ExecutionClaimReceipt
	ref := ControllerReference{GrantID: grant, CommandID: q.CommandID}
	if !ValidControllerReference(ref) || !runtimeHex(q.CommandID) || !validSHA256Hex(q.ManifestDigest) || q.NotAfter < 1 {
		return r, errors.New("invalid execution claim")
	}
	secret, err := credential.read()
	if err != nil {
		return r, err
	}
	if err = c.post(ctx, BackgroundControllerClaimPath, ControllerClaim{ExecutionClaimRequest: q, GrantID: grant, Token: secret}, &r); err != nil {
		return r, err
	}
	if r.ManifestDigest != q.ManifestDigest || r.NotAfter != q.NotAfter {
		return r, errors.New("execution claim changed")
	}
	return r, ValidateControllerClaim(r, ref)
}

func (c *backgroundClient) ReadControllerExecution(ctx context.Context, grant, command string) (ExecutionClaimReceipt, error) {
	ref := ControllerReference{GrantID: grant, CommandID: command}
	var r ExecutionClaimReceipt
	if !ValidControllerReference(ref) || !runtimeHex(command) {
		return r, errors.New("invalid claim reference")
	}
	if err := c.post(ctx, BackgroundControllerClaimGetPath, ref, &r); err != nil {
		return r, err
	}
	if r.Acquired {
		return r, errors.New("lookup cannot acquire execution")
	}
	return r, ValidateControllerClaim(r, ref)
}

func (c *backgroundClient) RetireControllerExecution(ctx context.Context, grant, command string, q ExecutionRetirement) (ExecutionClaimReceipt, error) {
	ref := ControllerReference{GrantID: grant, CommandID: command}
	var r ExecutionClaimReceipt
	if !ValidControllerReference(ref) || !runtimeHex(command) || !validSHA256Hex(q.ManifestDigest) || !validSHA256Hex(q.ReceiptDigest) || (q.State != "retired" && q.State != "not_started") {
		return r, errors.New("invalid claim retirement")
	}
	if err := c.post(ctx, BackgroundControllerRetirePath, ControllerRetirement{ControllerReference: ref, ExecutionRetirement: q}, &r); err != nil {
		return r, err
	}
	if r.Acquired || r.ManifestDigest != q.ManifestDigest || r.State != q.State || r.RetirementDigest != q.ReceiptDigest {
		return r, errors.New("claim retirement changed")
	}
	return r, ValidateControllerClaim(r, ref)
}

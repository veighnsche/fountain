// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

// RuntimePlacement is a host observation, not a launch lease. A new generation
// can become active only after the prior controller's claims and native effects
// retire. Repository and actor identities are native to this Fountain host.
type RuntimePlacement struct {
	RepositoryID string `json:"repository_id"`
	RuntimeID    string `json:"runtime_id"`
	OwnerKind    string `json:"owner_kind"`
	OwnerID      string `json:"owner_id"`
	Generation   int64  `json:"generation,string"`
	Revision     string `json:"revision"`
	State        string `json:"state"`
}

// RuntimeSelection captures one finite administrative command. Missing lookup
// evidence never changes its deadline, expected revision or command identity.
type RuntimeSelection struct {
	CommandID        string `json:"command_id"`
	ExpectedRevision string `json:"expected_revision"`
	Action           string `json:"action"`
	NotAfter         int64  `json:"not_after"`
}

type RuntimeSelectionReceipt struct {
	CommandID  string           `json:"command_id"`
	Outcome    string           `json:"outcome"`
	ReasonCode string           `json:"reason_code,omitempty"`
	Placement  RuntimePlacement `json:"placement"`
}

type DesktopOperationAuthorization struct {
	CommandID         string   `json:"command_id"`
	ExpectedPlacement string   `json:"expected_placement"`
	Generation        int64    `json:"generation,string"`
	Operations        []string `json:"operations"`
	NotAfter          int64    `json:"not_after"`
}

// DesktopOperationGrant is public metadata. Every new use requires the current
// authenticated actor and native-held profile proof. Expiry or withdrawal never
// removes the old operation journal or permits a replacement send.
type DesktopOperationGrant struct {
	ID             string     `json:"id"`
	EnrollmentID   string     `json:"enrollment_id"`
	RuntimeID      string     `json:"runtime_id"`
	InstallationID string     `json:"installation_id"`
	Generation     int64      `json:"generation,string"`
	Actor          Actor      `json:"actor"`
	Repository     Repository `json:"repository"`
	Operations     []string   `json:"operations"`
	NotAfter       int64      `json:"not_after"`
	Active         bool       `json:"active"`
}

// ExecutionClaim reserves one exact native dispatch. Acquired is true only for
// the first admission; a lost reply must be reconciled with the original native
// journal, never treated as permission to launch again.
type ExecutionClaimRequest struct {
	CommandID      string `json:"command_id"`
	ManifestDigest string `json:"manifest_digest"`
	NotAfter       int64  `json:"not_after"`
}
type ExecutionClaimReceipt struct {
	CommandID        string `json:"command_id"`
	GrantID          string `json:"grant_id"`
	ManifestDigest   string `json:"manifest_digest"`
	NotAfter         int64  `json:"not_after"`
	State            string `json:"state"`
	RetirementDigest string `json:"retirement_digest"`
	Acquired         bool   `json:"acquired"`
}
type ExecutionRetirement struct {
	ManifestDigest string `json:"manifest_digest"`
	State          string `json:"state"`
	ReceiptDigest  string `json:"receipt_digest"`
}

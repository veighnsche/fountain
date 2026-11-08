// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

// DesktopGrantEnrollment selects one repository and native runtime. The host
// supplies the account and OAuth application from authenticated request state.
type DesktopGrantEnrollment struct {
	CommandID   string `json:"command_id"`
	RuntimeID   string `json:"runtime_id"`
	ProofSHA256 string `json:"proof_sha256,omitempty"`
}

// DesktopGrant is enrollment metadata, never a bearer credential. Every use
// still requires the same current OAuth authorization and repository access.
// This initial gateway scope admits repository reads only.
type DesktopGrant struct {
	ID            string     `json:"id"`
	RuntimeID     string     `json:"runtime_id"`
	ApplicationID string     `json:"application_id"`
	Actor         Actor      `json:"actor"`
	Repository    Repository `json:"repository"`
	Active        bool       `json:"active"`
	Operations    []string   `json:"operations"`
	RuntimeBound  bool       `json:"runtime_bound"`
}

const DesktopRepositoryRead = "repository.read"

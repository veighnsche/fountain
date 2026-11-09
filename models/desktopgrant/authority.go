// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package desktopgrant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"
	"github.com/google/uuid"
)

const (
	CredentialOAuth = "desktop_oauth"
	CredentialPAT   = "background_pat"
	ExecutionLaunch = "execution.launch"
	ClaimHeld       = "claimed"
	ClaimRetired    = "retired"
	ClaimNotStarted = "not_started"
)

// OperationGrant is a separately authorized, immutable privilege scope. A read
// enrollment never creates it. OAuth and PAT provenance remain distinct; no PAT
// is synthesized for the desktop and no credential is returned in a projection.
type OperationGrant struct {
	ID                string             `xorm:"pk VARCHAR(32)"`
	CredentialKind    string             `xorm:"VARCHAR(24) NOT NULL"`
	EnrollmentID      string             `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"`
	OAuthGrantID      int64              `xorm:"'oauth_grant_id' NOT NULL DEFAULT 0"`
	ApplicationID     int64              `xorm:"NOT NULL DEFAULT 0"`
	TokenID           int64              `xorm:"NOT NULL DEFAULT 0"`
	TokenFingerprint  string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	InstallationID    string             `xorm:"VARCHAR(36) NOT NULL INDEX"`
	PackageID         string             `xorm:"VARCHAR(255) NOT NULL DEFAULT ''"`
	ActorID           int64              `xorm:"NOT NULL INDEX"`
	RepositoryID      int64              `xorm:"NOT NULL INDEX"`
	RuntimeID         string             `xorm:"VARCHAR(32) NOT NULL"`
	Generation        int64              `xorm:"NOT NULL"`
	SelectionRevision string             `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"`
	ProofSHA256       string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	Operations        string             `xorm:"TEXT NOT NULL"`
	NotAfter          int64              `xorm:"NOT NULL"`
	Revoked           bool               `xorm:"NOT NULL DEFAULT FALSE"`
	CreatedUnix       timeutil.TimeStamp `xorm:"created NOT NULL"`
}

func (*OperationGrant) TableName() string { return "native_controller_grant" }

// ExecutionClaim records authority reserved for an exact external native run;
// it does not assert that a worker launched or that inference happened. Neither
// expiry nor absence of a reply releases a claim. Only the owning native
// controller's exact retirement/no-start evidence can settle it.
type ExecutionClaim struct {
	ID               string             `xorm:"pk VARCHAR(32)"`
	GrantID          string             `xorm:"VARCHAR(32) NOT NULL INDEX"`
	InstallationID   string             `xorm:"VARCHAR(36) NOT NULL"`
	RepositoryID     int64              `xorm:"NOT NULL INDEX"`
	ActorID          int64              `xorm:"NOT NULL"`
	RuntimeID        string             `xorm:"VARCHAR(32) NOT NULL"`
	Generation       int64              `xorm:"NOT NULL"`
	ManifestDigest   string             `xorm:"VARCHAR(64) NOT NULL"`
	NotAfter         int64              `xorm:"NOT NULL"`
	State            string             `xorm:"VARCHAR(16) NOT NULL"`
	RetirementDigest string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	CreatedUnix      timeutil.TimeStamp `xorm:"created NOT NULL"`
	UpdatedUnix      timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*ExecutionClaim) TableName() string { return "native_execution_claim" }
func init()                               { db.RegisterModel(new(OperationGrant)); db.RegisterModel(new(ExecutionClaim)) }

func DesktopInstallation(enrollment string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("fountain.native-desktop\x00"+enrollment)).String()
}

// ApplianceRuntime names the controller of this exact admitted installation,
// not a browser-selected address or a desktop profile. Reinstallation changes
// the owner; old grants and unresolved claims cannot move to the replacement.
func ApplianceRuntime(installation string) string {
	return strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("fountain.native-appliance\x00"+installation)).String(), "-", "")
}

func (g *OperationGrant) Allows(kind string) bool {
	for _, allowed := range strings.Split(g.Operations, ",") {
		if allowed == kind {
			return true
		}
	}
	return false
}

// Fingerprint binds private immutable provenance, never a reusable credential.
func (g OperationGrant) Fingerprint() string {
	g.Revoked = false
	g.CreatedUnix = 0
	raw, _ := json.Marshal(g)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func GetOperationGrant(ctx context.Context, id string) (*OperationGrant, error) {
	g := new(OperationGrant)
	found, err := db.GetEngine(ctx).ID(id).NoAutoCondition().Get(g)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	return g, nil
}

// EnrollOperationGrant runs under WithPlacement. A same-ID replay returns the
// old scope even when it expired or withdrew; changed scope cannot renew it.
func EnrollOperationGrant(ctx context.Context, g *OperationGrant) (*OperationGrant, error) {
	prior, err := GetOperationGrant(ctx, g.ID)
	if err == nil {
		if prior.Fingerprint() != g.Fingerprint() {
			return nil, ErrConflict
		}
		return prior, nil
	}
	if err != ErrNotFound {
		return nil, err
	}
	if err = db.Insert(ctx, g); err != nil {
		return nil, err
	}
	return g, nil
}

func RevokeOperationGrant(ctx context.Context, g *OperationGrant) error {
	_, err := db.GetEngine(ctx).ID(g.ID).NoAutoCondition().Cols("revoked").Update(&OperationGrant{Revoked: true})
	return err
}

func GetExecutionClaim(ctx context.Context, id string) (*ExecutionClaim, error) {
	c := new(ExecutionClaim)
	found, err := db.GetEngine(ctx).ID(id).NoAutoCondition().Get(c)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	return c, nil
}

// RecordExecutionClaim runs under WithPlacement after current grant admission.
// An existing claim never reports new launch ownership to another controller.
func RecordExecutionClaim(ctx context.Context, c *ExecutionClaim) (*ExecutionClaim, bool, error) {
	prior, err := GetExecutionClaim(ctx, c.ID)
	if err == nil {
		if prior.GrantID != c.GrantID || prior.InstallationID != c.InstallationID || prior.RepositoryID != c.RepositoryID || prior.ActorID != c.ActorID ||
			prior.RuntimeID != c.RuntimeID || prior.Generation != c.Generation || prior.ManifestDigest != c.ManifestDigest || prior.NotAfter != c.NotAfter {
			return nil, false, ErrConflict
		}
		return prior, false, nil
	}
	if err != ErrNotFound {
		return nil, false, err
	}
	if c.State != ClaimHeld || c.RetirementDigest != "" {
		return nil, false, ErrController
	}
	if err = db.Insert(ctx, c); err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// RetireExecutionClaim does not infer retirement from a deadline, missing lookup
// or current placement. The authenticated owner supplies an exact native
// receipt digest; a changed receipt conflicts rather than replacing history.
func RetireExecutionClaim(ctx context.Context, id, grant, manifest, state, digest string) (*ExecutionClaim, error) {
	c, err := GetExecutionClaim(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.GrantID != grant || c.ManifestDigest != manifest || len(digest) != 64 || (state != ClaimRetired && state != ClaimNotStarted) {
		return nil, ErrConflict
	}
	if c.State != ClaimHeld {
		if c.State != state || c.RetirementDigest != digest {
			return nil, ErrConflict
		}
		return c, nil
	}
	_, err = db.GetEngine(ctx).ID(id).Where("state=?", ClaimHeld).Cols("state", "retirement_digest").Update(&ExecutionClaim{State: state, RetirementDigest: digest})
	if err != nil {
		return nil, err
	}
	return GetExecutionClaim(ctx, id)
}

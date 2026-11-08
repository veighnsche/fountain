// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"forgejo.org/services/nativeownership"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
)

// ReceiveClaimTimeout bounds how long a Git ingress claim waits for a
// busy reservation before refusing the push. Holds are short writer
// spans; a push waits out ordinary contention but never waits through
// offline inhibition, which refuses immediately.
const ReceiveClaimTimeout = 60 * time.Second

// FT05 repository/ref writer families. Each family records its affected
// resource identities in its scope before effects so offline recovery can
// reconcile that family's actual database/ref effects; a merge-tip
// comparison never reconciles another family.
const (
	// FamilyReceiveHTTP is one Git smart-HTTP receive-pack execution.
	FamilyReceiveHTTP = "receive-http"
	// FamilyReceiveSSH is one SSH receive-pack execution. The serv process
	// holds no database handle, so the web process claims on its behalf at
	// ServCommand time and releases through the SSH release endpoint.
	FamilyReceiveSSH = "receive-ssh"
	// FamilyRefWrite is one direct single-ref native write: file-service
	// commits, release tags, PR ref updates, PR branch updates, ordinary
	// merges, branch renames, default-branch changes and fork syncs. The
	// resource string names the ref-write kind (see RefWrite* below).
	FamilyRefWrite = "ref-write"
	// FamilyRepoLifecycle is one repository lifecycle operation: creation,
	// deletion, fork, migration, template generation, adoption, transfer,
	// rename or push-create. The resource string names the operation.
	FamilyRepoLifecycle = "repo-lifecycle"
	// FamilyRepoSettings is one repository settings change: units, edit
	// form fields or fork/mirror conversion. The resource string names
	// the settings area.
	FamilyRepoSettings = "repo-settings"
	// FamilyProtection is one branch/tag protection rule change. The
	// resource string names the rule.
	FamilyProtection = "protection"
	// FamilyMirrorSync is one pull or push mirror synchronization with its
	// resulting branch state. The resource string names the mirror.
	FamilyMirrorSync = "mirror-sync"
	// FamilyRefSync is one branch/tag database synchronization with Git.
	// The resource string names the ref area.
	FamilyRefSync = "ref-sync"
	// FamilyMaintenance is one repository maintenance operation: garbage
	// collection, HEAD repair or doctor fixes. The resource string names
	// the maintenance operation.
	FamilyMaintenance = "maintenance"
)

// CrashPointMaintenanceAfterEffects pauses one maintenance update after
// its effects commit. It is consumed through TestCrashBarrier like the
// points in crash_barrier.go; the point string lives here so this family
// needs no change to that file.
const CrashPointMaintenanceAfterEffects = "maintenance-after-effects"

// Ref-write kinds carried in the FamilyRefWrite resource string.
const (
	RefWriteFile          = "file"
	RefWriteTag           = "tag"
	RefWritePRRef         = "pr-ref"
	RefWritePRUpdate      = "pr-update"
	RefWriteMerge         = "merge"
	RefWriteRename        = "rename"
	RefWriteDefaultBranch = "default-branch"
	RefWriteSyncFork      = "sync-fork"
)

// Repository lifecycle operations carried in the FamilyRepoLifecycle
// resource string.
const (
	LifecycleCreate          = "create"
	LifecycleDelete          = "delete"
	LifecycleFork            = "fork"
	LifecycleMigrate         = "migrate"
	LifecycleGenerate        = "generate"
	LifecycleAdopt           = "adopt"
	LifecycleTransfer        = "transfer"
	LifecycleRename          = "rename"
	LifecycleConvertFork     = "convert-fork"
	LifecycleConvertMirror   = "convert-mirror"
	LifecycleDeleteUnadopted = "delete-unadopted"
)

// LifecycleResource builds the owner resource string for one repository
// lifecycle operation: repository (0 when the row does not exist yet),
// operation and owner/name identity.
func LifecycleResource(repoID int64, op, ownerName, repoName string) string {
	return fmt.Sprintf("%d/%s/%s/%s", repoID, op, ownerName, repoName)
}

// Protection rule kinds carried in the FamilyProtection resource string.
const (
	ProtectionBranch = "branch"
	ProtectionTag    = "tag"
)

// Protection operations carried in the FamilyProtection resource string.
// Creates and deletes reconcile by rule presence; edits fence when the
// rule is present because the applied field values are unknowable.
const (
	ProtectionCreate = "create"
	ProtectionEdit   = "edit"
	ProtectionDelete = "delete"
)

// ProtectionResource builds the owner resource string for one protection
// rule change: repository, rule kind, operation and rule name. Rule names
// may contain slashes; parsers split the first three segments only.
func ProtectionResource(repoID int64, kind, op, name string) string {
	return fmt.Sprintf("%d/%s/%s/%s", repoID, kind, op, name)
}

// OwnedGitEnv returns a child-process environment carrying this owner's
// execution capability for hook binding, or nil when the context carries
// no execution. A nil result preserves the ambient environment exactly,
// so call sites behave identically while idle.
func OwnedGitEnv(ctx context.Context) []string {
	exec := execcontext.FromContext(ctx)
	if exec == nil || exec.CapabilityPath == "" {
		return nil
	}
	return append(os.Environ(), execcontext.EnvExecFile+"="+exec.CapabilityPath)
}

// WithOrdinaryOwnershipWait claims like WithOrdinaryOwnership but waits
// out a busy reservation up to timeout instead of refusing at once.
// Only contention waits: inhibition, errors and nested reuse return
// immediately, and callers observe the same errors as the single attempt.
// Git ingress uses it so an ordinary push waits for short writer spans
// instead of failing them.
func (s *Service) WithOrdinaryOwnershipWait(ctx context.Context, family, resource string, scope Scope, timeout time.Duration, fn func(ctx context.Context) error) error {
	deadline := time.Now().Add(timeout)
	backoff := 100 * time.Millisecond
	for {
		entered := false
		err := s.WithOrdinaryOwnership(ctx, family, resource, scope, func(owned context.Context) error {
			entered = true
			return fn(owned)
		})
		if entered || !isBusyOnly(err) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 2*time.Second {
			backoff = 2 * time.Second
		}
	}
}

// isBusyOnly reports contention refusals that a bounded wait may ride
// out. Inhibition never clears by waiting and returns at once.
func isBusyOnly(err error) bool {
	return errors.Is(err, ErrBusy) || errors.Is(err, nativeoperation_model.ErrBusy)
}

// ClaimSSHReceive claims the reservation for one SSH receive-pack
// execution on behalf of the serv process, advancing the revision before
// its effects. The serv process creates the host-private capability file
// itself and passes only its verifier: the secret never crosses the
// internal channel. A busy reservation waits up to ReceiveClaimTimeout
// before refusing; inhibition refuses at once. The owner resource names
// the push target ("<id>/main" or "<id>/wiki") for offline recovery.
func (s *Service) ClaimSSHReceive(ctx context.Context, repoID, pusherID int64, verifier string, wiki bool) (owner string, generation int64, err error) {
	if repoID <= 0 || pusherID < 0 || len(verifier) != 64 {
		return "", 0, errors.New("ssh receive claim requires repository, pusher and execution verifier")
	}
	scope, err := encodeScope(Scope{
		Family:       FamilyReceiveSSH,
		RepositoryID: repoID,
		PusherID:     pusherID,
	})
	if err != nil {
		return "", 0, err
	}
	target := "main"
	if wiki {
		target = "wiki"
	}
	owner = nativeownership.NewOwner(FamilyReceiveSSH, fmt.Sprintf("%d/%s", repoID, target))
	deadline := time.Now().Add(ReceiveClaimTimeout)
	backoff := 100 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		var claimed *nativeoperation_model.Reservation
		claimed, err = nativeoperation_model.ClaimOrdinary(ctx, owner, scope, verifier)
		if err == nil {
			return owner, claimed.Generation, nil
		}
		if errors.Is(err, nativeoperation_model.ErrInhibited) {
			return "", 0, fmt.Errorf("%w: ssh receive refuses while offline recovery holds the domain", nativeoperation_model.ErrInhibited)
		}
		if !errors.Is(err, nativeoperation_model.ErrBusy) {
			return "", 0, err
		}
		if time.Now().After(deadline) {
			return "", 0, fmt.Errorf("%w: ssh receive for repository %d is fenced", nativeoperation_model.ErrBusy, repoID)
		}
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 2*time.Second {
			backoff = 2 * time.Second
		}
	}
}

// ReleaseSSHReceive releases one SSH receive owner after its receiver
// exits. Only exact receive-ssh owners release here; anything else,
// including conditional owners, refuses. The serv process removes its own
// capability file; a crashed receiver keeps its owner for offline
// recovery, which retires stale files.
func (s *Service) ReleaseSSHReceive(ctx context.Context, owner string, generation int64) error {
	if !strings.HasPrefix(owner, "ord:"+FamilyReceiveSSH+"/") || generation <= 0 {
		return nativeoperation_model.ErrWrongOwner
	}
	// The release must land even if the receiver's context is gone;
	// anything else strands the reservation on a routine disconnect.
	return nativeoperation_model.ReleaseExactOwner(context.WithoutCancel(ctx), owner, generation)
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sdk "forgejo.org/extension-sdk"
)

// RecoveryCandidates lists known package IDs eligible for interrupted-swap
// repair (RCX-2 FX04): required IDs, trusted catalog IDs, and IDs carrying
// intent, installation or host origin records. The result is deduplicated and sorted so
// startup repair runs deterministically. Exact `.previous-<id>` paths are
// derived per ID by RecoverPackage; no directory is swept for hidden
// names, so staging residue (`.install-*`, `.rollback-*`), the package
// lock, and backups of unrelated or fully removed IDs are never touched.
func RecoveryCandidates(root string, catalog []sdk.CatalogPackage, requiredIDs []string) []string {
	known := make(map[string]bool)
	add := func(id string) {
		if sdk.ValidID(id) {
			known[id] = true
		}
	}
	for _, id := range requiredIDs {
		add(id)
	}
	for _, entry := range catalog {
		add(entry.ID)
	}
	// Unreadable metadata never blocks discovery: required and
	// catalog IDs still repair, and exact repair validates
	// everything it restores.
	if intents, err := listIntents(root); err == nil {
		for _, id := range intents {
			add(id)
		}
	}
	for _, id := range listInstallations(root) {
		add(id)
	}
	if entries, err := os.ReadDir(filepath.Dir(packageStatePath(root, "probe"))); err == nil {
		for _, entry := range entries {
			if id, ok := strings.CutSuffix(entry.Name(), ".json"); ok && !entry.IsDir() {
				add(id)
			}
		}
	}
	candidates := make([]string, 0, len(known))
	for id := range known {
		candidates = append(candidates, id)
	}
	sort.Strings(candidates)
	return candidates
}

// listInstallations returns the IDs carrying a persisted installation
// record. A missing directory means no installations; other read
// failures are ignored here because repair discovery must never block
// startup on unreadable metadata — the exact `.previous-<id>` repair
// below still validates everything it restores.
func listInstallations(root string) []string {
	entries, err := os.ReadDir(filepath.Dir(InstallationPath(root, "probe")))
	if err != nil {
		return nil
	}
	var ids []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		if id := strings.TrimSuffix(name, ".json"); sdk.ValidID(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// RecoverInterrupted repairs interrupted replacements for known package
// IDs through the lock-bearing store handle (RCX-2 FX04): a validated
// fixed backup restores an absent target, a validated target retires
// exactly its backup, and invalid or incomplete state stays unavailable
// with the backup preserved for inspection. IDs with no backup are
// already stable and cost one Lstat each. Only truly empty repairs
// (neither target nor backup) keep the empty state, silently: nothing
// was ever installed there. The returned map carries only the IDs whose
// repair failed, so callers can block required readiness while
// isolating optional packages; a nil map means every known package is
// repaired or was already stable.
func (s *PackageStore) RecoverInterrupted(ids []string) map[string]error {
	var failures map[string]error
	for _, id := range ids {
		if err := RecoverPackage(s.root, id); err != nil {
			if failures == nil {
				failures = make(map[string]error)
			}
			failures[id] = err
		}
	}
	return failures
}

// errRecoveryUnavailable marks state RecoverPackage refuses to execute.
var errRecoveryUnavailable = errors.New("installed package is unavailable")

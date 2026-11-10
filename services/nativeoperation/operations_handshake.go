// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/modules/setting"
)

// OperationsImplementationRevision is the explicitly maintained revision
// of background-operation validation, availability and semantics. Bump it
// by one in the same commit that changes any submit/get/cancel behavior,
// intent validation, refusal mapping, reservation rules or the supported
// kind set. The handshake generation binds this revision so a same-kind
// native upgrade invalidates previously registered Soda capability
// markers; forgetting the bump leaves stale markers authoritative.
const OperationsImplementationRevision = 4

// OperationsGeneration derives a stable handshake generation from the
// operation implementation revision, the installed build identity and
// the supported kind set. Identical installed bytes yield identical
// output across restarts; any input change yields a new generation.
// Kind order is normalized before digesting.
func OperationsGeneration(implementationRevision int, buildVersion, buildContext string, kinds []string) string {
	sorted := make([]string, len(kinds))
	copy(sorted, kinds)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"implementation:" + strconv.Itoa(implementationRevision),
		"build:" + buildVersion,
		"built-with:" + buildContext,
		strings.Join(sorted, "\n"),
	}, "\n")))
	return "ops-" + hex.EncodeToString(sum[:])[:16]
}

// OperationsHandshake describes the host's supported background
// operations: a stable generation digest plus the sorted kind list. The
// generation binds the implementation revision, installed build identity
// and kind set; native replacement that keeps kind names still changes
// it through the revision or build inputs. Installation identity in the
// response comes from verified admission, not from this query. This
// query is read-only: no reservation, no mutation, no capability file.
type OperationsHandshake struct {
	Generation string
	Kinds      []string
}

// SupportedOperations returns the current handshake facts.
func (s *Service) SupportedOperations(_ context.Context) (OperationsHandshake, error) {
	kinds := nativeoperation_model.SupportedKinds()
	return OperationsHandshake{
		Generation: OperationsGeneration(OperationsImplementationRevision, setting.AppVer, setting.AppBuiltWith, kinds),
		Kinds:      kinds,
	}, nil
}

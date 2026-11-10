// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"strings"

	"forgejo.org/extension-sdk/gitsyntax"
)

// Snapshot conversion for native check and ref evidence (FT10, early start).
//
// Like the collaboration snapshots, these types convert already-loaded
// native records into permission-checked evidence with exact versions,
// visibility and completeness. The caller reads current commit-status rows
// and live ref tips and supplies the actor's visibility decision; hidden
// records are redacted to their locator keys. The required check set is
// Soda policy and never appears here. Authoritative guarantees wait for
// FT09 plus the FT10 revision-bracket proof; until then this is
// conversion/DTO work only, not F-read.

// ErrCheckSnapshotInvalid rejects malformed check/ref inputs rather than
// silently broadening or defaulting them.
var ErrCheckSnapshotInvalid = errors.New("invalid native check snapshot input")

// Hidden reasons match the collaboration snapshot vocabulary.
const (
	CheckHiddenNotFound  = "not_found"
	CheckHiddenNoAccess  = "no_access"
	CheckHiddenRedacted  = "redacted"
	CheckHiddenWithdrawn = "withdrawn"
)

func validCheckHiddenReason(reason string) bool {
	switch reason {
	case CheckHiddenNotFound, CheckHiddenNoAccess, CheckHiddenRedacted, CheckHiddenWithdrawn:
		return true
	default:
		return false
	}
}

func validCheckOID(oid string) bool {
	return gitsyntax.FullOID(oid)
}

func validCheckBranchRef(ref string) bool {
	return gitsyntax.BranchRef(ref)
}

// CheckSnapshot is the permission-checked view of one native commit status.
type CheckSnapshot struct {
	CheckID      int64
	Index        int64
	RepositoryID int64
	SHA          string
	Context      string
	State        string
	CreatorID    int64
	CreatedUnix  int64
	UpdatedUnix  int64
	Visible      bool
	HiddenReason string
	Complete     bool
}

// NewCheckSnapshot converts one current commit-status row. A hidden check
// preserves only the repository and commit locators the caller supplied.
func NewCheckSnapshot(status *CommitStatus, visible bool, hiddenReason string, complete bool) (CheckSnapshot, error) {
	if status == nil || status.ID <= 0 || status.RepoID <= 0 {
		return CheckSnapshot{}, ErrCheckSnapshotInvalid
	}
	if !validCheckOID(status.SHA) {
		return CheckSnapshot{}, ErrCheckSnapshotInvalid
	}
	if !visible {
		if !validCheckHiddenReason(hiddenReason) {
			return CheckSnapshot{}, ErrCheckSnapshotInvalid
		}
		return CheckSnapshot{
			CheckID:      status.ID,
			RepositoryID: status.RepoID,
			SHA:          strings.ToLower(status.SHA),
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return CheckSnapshot{}, ErrCheckSnapshotInvalid
	}
	// The stored state passes through exactly as observed: writers accept
	// states beyond the five documented ones (ordinary REST stores
	// cancelled, skipped and arbitrary strings), and consumers fail
	// closed on non-success states. Refusing here would fail snapshot
	// reads for rows the platform itself stores and lists back.
	state := string(status.State)
	// The stored creator passes through exactly as observed, including
	// system creators such as the Actions user: Actions-posted statuses
	// carry no real user. The wire renders non-positive creator IDs
	// empty, matching the REST rendering of the same row.
	if status.Index < 0 {
		return CheckSnapshot{}, ErrCheckSnapshotInvalid
	}
	created := int64(status.CreatedUnix)
	updated := int64(status.UpdatedUnix)
	if created <= 0 || updated < created {
		return CheckSnapshot{}, ErrCheckSnapshotInvalid
	}
	return CheckSnapshot{
		CheckID:      status.ID,
		Index:        status.Index,
		RepositoryID: status.RepoID,
		SHA:          strings.ToLower(status.SHA),
		Context:      status.Context,
		State:        state,
		CreatorID:    status.CreatorID,
		CreatedUnix:  created,
		UpdatedUnix:  updated,
		Visible:      true,
		Complete:     complete,
	}, nil
}

// CheckSet carries one bounded check list for an exact commit with
// completeness evidence. Required-context matching is Soda policy.
type CheckSet struct {
	RepositoryID int64
	SHA          string
	Items        []CheckSnapshot
	Total        int
	Complete     bool
}

// NewCheckSet validates one loader-read check list. Complete requires every
// Total item Returned with each item complete and bound to this commit.
func NewCheckSet(repositoryID int64, sha string, items []CheckSnapshot, total int, complete bool) (CheckSet, error) {
	if repositoryID <= 0 || !validCheckOID(sha) || total < 0 || len(items) > total {
		return CheckSet{}, ErrCheckSnapshotInvalid
	}
	for _, item := range items {
		if item.RepositoryID != repositoryID || !item.Complete {
			return CheckSet{}, ErrCheckSnapshotInvalid
		}
		if item.Visible && item.SHA != strings.ToLower(sha) {
			return CheckSet{}, ErrCheckSnapshotInvalid
		}
	}
	if complete && len(items) != total {
		return CheckSet{}, ErrCheckSnapshotInvalid
	}
	return CheckSet{
		RepositoryID: repositoryID,
		SHA:          strings.ToLower(sha),
		Items:        append([]CheckSnapshot(nil), items...),
		Total:        total,
		Complete:     complete,
	}, nil
}

// RefSnapshot is the permission-checked view of one native branch tip read
// from live ref state. A hidden ref reports Exists=false with Visible=false
// so existence itself does not leak.
type RefSnapshot struct {
	RepositoryID int64
	Ref          string
	OID          string
	Exists       bool
	Visible      bool
	HiddenReason string
	Complete     bool
}

// NewRefSnapshot converts one loader-observed ref tip. oid must be the full
// tip read from live ref state when exists is true, and empty otherwise.
func NewRefSnapshot(repositoryID int64, ref, oid string, exists, visible bool, hiddenReason string, complete bool) (RefSnapshot, error) {
	if repositoryID <= 0 || !validCheckBranchRef(ref) {
		return RefSnapshot{}, ErrCheckSnapshotInvalid
	}
	if !visible {
		if !validCheckHiddenReason(hiddenReason) {
			return RefSnapshot{}, ErrCheckSnapshotInvalid
		}
		return RefSnapshot{
			RepositoryID: repositoryID,
			Ref:          ref,
			Exists:       false,
			Visible:      false,
			HiddenReason: hiddenReason,
			Complete:     complete,
		}, nil
	}
	if hiddenReason != "" {
		return RefSnapshot{}, ErrCheckSnapshotInvalid
	}
	if exists != (oid != "") {
		return RefSnapshot{}, ErrCheckSnapshotInvalid
	}
	if oid != "" && !validCheckOID(oid) {
		return RefSnapshot{}, ErrCheckSnapshotInvalid
	}
	return RefSnapshot{
		RepositoryID: repositoryID,
		Ref:          ref,
		OID:          strings.ToLower(oid),
		Exists:       exists,
		Visible:      true,
		Complete:     complete,
	}, nil
}

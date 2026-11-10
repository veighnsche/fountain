// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"errors"
	"strings"

	"forgejo.org/extension-sdk/gitsyntax"
)

const EnvGitOrigin = "FORGEJO_NATIVEOP_GIT_ORIGIN"

type GitExecutionOrigin string

const (
	GitOriginReceiveHTTP   GitExecutionOrigin = "receive-http"
	GitOriginReceiveSSH    GitExecutionOrigin = "receive-ssh"
	GitOriginInternalRef   GitExecutionOrigin = "internal-ref"
	GitOriginRefRename     GitExecutionOrigin = "branch-rename"
	GitOriginLegacyReceive GitExecutionOrigin = "legacy-receive"
)

// GitOrigin accepts the old receive marker only when the explicit marker is
// absent. Origin selects participation; it never supplies execution authority.
func GitOrigin(explicit, legacyCommand string) (GitExecutionOrigin, error) {
	switch GitExecutionOrigin(explicit) {
	case GitOriginReceiveHTTP, GitOriginReceiveSSH, GitOriginInternalRef, GitOriginRefRename:
		return GitExecutionOrigin(explicit), nil
	case "":
		if legacyCommand != "" {
			return GitOriginLegacyReceive, nil
		}
		return "", nil
	default:
		return "", errors.New("unknown native Git execution origin")
	}
}

func AppendGitOriginEnv(env []string, origin GitExecutionOrigin) []string {
	result := make([]string, 0, len(env)+1)
	for _, value := range env {
		if !strings.HasPrefix(value, EnvGitOrigin+"=") {
			result = append(result, value)
		}
	}
	return append(result, EnvGitOrigin+"="+string(origin))
}

// RefRenamePlan binds native branch -m, whose files backend does not expose
// destination creation in a prepared reference transaction. The active witness
// survives until the native command and enclosing metadata changes are proved.
type RefRenamePlan struct {
	RepositoryID   int64            `json:"repository_id"`
	OwnerName      string           `json:"owner_name"`
	RepositoryName string           `json:"repository_name"`
	Target         RepositoryTarget `json:"target"`
	FromRef        string           `json:"from_ref"`
	ToRef          string           `json:"to_ref"`
	TipOID         string           `json:"tip_oid"`
}

func (p RefRenamePlan) Validate() error {
	if p.RepositoryID <= 0 || p.OwnerName == "" || p.RepositoryName == "" ||
		strings.ContainsAny(p.OwnerName+p.RepositoryName, "/\\\x00\r\n") ||
		p.Target != TargetCode && p.Target != TargetWiki ||
		!renameBranchRef(p.FromRef) || !renameBranchRef(p.ToRef) || p.FromRef == p.ToRef ||
		!gitsyntax.FullOID(p.TipOID) || strings.Trim(p.TipOID, "0") == "" {
		return errors.New("invalid native branch rename plan")
	}

	return nil
}

func renameBranchRef(ref string) bool {
	return gitsyntax.BranchRef(ref) && !strings.ContainsAny(ref, "\x00\r\n")
}

type RefTransactionLine struct{ Old, New, Ref string }

// PermitsTransaction recognizes only the exact native rename transcript. A
// canonical atomic delete/create pair is also permitted; partial success still
// needs complete result proof before the active witness can be cleared.
func (p RefRenamePlan) PermitsTransaction(phase string, lines []RefTransactionLine) bool {
	if p.Validate() != nil || phase != "prepared" && phase != "committed" && phase != "aborted" {
		return false
	}
	zero := strings.Repeat("0", len(p.TipOID))
	deletion := func(line RefTransactionLine) bool {
		return line.Ref == p.FromRef && line.Old == p.TipOID && line.New == zero
	}
	creation := func(line RefTransactionLine) bool {
		return line.Ref == p.ToRef && line.Old == zero && line.New == p.TipOID
	}
	if len(lines) == 2 {
		return deletion(lines[0]) && creation(lines[1]) || deletion(lines[1]) && creation(lines[0])
	}
	if len(lines) != 1 {
		return false
	}
	line := lines[0]
	if phase == "aborted" && line.Ref == p.FromRef && line.Old == zero && line.New == zero {
		return true
	}
	return deletion(line) || creation(line) || line.Ref == "HEAD" && line.Old == zero && line.New == "ref:"+p.ToRef
}

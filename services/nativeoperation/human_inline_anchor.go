// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	issues_model "forgejo.org/models/issues"
	"forgejo.org/modules/git"
)

const MaxHumanInlineDiffBytes = 4 << 20

var ErrHumanInlineDiffTooLarge = errors.New("inline diff proof exceeds limit")

type HumanInlineProof struct{ EntryOID, EntryKind, DiffDigest string }

type humanInlineBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *humanInlineBuffer) Len() int      { return b.buffer.Len() }
func (b *humanInlineBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *humanInlineBuffer) Write(p []byte) (int, error) {
	if len(p) > MaxHumanInlineDiffBytes-b.Len() {
		b.exceeded = true
		return 0, ErrHumanInlineDiffTooLarge
	}
	return b.buffer.Write(p)
}

// ProveHumanInlineAnchor reads immutable Git objects only. Callers own live PR,
// native revision, principal and permission checks. File anchors need only bounded
// raw change metadata, so large/binary files do not require a full text patch.
// Line ranges must be wholly covered by actual hunks on their selected side.
func ProveHumanInlineAnchor(ctx context.Context, repository *git.Repository, a issues_model.CodeCommentAnchor) (HumanInlineProof, error) {
	var proof HumanInlineProof
	refuse := func() (HumanInlineProof, error) {
		return proof, issues_model.NewExactReviewRefusal(reviewRefusalInvalidInlineAnchor, "invalid inline anchor")
	}
	if a.Validate() != nil || a.EndLine > 9007199254740991 {
		return refuse()
	}
	commit := a.Head
	if a.Side == "old" {
		commit = a.Base
	}
	tree, err := repository.GetCommit(commit)
	if err != nil {
		return proof, err
	}
	entry, err := tree.GetTreeEntryByPath(a.Path)
	if git.IsErrNotExist(err) {
		return refuse()
	}
	if err != nil {
		return proof, err
	}
	if entry.IsDir() || a.Scope == "lines" && entry.IsSubmodule() {
		return refuse()
	}
	output := &humanInlineBuffer{}
	cmd := git.NewCommand(ctx, "--literal-pathspecs", "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--no-relative", "--ignore-submodules=none", "--submodule=short", "--diff-algorithm=histogram")
	if a.Scope == "file" {
		cmd.AddArguments("--raw", "-z", "--no-abbrev")
	} else {
		cmd.AddArguments("--patch", "--src-prefix=a/", "--dst-prefix=b/", "--unified=3")
	}
	err = cmd.AddDynamicArguments(a.Base, a.Head).AddDashesAndList(a.Path).Run(&git.RunOpts{Dir: repository.Path, Stdout: output})
	if output.exceeded {
		return proof, ErrHumanInlineDiffTooLarge
	}
	if err != nil {
		if errors.Is(err, ErrHumanInlineDiffTooLarge) || output.Len() >= MaxHumanInlineDiffBytes {
			return proof, ErrHumanInlineDiffTooLarge
		}
		return proof, err
	}
	if output.Len() == 0 {
		return refuse()
	}
	if a.Scope == "lines" {
		lines := parseReviewFileDiff(output.Bytes())
		if lines.binary || !lines.hasHunks {
			return refuse()
		}
		covered := lines.newLines
		if a.Side == "old" {
			covered = lines.oldLines
		}
		var count int64
		for line := range covered {
			if line >= a.StartLine && line <= a.EndLine {
				count++
			}
		}
		if count != a.EndLine-a.StartLine+1 {
			return refuse()
		}
	}
	sum := sha256.Sum256(output.Bytes())
	proof = HumanInlineProof{EntryOID: entry.ID.String(), EntryKind: entry.Type(), DiffDigest: hex.EncodeToString(sum[:])}
	return proof, nil
}

var _ io.Writer = (*humanInlineBuffer)(nil)

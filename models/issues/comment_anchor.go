// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"errors"
	"path"
	"strings"
	"unicode/utf8"
)

// CodeCommentAnchor is the immutable context originally selected by the human
// or conditional writer. Path belongs to Side in that exact diff. A reader may
// recover the other side's rename path from these immutable trees; it must never
// replace this context with today's PR base/head or the mutable blame location.
// Nil on a retained/migrated comment means this context was not recorded.
type CodeCommentAnchor struct {
	Base      string `json:"base"`
	Head      string `json:"head"`
	Path      string `json:"path"`
	Side      string `json:"side"`
	Scope     string `json:"scope"`
	StartLine int64  `json:"start_line"`
	EndLine   int64  `json:"end_line"`
}

func (a CodeCommentAnchor) Validate() error {
	if !validFullOID(a.Base) || !validFullOID(a.Head) || a.Base != strings.ToLower(a.Base) || a.Head != strings.ToLower(a.Head) || a.Path == "" || len(a.Path) > 4096 || !utf8.ValidString(a.Path) || strings.ContainsRune(a.Path, 0) || strings.HasPrefix(a.Path, "/") || path.Clean(a.Path) != a.Path || a.Path == "." || a.Path == ".." || strings.HasPrefix(a.Path, "../") || a.Side != "old" && a.Side != "new" {
		return errors.New("invalid original code comment anchor")
	}
	switch a.Scope {
	case "file":
		if a.StartLine != 0 || a.EndLine != 0 {
			return errors.New("invalid file comment range")
		}
	case "lines":
		if a.StartLine < 1 || a.EndLine < a.StartLine {
			return errors.New("invalid code comment range")
		}
	default:
		return errors.New("invalid code comment scope")
	}
	return nil
}

// Copy keeps the caller from sharing mutable pointer storage with a persisted
// comment or the root whose context a reply inherits.
func (a *CodeCommentAnchor) Copy() *CodeCommentAnchor {
	if a == nil {
		return nil
	}
	copy := *a
	return &copy
}

// Equal compares exact recorded context, keeping absent context distinct from
// every recorded value, including malformed retained values.
func (a *CodeCommentAnchor) Equal(b *CodeCommentAnchor) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

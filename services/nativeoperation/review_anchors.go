// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	issues_model "forgejo.org/models/issues"
	"forgejo.org/modules/git"
	"forgejo.org/modules/setting"
)

// inlineAnchor is one review-submit comment anchor to prove against the
// live candidate diff.
type inlineAnchor struct {
	Key  string
	Path string
	Side string
	Line int64
}

// deriveReviewMergeBase returns the native merge base of the expected
// base and head OIDs: the old-side tree inline anchors validate against.
// The client cannot substitute one; a candidate without a merge base
// refuses instead of silently validating against a wrong tree.
func deriveReviewMergeBase(ctx context.Context, repoPath, baseOID, headOID string) (string, error) {
	stdout, _, err := git.NewCommand(ctx, "merge-base").AddDashesAndList(baseOID, headOID).RunStdBytes(&git.RunOpts{Dir: repoPath})
	if err != nil {
		return "", err
	}
	mergeBase := strings.ToLower(strings.TrimSpace(string(stdout)))
	if !validOID(mergeBase) {
		return "", issues_model.ErrExactReviewRefused{Reason: "merge base unavailable"}
	}
	return mergeBase, nil
}

// fetchReviewFileDiff returns the unified candidate diff of one requested
// path between the merge base and the exact head. Rename detection is
// disabled on purpose: the single-path view reads the old path as the old
// content and the new path as the new content, which is exactly the side
// rule inline anchors follow (old side uses the old path, new side the
// new path).
func fetchReviewFileDiff(ctx context.Context, repoPath, mergeBase, headOID, path string) ([]byte, error) {
	stdout, _, err := git.NewCommand(ctx, "diff", "--no-color", "--no-ext-diff", "--no-renames", "-U3").
		AddDynamicArguments(mergeBase, headOID).AddDashesAndList(path).
		RunStdBytes(&git.RunOpts{Dir: repoPath})
	if err != nil {
		return nil, err
	}
	return stdout, nil
}

// reviewDiffLines holds the anchorable one-based lines of one file diff:
// added and context lines on the new side, deleted and context lines on
// the old side.
type reviewDiffLines struct {
	oldLines map[int64]bool
	newLines map[int64]bool
	binary   bool
	hasHunks bool
}

// parseHunkHeader reads "@@ -old[,count] +new[,count] @@" and returns the
// starting line numbers on each side.
func parseHunkHeader(header string) (int64, int64, bool) {
	rest, found := strings.CutPrefix(header, "@@ -")
	if !found {
		return 0, 0, false
	}
	before, afterPlus, found := strings.Cut(rest, " +")
	if !found {
		return 0, 0, false
	}
	oldStart, ok := parseHunkNumber(before)
	if !ok {
		return 0, 0, false
	}
	newPart, _, found := strings.Cut(afterPlus, " @@")
	if !found {
		return 0, 0, false
	}
	newStart, ok := parseHunkNumber(newPart)
	if !ok {
		return 0, 0, false
	}
	return oldStart, newStart, true
}

// parseHunkNumber reads the leading line number of a "start[,count]"
// hunk coordinate.
func parseHunkNumber(coordinate string) (int64, bool) {
	if i := strings.Index(coordinate, ","); i >= 0 {
		coordinate = coordinate[:i]
	}
	number, err := strconv.ParseInt(coordinate, 10, 64)
	if err != nil || number < 0 {
		return 0, false
	}
	return number, true
}

// parseReviewFileDiff extracts the anchorable lines from one unified file
// diff. Only hunk body lines count: "+" and blank/context lines on the
// new side, "-" and blank/context lines on the old side. The binary
// marker and header lines never anchor.
func parseReviewFileDiff(diff []byte) reviewDiffLines {
	parsed := reviewDiffLines{oldLines: map[int64]bool{}, newLines: map[int64]bool{}}
	var oldLine, newLine int64
	inHunk := false
	for line := range strings.SplitSeq(string(diff), "\n") {
		if strings.HasPrefix(line, "Binary files ") {
			parsed.binary = true
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			oldStart, newStart, ok := parseHunkHeader(line)
			if !ok {
				inHunk = false
				continue
			}
			oldLine, newLine = oldStart, newStart
			inHunk = true
			parsed.hasHunks = true
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, " "):
			// Context lines follow native rules: anchorable on
			// either side. Git prefixes blank context with a space;
			// a bare empty line after the final newline is not content.
			parsed.oldLines[oldLine] = true
			parsed.newLines[newLine] = true
			oldLine++
			newLine++
		case strings.HasPrefix(line, "+"):
			parsed.newLines[newLine] = true
			newLine++
		case strings.HasPrefix(line, "-"):
			parsed.oldLines[oldLine] = true
			oldLine++
		case strings.HasPrefix(line, "\\"):
			// "\ No newline at end of file": no line advances.
		default:
			inHunk = false
		}
	}
	return parsed
}

// proveInlineAnchors checks every comment anchor against its file diff: a
// binary-only file refuses, a path without hunks refuses, and a line
// outside the hunk coverage of the claimed side refuses. Callers prove
// the structural rules (usable keys/paths, sides, duplicate anchors)
// separately before this diff-membership proof.
func proveInlineAnchors(diffs map[string][]byte, anchors []inlineAnchor) error {
	parsed := make(map[string]reviewDiffLines, len(diffs))
	for path, diff := range diffs {
		parsed[path] = parseReviewFileDiff(diff)
	}
	for _, anchor := range anchors {
		lines, found := parsed[anchor.Path]
		if !found {
			return issues_model.ErrExactReviewRefused{Reason: "invalid inline anchor"}
		}
		if lines.binary {
			return issues_model.ErrExactReviewRefused{Reason: "binary-only inline anchor"}
		}
		if !lines.hasHunks {
			return issues_model.ErrExactReviewRefused{Reason: "invalid inline anchor"}
		}
		var covered bool
		if anchor.Side == issues_model.ExactReviewCommentSideOld {
			covered = lines.oldLines[anchor.Line]
		} else {
			covered = lines.newLines[anchor.Line]
		}
		if !covered {
			return issues_model.ErrExactReviewRefused{Reason: "invalid inline anchor"}
		}
	}
	return nil
}

// cutInlineExcerpt cuts the native hunk excerpt stored on the code
// comment for diff-context rendering. Excerpts are rendering only: the
// anchor was already proven, so a cutter failure stores no excerpt
// rather than refusing a valid anchor.
func cutInlineExcerpt(diff []byte, anchor inlineAnchor) string {
	var old bool
	if anchor.Side == issues_model.ExactReviewCommentSideOld {
		old = true
	}
	excerpt, err := git.CutDiffAroundLine(bytes.NewReader(diff), anchor.Line, old, setting.UI.CodeCommentLines)
	if err != nil {
		return ""
	}
	return excerpt
}

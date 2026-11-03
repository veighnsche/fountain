// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"testing"

	issues_model "forgejo.org/models/issues"
	"forgejo.org/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const anchorsModifyDiff = `diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -1,4 +1,5 @@
 context1
-old2
+new2
 context3
 context4
@@ -10 +10 @@
 solo
`

const anchorsBlankDiff = `diff --git a/b.go b/b.go
index 1111111..2222222 100644
--- a/b.go
+++ b/b.go
@@ -1,4 +1,4 @@
 first
` + " \n" + `
-third
+third!
 fifth
`

const anchorsAddedDiff = `diff --git a/new.go b/new.go
new file mode 100644
index 0000000..2222222
--- /dev/null
+++ b/new.go
@@ -0,0 +1,3 @@
+one
+two
+three
`

const anchorsDeletedDiff = `diff --git a/old.go b/old.go
deleted file mode 100644
index 1111111..0000000
--- a/old.go
+++ /dev/null
@@ -1,2 +0,0 @@
-gone1
-gone2
`

const anchorsBinaryDiff = `diff --git a/img.png b/img.png
index 1111111..2222222 100644
Binary files a/img.png and b/img.png differ
`

const anchorsModeOnlyDiff = `diff --git a/run.sh b/run.sh
old mode 100644
new mode 100755
`

func anchorFor(path, side string, line int64) inlineAnchor {
	return inlineAnchor{Key: path + side, Path: path, Side: side, Line: line}
}

func TestParseHunkHeader(t *testing.T) {
	oldStart, newStart, ok := parseHunkHeader("@@ -1,4 +1,5 @@ trailing")
	require.True(t, ok)
	assert.Equal(t, int64(1), oldStart)
	assert.Equal(t, int64(1), newStart)

	oldStart, newStart, ok = parseHunkHeader("@@ -0,0 +1,3 @@")
	require.True(t, ok)
	assert.Equal(t, int64(0), oldStart)
	assert.Equal(t, int64(1), newStart)

	oldStart, newStart, ok = parseHunkHeader("@@ -10 +10 @@")
	require.True(t, ok)
	assert.Equal(t, int64(10), oldStart)
	assert.Equal(t, int64(10), newStart)

	for _, bad := range []string{"", "@@ -x +1 @@", "@@ -1 @@", "+++ b/a.go", "@@ -1,2 + @@", "@@ --1,2 +1,2 @@@"} {
		_, _, ok = parseHunkHeader(bad)
		assert.False(t, ok, bad)
	}
}

func TestProveInlineAnchorsAcceptsAddedDeletedContext(t *testing.T) {
	diffs := map[string][]byte{
		"a.go":   []byte(anchorsModifyDiff),
		"b.go":   []byte(anchorsBlankDiff),
		"new.go": []byte(anchorsAddedDiff),
		"old.go": []byte(anchorsDeletedDiff),
	}
	// Added, deleted and context lines anchor on their native side;
	// context lines anchor on either side; the no-count hunk form and
	// blank context lines count.
	anchors := []inlineAnchor{
		anchorFor("a.go", issues_model.ExactReviewCommentSideNew, 2),
		anchorFor("a.go", issues_model.ExactReviewCommentSideOld, 2),
		anchorFor("a.go", issues_model.ExactReviewCommentSideNew, 1),
		anchorFor("a.go", issues_model.ExactReviewCommentSideOld, 4),
		anchorFor("a.go", issues_model.ExactReviewCommentSideNew, 10),
		anchorFor("b.go", issues_model.ExactReviewCommentSideOld, 2),
		anchorFor("b.go", issues_model.ExactReviewCommentSideNew, 2),
		anchorFor("new.go", issues_model.ExactReviewCommentSideNew, 3),
		anchorFor("old.go", issues_model.ExactReviewCommentSideOld, 1),
	}
	for i := range anchors {
		anchors[i].Key = "k" + string(rune('a'+i))
	}
	require.NoError(t, proveInlineAnchors(diffs, anchors))
}

func TestProveInlineAnchorsRefuses(t *testing.T) {
	diffs := map[string][]byte{
		"a.go":     []byte(anchorsModifyDiff),
		"new.go":   []byte(anchorsAddedDiff),
		"old.go":   []byte(anchorsDeletedDiff),
		"img.png":  []byte(anchorsBinaryDiff),
		"run.sh":   []byte(anchorsModeOnlyDiff),
		"renamed?": []byte(""),
	}
	cases := []struct {
		name   string
		anchor inlineAnchor
		reason string
	}{
		{"added line claimed old", anchorFor("new.go", issues_model.ExactReviewCommentSideOld, 1), "invalid inline anchor"},
		{"deleted line claimed new", anchorFor("old.go", issues_model.ExactReviewCommentSideNew, 1), "invalid inline anchor"},
		{"line outside hunks", anchorFor("a.go", issues_model.ExactReviewCommentSideNew, 8), "invalid inline anchor"},
		{"old line outside hunks", anchorFor("a.go", issues_model.ExactReviewCommentSideOld, 9), "invalid inline anchor"},
		{"binary file", anchorFor("img.png", issues_model.ExactReviewCommentSideNew, 1), "binary-only inline anchor"},
		{"mode-only change", anchorFor("run.sh", issues_model.ExactReviewCommentSideNew, 1), "invalid inline anchor"},
		{"path with no diff", anchorFor("missing.go", issues_model.ExactReviewCommentSideNew, 1), "invalid inline anchor"},
		{"empty diff", anchorFor("renamed?", issues_model.ExactReviewCommentSideNew, 1), "invalid inline anchor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := proveInlineAnchors(diffs, []inlineAnchor{tc.anchor})
			require.Error(t, err)
			refused, ok := err.(issues_model.ErrExactReviewRefused)
			require.True(t, ok, "expected exact refusal, got %v", err)
			assert.Contains(t, refused.Reason, tc.reason)
			assert.Equal(t, "native_refused", mapExactReviewRefusal(err))
		})
	}
}

func TestCutInlineExcerpt(t *testing.T) {
	previous := setting.UI.CodeCommentLines
	setting.UI.CodeCommentLines = 4
	defer func() { setting.UI.CodeCommentLines = previous }()

	excerpt := cutInlineExcerpt([]byte(anchorsModifyDiff), anchorFor("a.go", issues_model.ExactReviewCommentSideNew, 2))
	require.NotEmpty(t, excerpt)
	assert.Contains(t, excerpt, "new2")

	oldExcerpt := cutInlineExcerpt([]byte(anchorsModifyDiff), anchorFor("a.go", issues_model.ExactReviewCommentSideOld, 2))
	require.NotEmpty(t, oldExcerpt)
	assert.Contains(t, oldExcerpt, "old2")
}

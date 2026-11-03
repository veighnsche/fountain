// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateThreadResolvePayload(t *testing.T) {
	valid := ThreadResolvePayload{
		PullRequestNumber:  3,
		HeadRepositoryID:   7,
		HeadRef:            "refs/heads/feature",
		BaseRef:            "refs/heads/main",
		ExpectedHeadOID:    strings.Repeat("1", 40),
		ExpectedBaseOID:    strings.Repeat("2", 40),
		RootCommentID:      11,
		ConversationDigest: strings.Repeat("a", 64),
		Resolved:           true,
	}
	if err := ValidateThreadResolvePayload(7, valid); err != nil {
		t.Fatalf("valid thread-resolve payload refused: %v", err)
	}
	cases := map[string]ThreadResolvePayload{
		"zero pr number": func() ThreadResolvePayload { p := valid; p.PullRequestNumber = 0; return p }(),
		"zero root":      func() ThreadResolvePayload { p := valid; p.RootCommentID = 0; return p }(),
		"fork source":    func() ThreadResolvePayload { p := valid; p.HeadRepositoryID = 9; return p }(),
		"identical refs": func() ThreadResolvePayload { p := valid; p.BaseRef = "refs/heads/feature"; return p }(),
		"short head ref": func() ThreadResolvePayload { p := valid; p.HeadRef = "feature"; return p }(),
		"bad head oid":   func() ThreadResolvePayload { p := valid; p.ExpectedHeadOID = "xyz"; return p }(),
		"identical oids": func() ThreadResolvePayload { p := valid; p.ExpectedBaseOID = p.ExpectedHeadOID; return p }(),
		"short digest":   func() ThreadResolvePayload { p := valid; p.ConversationDigest = "abc"; return p }(),
		"unresolve":      func() ThreadResolvePayload { p := valid; p.Resolved = false; return p }(),
	}
	for name, payload := range cases {
		if err := ValidateThreadResolvePayload(7, payload); !errors.Is(err, ErrInvalidThreadResolvePayload) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestResolveConversationDigestMatchesFactoryVector(t *testing.T) {
	lines := []string{
		ResolveConversationLine(ResolveConversationMember{ID: 11, Body: "needs a bounds check", ContentVer: 1, Location: "review_native.go|new|42", ResolverState: "open"}),
		ResolveConversationLine(ResolveConversationMember{ID: 12, Body: "added in the next push", ContentVer: 2, Location: "review_native.go|new|42", ResolverState: "open"}),
	}
	digest, err := ResolveConversationDigest(lines)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "b0430a8696a7edc088d1da167b5246d947512ac989efda0fd4c3d3106d211e17" {
		t.Fatalf("resolve digest diverged from factory twin: %s", digest)
	}
	if _, err := ResolveConversationDigest(nil); err == nil {
		t.Fatal("memberless digest accepted")
	}
}

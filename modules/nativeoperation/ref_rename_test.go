// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"strings"
	"testing"
)

func TestRefRenameTranscriptRemainsBounded(t *testing.T) {
	for _, width := range []int{40, 64} {
		for _, target := range []RepositoryTarget{TargetCode, TargetWiki} {
			plan := RefRenamePlan{
				RepositoryID: 7, OwnerName: "owner", RepositoryName: "repo", Target: target,
				FromRef: "refs/heads/from", ToRef: "refs/heads/to", TipOID: strings.Repeat("a", width),
			}
			zero := strings.Repeat("0", width)
			deletion := RefTransactionLine{Old: plan.TipOID, New: zero, Ref: plan.FromRef}
			creation := RefTransactionLine{Old: zero, New: plan.TipOID, Ref: plan.ToRef}
			for _, phase := range []string{"prepared", "committed", "aborted"} {
				for _, lines := range [][]RefTransactionLine{{deletion}, {creation}, {deletion, creation}, {creation, deletion}, {{Old: zero, New: "ref:" + plan.ToRef, Ref: "HEAD"}}} {
					if !plan.PermitsTransaction(phase, lines) {
						t.Errorf("%s/%d rejected native %s transcript %+v", target, width, phase, lines)
					}
				}
			}
			if !plan.PermitsTransaction("aborted", []RefTransactionLine{{Old: zero, New: zero, Ref: plan.FromRef}}) {
				t.Fatal("native rename cancellation notification refused")
			}
			for _, test := range []struct {
				phase string
				lines []RefTransactionLine
			}{
				{"prepared", nil},
				{"prepared", []RefTransactionLine{{Old: zero, New: zero, Ref: plan.FromRef}}},
				{"preparing", []RefTransactionLine{deletion}},
				{"prepared", []RefTransactionLine{{Old: zero, New: plan.TipOID, Ref: "refs/heads/third"}}},
				{"prepared", []RefTransactionLine{{Old: strings.Repeat("b", width), New: zero, Ref: plan.FromRef}}},
				{"prepared", []RefTransactionLine{{Old: zero, New: strings.Repeat("b", width), Ref: plan.ToRef}}},
				{"prepared", []RefTransactionLine{{Old: zero, New: "ref:refs/heads/third", Ref: "HEAD"}}},
				{"prepared", []RefTransactionLine{deletion, creation, {Old: zero, New: plan.TipOID, Ref: "refs/heads/third"}}},
				{"prepared", []RefTransactionLine{deletion, deletion}},
			} {
				if plan.PermitsTransaction(test.phase, test.lines) {
					t.Errorf("%s/%d accepted undeclared effect %s %+v", target, width, test.phase, test.lines)
				}
			}
		}
	}
}

func TestGitExecutionOriginPreservesLegacyWithoutDowngrade(t *testing.T) {
	for _, origin := range []GitExecutionOrigin{GitOriginReceiveHTTP, GitOriginReceiveSSH, GitOriginInternalRef, GitOriginRefRename} {
		got, err := GitOrigin(string(origin), "")
		if err != nil || got != origin {
			t.Errorf("explicit origin %s: %s %v", origin, got, err)
		}
	}
	if got, err := GitOrigin("", "true"); err != nil || got != GitOriginLegacyReceive {
		t.Fatalf("legacy origin lost: %s %v", got, err)
	}
	if got, err := GitOrigin("", ""); err != nil || got != "" {
		t.Fatalf("unrelated child gained participation: %s %v", got, err)
	}
	if _, err := GitOrigin("unknown", "true"); err == nil {
		t.Fatal("unknown explicit origin downgraded to legacy")
	}
	env := AppendGitOriginEnv([]string{EnvGitOrigin + "=old", EnvExecFile + "=proof-file", "KEEP=1"}, GitOriginRefRename)
	if strings.Join(env, "|") != EnvExecFile+"=proof-file|KEEP=1|"+EnvGitOrigin+"="+string(GitOriginRefRename) {
		t.Fatalf("origin builder changed unrelated proof/environment: %v", env)
	}
}

func TestRepositoryTargetCodecKeepsLegacyIdentities(t *testing.T) {
	for _, target := range []RepositoryTarget{TargetCode, TargetWiki} {
		ref := "refs/heads/main"
		legacy := EncodeScopedRefTarget(target, ref)
		for _, explicit := range []RepositoryTarget{"", target} {
			actual, plain, err := ScopedRefRepositoryTarget(ScopedRef{Ref: legacy, RepositoryTarget: explicit})
			if err != nil || actual != target || plain != ref {
				t.Fatalf("scoped target %s/%s: %s %s %v", target, explicit, actual, plain, err)
			}
		}
		actual, plain := DecodeRefWriteTarget(EncodeRefWriteTarget(target, ref))
		if actual != target || plain != ref {
			t.Fatal("ref-write legacy target changed")
		}
	}
	if _, _, err := ScopedRefRepositoryTarget(ScopedRef{Ref: "wiki:refs/heads/main", RepositoryTarget: TargetCode}); err == nil {
		t.Fatal("contradicting code/wiki target accepted")
	}
	if _, _, err := ScopedRefRepositoryTarget(ScopedRef{Ref: "refs/heads/main", RepositoryTarget: TargetWiki}); err == nil {
		t.Fatal("unmarked code ref reinterpreted as wiki")
	}
}

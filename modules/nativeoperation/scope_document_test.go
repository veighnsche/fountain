// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package nativeoperation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const retainedScope = `{
 "kind" : "ordinary", "repository_id":7,
 "future" : { "number":1e+09, "text":"\u0061" },
 "new_oid" : "old-result", "refs" : [
 { "ref" : "refs/heads/main", "old_oid":"base", "new_oid" : "before", "future_ref" : { "number":-0.00, "text":"\u0062" } }
 ],
 "completion_ref" : "refs/pull/3/head", "completion_old_oid":"base",
 "completion_new_oid":"before", "completion_phase" : "prepared"
}`

func retainedScopeDocument(t *testing.T, raw string) *ScopeDocument {
	t.Helper()
	document, err := DecodeScopeDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestScopeDocumentIdempotentRefinementsKeepExactWitness(t *testing.T) {
	document := retainedScopeDocument(t, retainedScope)
	if document.Original() != retainedScope {
		t.Fatal("decoding rewrote the retained witness")
	}
	for name, refine := range map[string]func() (string, error){
		"strict result":   func() (string, error) { return document.WithNewOID("old-result") },
		"observed result": func() (string, error) { return document.WithRefResult("old-result") },
		"existing ref": func() (string, error) {
			return document.UpdateRefs([]ScopedRef{{Ref: "refs/heads/main", NewOID: "before"}})
		},
		"duplicate append": func() (string, error) {
			return document.AppendRefs([]ScopedRef{{Ref: "refs/heads/main", OldOID: "base", NewOID: "before"}})
		},
		"empty update": func() (string, error) { return document.UpdateRefs(nil) },
		"empty append": func() (string, error) { return document.AppendRefs(nil) },
		"completion": func() (string, error) {
			return document.WithCompletion("refs/pull/3/head", "base", "before", "prepared")
		},
		"absent rename":   func() (string, error) { return document.WithNativeRename(nil) },
		"receive options": func() (string, error) { return document.WithReceivePushOptions(false) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := refine()
			if err != nil || got != retainedScope {
				t.Fatalf("idempotent refinement changed bytes: %v\n%s", err, got)
			}
		})
	}
}

func TestScopeDocumentRefinementsPreserveUnrelatedBytes(t *testing.T) {
	document := retainedScopeDocument(t, retainedScope)
	for _, test := range []struct {
		name   string
		refine func() (string, error)
		want   string
	}{
		{"observed result", func() (string, error) { return document.WithRefResult("observed") }, strings.Replace(retainedScope, `"new_oid" : "old-result"`, `"new_oid" : "observed"`, 1)},
		{"existing ref", func() (string, error) {
			return document.UpdateRefs([]ScopedRef{{Ref: "refs/heads/main", NewOID: "after"}})
		}, strings.Replace(retainedScope, `"new_oid" : "before"`, `"new_oid" : "after"`, 1)},
		{"completion", func() (string, error) {
			return document.WithCompletion("refs/pull/3/head", "base", "before", "committed")
		}, strings.Replace(retainedScope, `"completion_phase" : "prepared"`, `"completion_phase" : "committed"`, 1)},
		{"append", func() (string, error) {
			return document.AppendRefs([]ScopedRef{{Ref: "wiki:refs/heads/topic", OldOID: "initial", NewOID: "final", RepositoryTarget: TargetWiki}})
		}, strings.Replace(retainedScope, "\n ],", "\n ,"+`{"ref":"wiki:refs/heads/topic","old_oid":"initial","new_oid":"final","repository_target":"wiki"}`+"],", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.refine()
			if err != nil || got != test.want {
				t.Fatalf("refinement changed unrelated bytes: %v\nwant %s\ngot  %s", err, test.want, got)
			}
			if !json.Valid([]byte(got)) {
				t.Fatal("refined witness is not valid JSON")
			}
			if document.Original() != retainedScope {
				t.Fatal("refinement mutated the captured witness")
			}
		})
	}
	withoutResult := strings.Replace(retainedScope, `"new_oid" : "old-result", `, "", 1)
	got, err := retainedScopeDocument(t, withoutResult).WithNewOID("allocated")
	if err != nil || !strings.Contains(got, `"new_oid":"allocated"`) || !strings.Contains(got, `"number":1e+09`) || !strings.Contains(got, `"number":-0.00`) {
		t.Fatalf("allocating a result lost future witness data: %v\n%s", err, got)
	}
}

func TestScopeDocumentRefusesConflictingRefinement(t *testing.T) {
	document := retainedScopeDocument(t, retainedScope)
	if _, err := document.WithNewOID("another-result"); !errors.Is(err, ErrScopeConflict) {
		t.Fatalf("strict result accepted conflicting proof: %v", err)
	}
	for _, ref := range []ScopedRef{
		{Ref: "refs/heads/main", OldOID: "another-base", NewOID: "before"},
		{Ref: "refs/heads/main", OldOID: "base", NewOID: "another-result"},
	} {
		if _, err := document.AppendRefs([]ScopedRef{ref}); !errors.Is(err, ErrScopeConflict) {
			t.Fatalf("append replaced an existing proof: %v", err)
		}
	}
}

func TestScopeDocumentRefinementBindsRepositoryTarget(t *testing.T) {
	for _, test := range []struct {
		name, recordedRef string
		recordedTarget    RepositoryTarget
		nextTarget        RepositoryTarget
		conflict          bool
	}{
		{"legacy code", "refs/heads/main", "", TargetCode, false},
		{"legacy wiki", "wiki:refs/heads/main", "", TargetWiki, false},
		{"recorded code", "refs/heads/main", TargetCode, TargetWiki, true},
		{"recorded wiki", "wiki:refs/heads/main", TargetWiki, TargetCode, true},
		{"code label with wiki target", "refs/heads/main", "", TargetWiki, true},
		{"wiki label with code target", "wiki:refs/heads/main", "", TargetCode, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := ScopedRef{Ref: test.recordedRef, OldOID: "before", NewOID: "after", RepositoryTarget: test.recordedTarget}
			raw, err := EncodeScope(Scope{Kind: OwnerOrdinary, Refs: []ScopedRef{ref}})
			if err != nil {
				t.Fatal(err)
			}
			document := retainedScopeDocument(t, raw)
			ref.RepositoryTarget = test.nextTarget
			for _, refine := range []func([]ScopedRef) (string, error){document.AppendRefs, document.UpdateRefs} {
				got, err := refine([]ScopedRef{ref})
				if test.conflict {
					if !errors.Is(err, ErrScopeConflict) {
						t.Fatalf("contradictory target accepted: %v", err)
					}
				} else if err != nil || got != raw {
					t.Fatalf("consistent target rewrote the legacy witness: %v\n%s", err, got)
				}
			}
		})
	}
}

func TestScopeDocumentRemovesOnlyNativeRename(t *testing.T) {
	const plan = `{"repository_id":7,"owner_name":"owner","repository_name":"repo","target":"code","from_ref":"refs/heads/old","to_ref":"refs/heads/new","tip_oid":"1111111111111111111111111111111111111111"}`
	for _, test := range []struct{ name, raw, want string }{
		{"first", `{"native_rename":` + plan + `, "future" : {"n":1e+09}, "kind":"ordinary"}`, `{ "future" : {"n":1e+09}, "kind":"ordinary"}`},
		{"middle", `{"future" : {"n":1e+09}, "native_rename":` + plan + `, "kind":"ordinary"}`, `{"future" : {"n":1e+09}, "kind":"ordinary"}`},
		{"last", `{"future" : {"n":1e+09}, "kind":"ordinary", "native_rename":` + plan + `}`, `{"future" : {"n":1e+09}, "kind":"ordinary"}`},
		{"only", `{"native_rename":` + plan + `}`, `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := retainedScopeDocument(t, test.raw).WithNativeRename(nil)
			if err != nil || got != test.want || !json.Valid([]byte(got)) {
				t.Fatalf("removing rename changed remaining witness: %v\nwant %s\ngot  %s", err, test.want, got)
			}
		})
	}
}

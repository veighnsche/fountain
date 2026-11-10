// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package gitsyntax defines the structural Git names carried by the native
// operation protocol. Object existence and permission remain host decisions.
package gitsyntax

import "strings"

const BranchPrefix = "refs/heads/"

func FullOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		if c < '0' || c > '9' {
			if (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

func BranchRef(ref string) bool {
	return strings.HasPrefix(ref, BranchPrefix) && len(ref) <= 512 &&
		strings.TrimSpace(ref) == ref && !strings.ContainsAny(ref, " ~^:?*\\") &&
		!strings.Contains(ref, "..") && len(ref) > len(BranchPrefix)
}

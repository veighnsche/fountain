// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package navigationpolicy owns finite navigation exclusions and route limits.
// URL parsing and confinement remain the responsibility of each trust boundary.
package navigationpolicy

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed policy.json
var policyJSON []byte

type navigationPolicy struct {
	AuthPathSegments        [][]string `json:"auth_path_segments"`
	CredentialQueryKeys     []string   `json:"credential_query_keys"`
	CredentialQuerySuffix   string     `json:"credential_query_suffix"`
	OpaqueRouteSegmentBytes int        `json:"opaque_route_segment_bytes"`
}

var policy = func() navigationPolicy {
	var value navigationPolicy
	if err := json.Unmarshal(policyJSON, &value); err != nil {
		panic(err)
	}
	return value
}()

func MaxOpaqueRouteSegmentBytes() int { return policy.OpaqueRouteSegmentBytes }

// IsAuthPath checks decoded path segments at complete segment boundaries.
func IsAuthPath(pathname string) bool {
	segments := strings.Split(pathname, "/")
	for _, auth := range policy.AuthPathSegments {
		for start := 0; start+len(auth) <= len(segments); start++ {
			matched := true
			for i, segment := range auth {
				if segments[start+i] != segment {
					matched = false
					break
				}
			}
			if matched {
				return true
			}
		}
	}
	return false
}

func IsCredentialQueryKey(key string) bool {
	key = strings.ToLower(key)
	for _, credential := range policy.CredentialQueryKeys {
		if key == credential {
			return true
		}
	}
	return strings.HasSuffix(key, policy.CredentialQuerySuffix)
}

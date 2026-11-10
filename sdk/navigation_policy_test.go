// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"net/url"
	"strings"
	"testing"
)

func TestDeclaredSubrouteUsesDecodedUTF8ByteLimit(t *testing.T) {
	page := Page{Subroutes: []Subroute{{Pattern: "requests/{request}"}}}
	for _, test := range []struct {
		name, value string
		accepted    bool
	}{
		{"ascii 255", strings.Repeat("a", 255), true},
		{"ascii 256", strings.Repeat("a", 256), false},
		{"unicode 255", strings.Repeat("界", 85), true},
		{"unicode 256", strings.Repeat("界", 85) + "a", false},
		{"astral 255", strings.Repeat("😀", 63) + "abc", true},
		{"astral 256", strings.Repeat("😀", 64), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, escaped := range []bool{false, true} {
				value := test.value
				if escaped {
					parsed, err := url.Parse("/forge/workspace/requests/" + url.PathEscape(value))
					if err != nil {
						t.Fatal(err)
					}
					value = strings.TrimPrefix(parsed.Path, "/forge/workspace/requests/")
				}
				for _, trailing := range []string{"", "/"} {
					_, params, ok := page.MatchSubroute("requests/" + value + trailing)
					if ok != test.accepted || ok && params["request"] != test.value {
						t.Fatalf("wrong decoded byte admission: escaped=%v trailing=%q bytes=%d accepted=%v", escaped, trailing, len(value), ok)
					}
				}
			}
		})
	}
}

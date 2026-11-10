// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	extension "forgejo.org/extension-sdk"
)

func TestMetadataJSONContract(t *testing.T) {
	var output bytes.Buffer
	if err := writeMetadata(&output); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("{\"extension_protocol\":%d,\"browser_contract\":%d}\n", extension.Protocol, extension.BrowserContractVersion)
	if output.String() != want {
		t.Fatalf("metadata = %q, want %q", output.String(), want)
	}
	if err := writeMetadata(rejectedOutput{}); !errors.Is(err, errRejectedOutput) {
		t.Fatalf("metadata output failure was hidden: %v", err)
	}
}

var errRejectedOutput = errors.New("metadata output is closed")

type rejectedOutput struct{}

func (rejectedOutput) Write([]byte) (int, error) { return 0, errRejectedOutput }

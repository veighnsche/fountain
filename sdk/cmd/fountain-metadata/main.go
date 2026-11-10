// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	extension "forgejo.org/extension-sdk"
)

func writeMetadata(out io.Writer) error {
	return json.NewEncoder(out).Encode(struct {
		ExtensionProtocol int `json:"extension_protocol"`
		BrowserContract   int `json:"browser_contract"`
	}{ExtensionProtocol: extension.Protocol, BrowserContract: extension.BrowserContractVersion})
}

func main() {
	if err := writeMetadata(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

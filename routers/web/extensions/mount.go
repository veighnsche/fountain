// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"encoding/json" //nolint:depguard // Mount datasets use the native wire JSON codec.
	"strconv"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/modules/setting"
)

type mountDescriptor struct {
	Mode, ExtensionID, ContributionID, OwnerID       string
	Entry, APIBase, AssetBase, AppSubURL             string
	ContractVersion, SessionGeneration, RepositoryID string
	DirectPatterns                                   string
}

func (descriptor mountDescriptor) WithRepositoryID(value any) mountDescriptor {
	descriptor.RepositoryID, _ = value.(string)
	return descriptor
}

type panelPresentation struct {
	Link
	Mount mountDescriptor
}

func contributionMount(mode string, link Link, generation, repositoryID string, patterns []string) mountDescriptor {
	descriptor := mountDescriptor{
		Mode: mode, ExtensionID: link.ExtensionID, ContributionID: link.ID,
		OwnerID: link.OwnerID, Entry: link.Entry, APIBase: link.APIBase, AssetBase: link.AssetBase,
		AppSubURL: setting.AppSubURL, ContractVersion: strconv.Itoa(extension.BrowserContractVersion),
		SessionGeneration: generation, RepositoryID: repositoryID,
	}
	if len(patterns) != 0 {
		encoded, _ := json.Marshal(patterns)
		descriptor.DirectPatterns = string(encoded)
	}
	return descriptor
}

func pageDirectPatterns(page extension.Page) []string {
	if page.Scope != "global" || page.Route == "" {
		return nil
	}
	root := "/" + page.Route
	patterns := []string{root}
	for _, subroute := range page.Subroutes {
		patterns = append(patterns, root+"/"+subroute.Pattern)
	}
	return patterns
}

func ownerPresented(extensionID, ownerID string, panels []Link) bool {
	if ownerID == "" {
		return false
	}
	for _, panel := range panels {
		if panel.ExtensionID == extensionID && panel.OwnerID == ownerID {
			return true
		}
	}
	return false
}

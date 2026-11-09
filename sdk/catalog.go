// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
)

// Trusted extension catalog (RCX-2 F01 frozen wire). The administrator
// chooses only among the release's declared packages; this scope adds no
// arbitrary URL installation, marketplace or package solver. Each entry is
// one gzip-compressed archive of Fountain's native package directory
// (manifest, executable and assets) with its exact size and SHA-256.
// Acquisition verifies both before activation. Generic only: no
// product-specific fields or capabilities.
const CatalogVersion = 1

// MaximumArchiveBytes bounds one catalog archive: 512 MiB, matching the
// installer package cap.
const MaximumArchiveBytes = 512 << 20

// maximumCatalogBytes bounds one catalog document: 1 MiB.
const maximumCatalogBytes = 1 << 20

// maximumCatalogPackages bounds one catalog: 256 entries.
const maximumCatalogPackages = 256

// maximumPackageIDBytes caps a catalog package ID: 64 bytes.
const maximumPackageIDBytes = 64

// packageVersionPattern is the frozen package version shape:
// [A-Za-z0-9][A-Za-z0-9._+-]{0,63}.
var packageVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// CatalogPackage declares one installable package revision.
// LifecycleController names the required package that controls this
// package's external effects; it is omitted for packages with no external
// effects (including lifecycle controllers themselves, which cannot target
// another controller).
type CatalogPackage struct {
	ID                  string   `json:"id"`
	Version             string   `json:"version"`
	URL                 string   `json:"url"`
	Size                int64    `json:"size"`
	SHA256              string   `json:"sha256"`
	Required            bool     `json:"required"`
	FountainProto       int      `json:"fountain_protocol"`
	BrowserContract     int      `json:"browser_contract"`
	Capabilities        []string `json:"capabilities,omitempty"`
	LifecycleController string   `json:"lifecycle_controller,omitempty"`
}

// Catalog is the release's declared package set.
type Catalog struct {
	Version  int              `json:"version"`
	Packages []CatalogPackage `json:"packages"`
}

// validPackageVersion reports whether a package version matches the frozen
// shape shared by catalog entries, manifests and lifecycle pins.
func validPackageVersion(value string) bool {
	return packageVersionPattern.MatchString(value)
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

// validCatalogURL checks an initial catalog URL: HTTPS with a nonempty
// host and no user info, query or fragment. Signed CDN query strings are
// allowed on redirects, never in the declared entry.
func validCatalogURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return true
}

// catalogCapabilities is the recognized SDK capability set for catalog
// entries. Generic only: no product-specific capabilities are invented.
func catalogCapabilities() map[string]bool {
	return map[string]bool{
		CapabilityActorRead:               true,
		CapabilityRepositoryRead:          true,
		CapabilityRepositoryIssuesRead:    true,
		CapabilityOwnedRepositoriesSearch: true,
		CapabilityOrganizationOwnership:   true,
		CapabilityPublicKeysRead:          true,
		CapabilityContributionAuthorize:   true,
		CapabilityServiceBridge:           true,
		CapabilityBackgroundOperations:    true,
		CapabilityIssueIntake:             true,
		CapabilityRuntimeControl:          true,
		CapabilityHumanComments:           true,
		CapabilityHumanEdits:              true,
		CapabilityHumanStates:             true,
		CapabilityHumanReviews:            true,
		CapabilityHumanMerges:             true,
		CapabilityHumanLocks:              true,
		CapabilityHumanIssueCreate:        true,
		CapabilityHumanPullCreate:         true,
		CapabilityExtensionLifecycle:      true,
	}
}

// ValidateCatalogPackage checks one entry: identity, exact bytes and digest,
// HTTPS location, compatibility with this host's contracts, and the
// lifecycle controller reference shape. Cross-entry rules (required-set
// equality, controller-is-required, acyclicity) need the release
// configuration and live manager state, so they stay with the caller.
func ValidateCatalogPackage(entry CatalogPackage) error {
	if !ValidID(entry.ID) || len(entry.ID) > maximumPackageIDBytes {
		return errors.New("invalid catalog package id")
	}
	if !validPackageVersion(entry.Version) {
		return fmt.Errorf("invalid catalog package version for %q", entry.ID)
	}
	if !validCatalogURL(entry.URL) {
		return fmt.Errorf("catalog package %q requires an https url without user info, query or fragment", entry.ID)
	}
	if entry.Size < 1 || entry.Size > MaximumArchiveBytes {
		return fmt.Errorf("invalid catalog package size for %q", entry.ID)
	}
	if !validSHA256Hex(entry.SHA256) {
		return fmt.Errorf("invalid catalog package digest for %q", entry.ID)
	}
	if entry.FountainProto != Protocol || entry.BrowserContract != BrowserContractVersion {
		return fmt.Errorf("catalog package %q is incompatible with this host", entry.ID)
	}
	if err := validateNames(entry.Capabilities, catalogCapabilities(), "capability"); err != nil {
		return err
	}
	if entry.LifecycleController != "" {
		if !ValidID(entry.LifecycleController) || len(entry.LifecycleController) > maximumPackageIDBytes {
			return fmt.Errorf("catalog package %q has an invalid lifecycle controller", entry.ID)
		}
		if entry.LifecycleController == entry.ID {
			return fmt.Errorf("catalog package %q cannot control itself", entry.ID)
		}
	}
	return nil
}

// ValidateCatalog checks the envelope and every entry; IDs must be unique.
func ValidateCatalog(catalog Catalog) error {
	if catalog.Version != CatalogVersion {
		return errors.New("unsupported catalog version")
	}
	if len(catalog.Packages) > maximumCatalogPackages {
		return errors.New("catalog declares too many packages")
	}
	seen := make(map[string]bool, len(catalog.Packages))
	for _, entry := range catalog.Packages {
		if seen[entry.ID] {
			return fmt.Errorf("duplicate catalog package %q", entry.ID)
		}
		seen[entry.ID] = true
		if err := ValidateCatalogPackage(entry); err != nil {
			return err
		}
	}
	return nil
}

// FindCatalogPackage returns the declared entry for an ID, or false.
func FindCatalogPackage(catalog Catalog, id string) (CatalogPackage, bool) {
	for _, entry := range catalog.Packages {
		if entry.ID == id {
			return entry, true
		}
	}
	return CatalogPackage{}, false
}

// requiredCatalogPackageFields must all be present in every catalog package
// object. "required" is presence-checked so an omitted boolean is never
// mistaken for an explicit false.
var requiredCatalogPackageFields = []string{
	"id", "version", "url", "size", "sha256", "required",
	"fountain_protocol", "browser_contract", "capabilities",
}

// DecodeCatalog strictly decodes one catalog document: unknown fields,
// duplicate object keys at any depth, trailing data and wrong scalar types
// are rejected, every required field must be present, and the decoded value
// must validate. data is bounded at 1 MiB.
func DecodeCatalog(data []byte) (Catalog, error) {
	if len(data) == 0 || len(data) > maximumCatalogBytes {
		return Catalog{}, errors.New("catalog document exceeds size limit")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Catalog{}, errors.New("catalog document must be an object")
	}
	if err := rejectDuplicateKeys(trimmed, "catalog document"); err != nil {
		return Catalog{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var raw struct {
		Version  int               `json:"version"`
		Packages []json.RawMessage `json:"packages"`
	}
	if err := decoder.Decode(&raw); err != nil {
		return Catalog{}, fmt.Errorf("decode catalog: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Catalog{}, errors.New("catalog document must contain one object")
	}
	present, err := objectKeys(trimmed)
	if err != nil {
		return Catalog{}, err
	}
	for _, field := range []string{"version", "packages"} {
		if !present[field] {
			return Catalog{}, fmt.Errorf("catalog is missing required field %q", field)
		}
	}
	var catalog Catalog
	catalog.Version = raw.Version
	for _, entryRaw := range raw.Packages {
		fields, err := objectKeys(bytes.TrimSpace(entryRaw))
		if err != nil {
			return Catalog{}, errors.New("catalog package must be an object")
		}
		for _, field := range requiredCatalogPackageFields {
			if !fields[field] {
				return Catalog{}, fmt.Errorf("catalog package is missing required field %q", field)
			}
		}
		entryDecoder := json.NewDecoder(bytes.NewReader(entryRaw))
		entryDecoder.DisallowUnknownFields()
		var entry CatalogPackage
		if err := entryDecoder.Decode(&entry); err != nil {
			return Catalog{}, fmt.Errorf("decode catalog package: %w", err)
		}
		if entry.Capabilities == nil {
			return Catalog{}, errors.New("catalog package capabilities must be an array")
		}
		catalog.Packages = append(catalog.Packages, entry)
	}
	if err := ValidateCatalog(catalog); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

// objectKeys returns the key set of one JSON object document.
func objectKeys(data []byte) (map[string]bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("not a JSON object")
	}
	fields := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid JSON object key")
		}
		fields[key] = true
		var skipped any
		if err := decoder.Decode(&skipped); err != nil {
			return nil, err
		}
	}
	return fields, nil
}

// rejectDuplicateKeys rejects any JSON object with a repeated key, at any
// depth. encoding/json silently keeps the last duplicate, so strict wire
// decoding must detect them explicitly.
func rejectDuplicateKeys(data []byte, what string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var check func() error
	check = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch token := token.(type) {
		case json.Delim:
			switch token {
			case '{':
				seen := make(map[string]bool)
				for decoder.More() {
					keyToken, err := decoder.Token()
					if err != nil {
						return err
					}
					key, ok := keyToken.(string)
					if !ok {
						return errors.New("invalid JSON object key")
					}
					if seen[key] {
						return fmt.Errorf("duplicate JSON object key %q", key)
					}
					seen[key] = true
					if err := check(); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			case '[':
				for decoder.More() {
					if err := check(); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			default:
				return nil
			}
		default:
			return nil
		}
	}
	if err := check(); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s must contain one value", what)
	}
	return nil
}

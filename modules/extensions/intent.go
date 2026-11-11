// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/extension-sdk/wirejson"

	"github.com/google/uuid"
)

// Intent operations are interruption markers, not a durable queue. They
// must agree with the desired boolean: activate pairs with desired=true,
// deactivate with desired=false, and null means no interrupted effect.
const (
	IntentActivate   = "activate"
	IntentDeactivate = "deactivate"
)

// IntentErrorKind separates invalid state/input, stale CAS and exhausted
// generations from their diagnostic prose.
type IntentErrorKind uint8

const (
	IntentInvalid IntentErrorKind = iota
	IntentStale
	IntentExhausted
)

type IntentError struct {
	Kind    IntentErrorKind
	message string
}

func (e *IntentError) Error() string { return e.message }

func intentError(kind IntentErrorKind, message string) error {
	return &IntentError{Kind: kind, message: message}
}

// maximumIntentBytes bounds one persisted intent document: 4 KiB.
const maximumIntentBytes = 4 << 10

// Intent is the sole authoritative optional-package requested-state
// record (RCX-2 F02). It replaces the experimental .disabled marker and
// the contradictory .lifecycle requested-state copies; no legacy readers
// are retained. Generation is a canonical unsigned-64 decimal string:
// persisted generations start at 1, while an absent optional intent is a
// virtual generation "0", disabled, with no installation UUID. The record
// deliberately contains no version or digest: desired state survives a
// release's package replacement while catalog and installed receipt bind
// versions. Last error text and progress stay advisory in-memory state,
// never another persisted authority.
type Intent struct {
	Schema           int     `json:"schema"`
	PackageID        string  `json:"package_id"`
	InstallationID   string  `json:"installation_id"`
	Generation       string  `json:"generation"`
	DesiredEnabled   bool    `json:"desired_enabled"`
	PendingOperation *string `json:"pending_operation"`
}

// IntentPath returns the authoritative requested-state file for a package.
func IntentPath(root, id string) string {
	return filepath.Join(root, ".intents", id+".json")
}

// VirtualIntent returns the absent-intent reading: generation "0",
// disabled, no installation UUID, no pending operation.
func VirtualIntent(id string) Intent {
	return Intent{Schema: 1, PackageID: id, Generation: "0"}
}

// ParseGeneration validates a canonical unsigned-64 decimal generation
// string. Generations avoid browser integer precision loss by staying
// strings on the wire; only canonical decimal without leading zeros (other
// than "0" itself) is accepted.
func ParseGeneration(generation string) (uint64, error) {
	if generation == "" || (len(generation) > 1 && generation[0] == '0') {
		return 0, intentError(IntentInvalid, "extension intent generation is invalid")
	}
	value, err := strconv.ParseUint(generation, 10, 64)
	if err != nil {
		return 0, intentError(IntentInvalid, "extension intent generation is invalid")
	}
	return value, nil
}

// validatePending checks that a pending operation agrees with the desired
// boolean.
func validatePending(desired bool, pending *string) error {
	if pending == nil {
		return nil
	}
	switch *pending {
	case IntentActivate:
		if !desired {
			return intentError(IntentInvalid, "extension intent activation requires desired enabled")
		}
	case IntentDeactivate:
		if desired {
			return intentError(IntentInvalid, "extension intent deactivation requires desired disabled")
		}
	default:
		return intentError(IntentInvalid, "extension intent operation is invalid")
	}
	return nil
}

// LoadIntent returns the authoritative requested state. Only a missing
// file reads as the virtual generation-"0" disabled intent. A persisted
// document must carry every field exactly once with the right types, and
// a persisted generation of at least 1: an on-disk generation "0" is
// malformed, never absence. A malformed file, a mismatched installation
// record, or a state that disagrees with itself is a hard error: such
// state is unavailable, never a default-enabled or silently fresh
// installation. Omitted booleans are never read as false.
func LoadIntent(root, id string) (Intent, error) {
	if !sdk.ValidID(id) {
		return Intent{}, intentError(IntentInvalid, "invalid extension id")
	}
	file, err := os.Open(IntentPath(root, id))
	if errors.Is(err, os.ErrNotExist) {
		return VirtualIntent(id), nil
	}
	if err != nil {
		return Intent{}, errors.New("extension intent is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumIntentBytes+1))
	if err != nil {
		return Intent{}, intentError(IntentInvalid, "extension intent is invalid")
	}
	if len(data) > maximumIntentBytes {
		return Intent{}, intentError(IntentInvalid, "extension intent exceeds size limit")
	}
	if err := checkIntentDocument(data); err != nil {
		return Intent{}, err
	}
	var intent Intent
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return Intent{}, intentError(IntentInvalid, "extension intent is invalid")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Intent{}, intentError(IntentInvalid, "extension intent is invalid")
	}
	if err := validateIntent(root, intent, id, false); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

// intentRequiredFields must each appear exactly once in every persisted
// intent document. pending_operation is required but nullable; desired
// state never defaults from an omitted field.
var intentRequiredFields = []string{
	"schema", "package_id", "installation_id",
	"generation", "desired_enabled", "pending_operation",
}

// checkIntentDocument enforces document shape before typed decoding:
// one JSON object, no duplicate keys at any depth, and every required
// top-level field present. Wrong scalar types fail in the typed decode.
func checkIntentDocument(data []byte) error {
	fields, err := wirejson.ObjectFields(data, "extension intent")
	if err != nil {
		return intentError(IntentInvalid, "extension intent is invalid")
	}
	if err := wirejson.RequireFields(fields, "extension intent", intentRequiredFields, []string{"desired_enabled"}); err != nil {
		return intentError(IntentInvalid, "extension intent is invalid")
	}
	return nil
}

// validateIntent checks strict fields, the generation shape, pending
// agreement, a valid installation UUID, and a matching installation
// record. allowVirtual permits the absent-intent reading for callers that
// load through LoadIntent; persisted intents always carry generation >= 1.
func validateIntent(root string, intent Intent, id string, allowVirtual bool) error {
	if intent.Schema != 1 {
		return intentError(IntentInvalid, "extension intent is invalid")
	}
	if intent.PackageID != id {
		return intentError(IntentInvalid, "extension intent is invalid")
	}
	generation, err := ParseGeneration(intent.Generation)
	if err != nil {
		return err
	}
	if generation == 0 && !allowVirtual {
		return intentError(IntentInvalid, "extension intent generation is invalid")
	}
	if err := validatePending(intent.DesiredEnabled, intent.PendingOperation); err != nil {
		return err
	}
	if generation == 0 {
		if intent.InstallationID != "" || intent.DesiredEnabled || intent.PendingOperation != nil {
			return intentError(IntentInvalid, "extension intent is invalid")
		}
		return nil
	}
	if _, err := uuid.Parse(intent.InstallationID); err != nil {
		return intentError(IntentInvalid, "extension intent installation is invalid")
	}
	recorded, err := LoadInstallation(root, id)
	if err != nil {
		return errors.New("extension intent installation is unavailable")
	}
	if recorded != intent.InstallationID {
		return intentError(IntentInvalid, "extension intent installation does not match")
	}
	return nil
}

// StoreIntent atomically persists requested state under
// compare-and-swap: the recorded generation must equal expectedGeneration
// ("0" for a first intent), and the persisted generation is exactly one
// higher. The write lands a sibling mode-0600 file, fsyncs it, renames,
// then fsyncs the directory before any external effect may follow; a
// persistence failure aborts before effects. Clearing or setting a pending
// interruption marker goes through this same path.
func StoreIntent(root, id, installationID string, desired bool, pending *string, expectedGeneration string) (Intent, error) {
	if !sdk.ValidID(id) {
		return Intent{}, intentError(IntentInvalid, "invalid extension id")
	}
	value, err := ParseGeneration(expectedGeneration)
	if err != nil {
		return Intent{}, err
	}
	current, err := LoadIntent(root, id)
	if err != nil {
		return Intent{}, err
	}
	if current.Generation != expectedGeneration {
		return Intent{}, intentError(IntentStale, "extension intent changed; reload and retry with the current generation")
	}
	if value == ^uint64(0) {
		return Intent{}, intentError(IntentExhausted, "extension intent generation exhausted")
	}
	intent := Intent{
		Schema:           1,
		PackageID:        id,
		InstallationID:   installationID,
		Generation:       strconv.FormatUint(value+1, 10),
		DesiredEnabled:   desired,
		PendingOperation: pending,
	}
	if err := validateIntent(root, intent, id, false); err != nil {
		return Intent{}, err
	}
	if err := writeIntent(root, intent); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

// StorePendingSameGeneration sets or clears the interruption marker at
// the recorded generation without incrementing it. An explicit accepted
// admin retry increments exactly once through StoreIntent; pending begin,
// successful completion, and boot reconciliation preserve the
// generation, installation UUID, and desired flag through this path. It
// uses the same compare-and-swap and the same atomic write as
// StoreIntent: the recorded generation must equal expectedGeneration,
// and persistence failure aborts before effects. There is no virtual
// generation to mark: pending operations require a persisted intent.
func StorePendingSameGeneration(root, id string, desired bool, pending *string, expectedGeneration string) (Intent, error) {
	if !sdk.ValidID(id) {
		return Intent{}, intentError(IntentInvalid, "invalid extension id")
	}
	if _, err := ParseGeneration(expectedGeneration); err != nil {
		return Intent{}, err
	}
	current, err := LoadIntent(root, id)
	if err != nil {
		return Intent{}, err
	}
	if current.Generation != expectedGeneration {
		return Intent{}, intentError(IntentStale, "extension intent changed; reload and retry with the current generation")
	}
	if current.Generation == "0" {
		return Intent{}, intentError(IntentInvalid, "extension intent has no persisted generation")
	}
	next := Intent{
		Schema:           1,
		PackageID:        id,
		InstallationID:   current.InstallationID,
		Generation:       current.Generation,
		DesiredEnabled:   desired,
		PendingOperation: pending,
	}
	if err := validateIntent(root, next, id, false); err != nil {
		return Intent{}, err
	}
	if err := writeIntent(root, next); err != nil {
		return Intent{}, err
	}
	return next, nil
}

// writeIntent performs the atomic durable write: sibling mode-0600 file,
// file fsync, rename, directory fsync.
func writeIntent(root string, intent Intent) error {
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	directory := filepath.Join(root, ".intents")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("extension intent directory unavailable")
	}
	temporary, err := os.CreateTemp(directory, ".intent-")
	if err != nil {
		return errors.New("extension intent is unavailable")
	}
	temporaryName := temporary.Name()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
		return errors.New("extension intent is unavailable")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
		return errors.New("extension intent is unavailable")
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return errors.New("extension intent is unavailable")
	}
	if err := os.Chmod(temporaryName, 0o600); err != nil {
		_ = os.Remove(temporaryName)
		return err
	}
	if err := os.Rename(temporaryName, IntentPath(root, intent.PackageID)); err != nil {
		_ = os.Remove(temporaryName)
		return errors.New("extension intent is unavailable")
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return errors.New("extension intent is unavailable")
	}
	defer directoryFile.Close()
	if err := directoryFile.Sync(); err != nil {
		return errors.New("extension intent is unavailable")
	}
	return nil
}

// listIntents returns the IDs carrying a persisted intent. A missing
// directory means no intents; other read failures are errors.
func listIntents(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, ".intents"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if sdk.ValidID(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// RemoveIntent retires a package's requested state. It is idempotent: a
// missing file is not an error.
func RemoveIntent(root, id string) error {
	if !sdk.ValidID(id) {
		return intentError(IntentInvalid, "invalid extension id")
	}
	if err := os.Remove(IntentPath(root, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(IntentControllerPath(root, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

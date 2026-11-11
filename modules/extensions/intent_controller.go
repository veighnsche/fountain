// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"encoding/json" //nolint:depguard // Bounded host-owned attribution sidecar, separate from the immutable SDK intent.
	"errors"
	"io"
	"os"
	"path/filepath"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/extension-sdk/wirejson"

	"github.com/google/uuid"
)

const maximumIntentControllerBytes = 1024

// IntentController records attribution while the source catalog is trusted.
// Empty ControllerID explicitly records an uncontrolled package. Absence is
// unknown legacy ownership, not an attribution to the requesting controller.
type IntentController struct {
	Schema         int    `json:"schema"`
	PackageID      string `json:"package_id"`
	InstallationID string `json:"installation_id"`
	ControllerID   string `json:"controller_id"`
}

func IntentControllerPath(root, id string) string {
	return filepath.Join(root, ".intent-controllers", id+".json")
}

func validIntentController(root, id string, binding IntentController) bool {
	if !sdk.ValidID(id) || binding.Schema != 1 || binding.PackageID != id || binding.ControllerID != "" && !sdk.ValidID(binding.ControllerID) {
		return false
	}
	installation, err := uuid.Parse(binding.InstallationID)
	if err != nil || installation.String() != binding.InstallationID {
		return false
	}
	recorded, err := LoadInstallation(root, id)
	return err == nil && recorded == binding.InstallationID
}

func LoadIntentController(root, id string) (IntentController, bool, error) {
	if !sdk.ValidID(id) {
		return IntentController{}, false, errors.New("invalid extension id")
	}
	file, err := os.Open(IntentControllerPath(root, id))
	if errors.Is(err, os.ErrNotExist) {
		return IntentController{}, false, nil
	}
	if err != nil {
		return IntentController{}, false, errors.New("extension controller attribution is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumIntentControllerBytes+1))
	if err != nil || len(data) > maximumIntentControllerBytes {
		return IntentController{}, false, errors.New("extension controller attribution is invalid")
	}
	fields, err := wirejson.ObjectFields(data, "extension controller attribution")
	if err != nil {
		return IntentController{}, false, errors.New("extension controller attribution is invalid")
	}
	if err := wirejson.RequireFields(fields, "extension controller attribution", []string{"schema", "package_id", "installation_id", "controller_id"}, []string{"controller_id"}); err != nil {
		return IntentController{}, false, errors.New("extension controller attribution is invalid")
	}
	var binding IntentController
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&binding) != nil || decoder.Decode(new(any)) != io.EOF || !validIntentController(root, id, binding) {
		return IntentController{}, false, errors.New("extension controller attribution is invalid")
	}
	return binding, true, nil
}

// BindIntentController is for a trusted source-catalog owner only. A current
// installation cannot silently move between controllers: that would reassign
// unknown external effects to an unrelated authority.
func BindIntentController(root, id, installationID, controllerID string) error {
	binding := IntentController{Schema: 1, PackageID: id, InstallationID: installationID, ControllerID: controllerID}
	if !validIntentController(root, id, binding) {
		return errors.New("extension controller attribution is invalid")
	}
	current, known, err := LoadIntentController(root, id)
	if err != nil {
		return err
	}
	if known {
		if current != binding {
			return errors.New("extension controller attribution conflicts with trusted catalog")
		}
		return nil
	}
	directory := filepath.Dir(IntentControllerPath(root, id))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := fsyncDirectory(root); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".controller-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	data, err := json.Marshal(binding)
	if err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Chmod(0o600)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Publish without replacing a concurrent, possibly conflicting binding.
	if err := os.Link(name, IntentControllerPath(root, id)); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		current, known, loadErr := LoadIntentController(root, id)
		if loadErr != nil {
			return loadErr
		}
		if !known || current != binding {
			return errors.New("extension controller attribution conflicts with trusted catalog")
		}
	}
	return fsyncDirectory(directory)
}

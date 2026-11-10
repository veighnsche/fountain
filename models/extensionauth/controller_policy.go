// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package extensionauth

import (
	"forgejo.org/models/desktopgrant"
	"forgejo.org/modules/setting"
)

// ControllerAdmissionPolicy identifies packages whose native writes require
// the configured repository controller, including the retained intake path.
type ControllerAdmissionPolicy struct {
	required bool
}

func ControllerPolicyForPackage(packageID string) ControllerAdmissionPolicy {
	return ControllerAdmissionPolicy{required: setting.OAuth2.FactoryAppliancePackageID != "" && packageID == setting.OAuth2.FactoryAppliancePackageID}
}

func (policy ControllerAdmissionPolicy) RequiresController() bool {
	return policy.required
}

func (policy ControllerAdmissionPolicy) CheckLegacyIntake() error {
	if policy.required {
		return desktopgrant.ErrController
	}
	return nil
}

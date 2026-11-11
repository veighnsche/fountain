// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"time"

	sdk "forgejo.org/extension-sdk"
)

type lifecycleCommand string

const (
	lifecycleObserve    lifecycleCommand = sdk.LifecycleObserve
	lifecycleActivate   lifecycleCommand = sdk.LifecycleActivate
	lifecycleDeactivate lifecycleCommand = sdk.LifecycleDeactivate
)

func commandForEnabled(enabled bool) lifecycleCommand {
	if enabled {
		return lifecycleActivate
	}
	return lifecycleDeactivate
}

type lifecycleTransition string

const (
	transitionIdle                lifecycleTransition = ""
	transitionCommand             lifecycleTransition = "active"
	transitionFailedEnableCleanup lifecycleTransition = "cleanup"
)

type lifecycleAdmissionPurpose string

const (
	purposeLifecycle           lifecycleAdmissionPurpose = "lifecycle"
	purposeFailedEnableCleanup lifecycleAdmissionPurpose = "failed-enable-cleanup"
)

type lifecycleVerificationHop uint8

const (
	controllerVerification lifecycleVerificationHop = iota
	serviceVerification
)

// completeLifecycleVerification preserves ordinary repeated verification. A
// cleanup call permits one controller check followed by one service check;
// failed tuple/state checks consume neither hop.
func (m *Manager) completeLifecycleVerification(token string, admission BackgroundAdmission, verification sdk.LifecycleVerification, hop lifecycleVerificationHop) (sdk.LifecycleVerification, *AuthError) {
	refuse := func() (sdk.LifecycleVerification, *AuthError) {
		return sdk.LifecycleVerification{}, &AuthError{Code: "unauthorized", Message: "lifecycle admission is unknown or revoked", HTTP: 403}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, known := m.controllers[admission.Call.ControllerID]
	if !known || !state.ready || state.instanceID != admission.Call.ControllerInstanceID || admission.Call.DeadlineUnix <= time.Now().Unix() {
		return refuse()
	}
	if admission.lifecyclePurpose == purposeFailedEnableCleanup &&
		(m.mutation != admission.Call.PackageID || m.mutationPhase != transitionFailedEnableCleanup) {
		return refuse()
	}
	m.background.mu.Lock()
	defer m.background.mu.Unlock()
	current := m.background.entries[token]
	if current == nil || !current.Lifecycle || current.Call != admission.Call || current.lifecyclePurpose != admission.lifecyclePurpose || current.DesiredEnabled != admission.DesiredEnabled {
		return refuse()
	}
	if admission.lifecyclePurpose == purposeFailedEnableCleanup {
		switch hop {
		case controllerVerification:
			if current.cleanupControllerVerified {
				return refuse()
			}
			current.cleanupControllerVerified = true
		case serviceVerification:
			if !current.cleanupControllerVerified {
				return refuse()
			}
			m.background.revokeLocked(token)
		default:
			return refuse()
		}
	}
	return verification, nil
}

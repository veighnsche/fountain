// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package extensions

import (
	"net/http"
	"testing"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/modules/setting"

	"github.com/stretchr/testify/require"
)

func TestLegacyIntakeUsesControllerAdmissionPolicy(t *testing.T) {
	priorPackage := setting.OAuth2.FactoryAppliancePackageID
	t.Cleanup(func() { setting.OAuth2.FactoryAppliancePackageID = priorPackage })
	for _, tc := range []struct {
		name       string
		configured string
		status     int
		message    string
	}{
		{"unconfigured", "", http.StatusBadRequest, "invalid background request"},
		{"unrelated package", "other-appliance", http.StatusBadRequest, "invalid background request"},
		{"configured appliance", "multiplexer-test", http.StatusForbidden, "controller-aware issue intake required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setting.OAuth2.FactoryAppliancePackageID = tc.configured
			manager := backgroundTestManager(t)
			token := issueRuntimeAdmission(t, manager, "11111111-2222-4333-8444-555555555555", "runtime", []string{extension.CapabilityBackgroundOperations, extension.CapabilityIssueIntake})
			handler := backgroundMux(manager, http.NotFoundHandler(), "runtime", false, testVerifiers(), testOperations())
			response := backgroundRequest(t, handler, extension.BackgroundIssueIntakePath, token, backgroundPeerAddr(t), map[string]any{"unsupported": true})
			require.Equal(t, tc.status, response.Code)
			require.Contains(t, response.Body.String(), tc.message)
		})
	}
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	sdk "forgejo.org/extension-sdk"

	"github.com/stretchr/testify/require"
)

func TestStandaloneWriteCapabilitiesReceiveCallbackSocket(t *testing.T) {
	root := shortTestRoot(t)
	installTestPackage(t, root, "sample")
	for _, capability := range []string{
		sdk.CapabilityHumanStates, sdk.CapabilityHumanReviews,
		sdk.CapabilityHumanMerges, sdk.CapabilityHumanLocks,
		sdk.CapabilityHumanIssueCreate, sdk.CapabilityHumanPullCreate,
		sdk.CapabilityIssueIntake,
	} {
		t.Run(capability, func(t *testing.T) {
			manifest := fmt.Sprintf(`{"protocol":1,"id":"sample","name":"Test","version":"1.0","executable":"extension","capabilities":[%q]}`, capability)
			require.NoError(t, os.WriteFile(filepath.Join(root, "sample", "extension.json"), []byte(manifest), 0o600))
			setDesiredState(t, root, "sample", true)
			manager := NewManager(root)
			manager.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
			require.NoError(t, manager.SetCallbackHandlerFactory(func(instanceID string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, instanceID)
				})
			}))
			require.NoError(t, manager.Start(t.Context()))
			defer manager.Close()
			waitStartupFinished(t, manager)
			descriptor, _, ok := manager.Lookup("sample")
			require.True(t, ok, "capability-only package must start")
			manager.mu.Lock()
			running := manager.running["sample"]
			socket := filepath.Join(running.runtimeDir, "c")
			server := running.callbackServer
			manager.mu.Unlock()
			require.NotNil(t, server, "package needs a callback server")
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}
			defer transport.CloseIdleConnections()
			response, err := (&http.Client{Transport: transport}).Get("http://host/callback")
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, response.Body.Close())
			require.NoError(t, err)
			require.Equal(t, descriptor.InstanceID, string(body))
		})
	}
}

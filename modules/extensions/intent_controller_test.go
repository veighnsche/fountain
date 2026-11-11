// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntentControllerBindingPreservesFrozenIntentAndRefusesReassignment(t *testing.T) {
	root := t.TempDir()
	installation := ensureTestInstallation(t, root, "sample")
	_, err := StoreIntent(root, "sample", installation, true, new(IntentActivate), "0")
	require.NoError(t, err)
	before, err := os.ReadFile(IntentPath(root, "sample"))
	require.NoError(t, err)
	_, known, err := LoadIntentController(root, "sample")
	require.NoError(t, err)
	require.False(t, known)
	require.NoError(t, BindIntentController(root, "sample", installation, "controller-a"))
	require.NoError(t, BindIntentController(root, "sample", installation, "controller-a"))
	bound, known, err := LoadIntentController(root, "sample")
	require.NoError(t, err)
	require.True(t, known)
	require.Equal(t, "controller-a", bound.ControllerID)
	require.Equal(t, installation, bound.InstallationID)
	require.Error(t, BindIntentController(root, "sample", installation, "controller-b"))
	after, err := os.ReadFile(IntentPath(root, "sample"))
	require.NoError(t, err)
	require.Equal(t, before, after)
	info, err := os.Stat(IntentControllerPath(root, "sample"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestIntentControllerConcurrentAttributionCannotOverwriteAnotherOwner(t *testing.T) {
	root := t.TempDir()
	installation := ensureTestInstallation(t, root, "sample")
	var succeeded atomic.Int32
	var group sync.WaitGroup
	for _, controller := range []string{"controller-a", "controller-b"} {
		group.Go(func() {
			if BindIntentController(root, "sample", installation, controller) == nil {
				succeeded.Add(1)
			}
		})
	}
	group.Wait()
	require.EqualValues(t, 1, succeeded.Load())
	_, known, err := LoadIntentController(root, "sample")
	require.NoError(t, err)
	require.True(t, known)
}

func TestRetiredIntentDoesNotReassignAttributionToANewInstallation(t *testing.T) {
	root := t.TempDir()
	installation := ensureTestInstallation(t, root, "sample")
	_, err := StoreIntent(root, "sample", installation, false, nil, "0")
	require.NoError(t, err)
	require.NoError(t, BindIntentController(root, "sample", installation, "controller-a"))
	require.NoError(t, RemoveInstallation(root, "sample"))
	require.NoError(t, RemoveIntent(root, "sample"))
	require.NoError(t, RemoveIntent(root, "sample"))
	reinstalled := ensureTestInstallation(t, root, "sample")
	require.NotEqual(t, installation, reinstalled)
	_, known, err := LoadIntentController(root, "sample")
	require.NoError(t, err)
	require.False(t, known)
	require.NoError(t, BindIntentController(root, "sample", reinstalled, "controller-b"))
}

func TestIntentControllerRejectsMalformedSidecarsInsteadOfGuessingOwnership(t *testing.T) {
	root := t.TempDir()
	installation := ensureTestInstallation(t, root, "sample")
	require.NoError(t, BindIntentController(root, "sample", installation, ""))
	valid, err := os.ReadFile(IntentControllerPath(root, "sample"))
	require.NoError(t, err)
	for _, document := range []string{
		strings.Replace(string(valid), `"controller_id":""`, `"controller_id":null`, 1),
		strings.Replace(string(valid), `"controller_id":""`, `"controller_id":"a","controller_id":"b"`, 1),
		strings.Replace(string(valid), `"schema":1`, `"schema":2`, 1),
		strings.Replace(string(valid), installation, "11111111-2222-4333-8444-555555555555", 1),
		strings.Repeat(" ", maximumIntentControllerBytes+1),
	} {
		require.NoError(t, os.WriteFile(IntentControllerPath(root, "sample"), []byte(document), 0o600))
		_, known, err := LoadIntentController(root, "sample")
		require.Error(t, err)
		require.False(t, known)
		require.Error(t, BindIntentController(root, "sample", installation, "controller"))
	}
}

func TestIntentErrorsDistinguishInvalidStaleAndExhaustedGeneration(t *testing.T) {
	root := t.TempDir()
	installation := ensureTestInstallation(t, root, "sample")
	_, err := StoreIntent(root, "sample", installation, true, nil, "01")
	var outcome *IntentError
	require.ErrorAs(t, err, &outcome)
	require.Equal(t, IntentInvalid, outcome.Kind)
	_, err = StoreIntent(root, "sample", installation, true, nil, "0")
	require.NoError(t, err)
	_, err = StoreIntent(root, "sample", installation, true, nil, "0")
	require.ErrorAs(t, err, &outcome)
	require.Equal(t, IntentStale, outcome.Kind)
	require.NoError(t, writeIntent(root, Intent{Schema: 1, PackageID: "sample", InstallationID: installation, Generation: "18446744073709551615", DesiredEnabled: true}))
	_, err = StoreIntent(root, "sample", installation, true, nil, "18446744073709551615")
	require.ErrorAs(t, err, &outcome)
	require.Equal(t, IntentExhausted, outcome.Kind)
	_, err = StorePendingSameGeneration(root, "sample", true, new(IntentActivate), "18446744073709551615")
	require.NoError(t, err, "pending markers do not increment the exhausted generation")
}

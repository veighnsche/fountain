// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failingRuntimeControl struct {
	entered chan struct{}
	release chan struct{}
}

func (c *failingRuntimeControl) ReadyContext(ctx context.Context) error {
	close(c.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.release:
		return errors.New("private listener failed")
	}
}

func TestManagerReapsUnreadyLiveProcessWithoutHoldingRegistryLock(t *testing.T) {
	root := shortTestRoot(t)
	installTestPackage(t, root, "sample")
	setDesiredState(t, root, "sample", true)
	m := NewManager(root)
	m.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
	require.NoError(t, m.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	waitStartupFinished(t, m)
	failure := &failingRuntimeControl{entered: make(chan struct{}), release: make(chan struct{})}
	m.mu.Lock()
	item := m.running["sample"]
	m.mu.Unlock()
	require.NotNil(t, item)
	m.mu.Lock()
	item.control = failure
	m.mu.Unlock()
	select {
	case <-failure.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("runtime readiness was not monitored")
	}
	listed := make(chan struct{})
	go func() {
		m.List()
		close(listed)
	}()
	select {
	case <-listed:
	case <-time.After(time.Second):
		t.Fatal("control health request held the manager registry lock")
	}
	require.False(t, item.client.Exited(), "fixture must still be alive when readiness fails")
	close(failure.release)
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.running["sample"] == nil
	}, 4*time.Second, 10*time.Millisecond)
	require.True(t, item.client.Exited())
	_, err := os.Stat(item.runtimeDir)
	require.True(t, os.IsNotExist(err), "unavailable runtime bytes should be retired")
}

func TestManagerIgnoresReadinessResultForReplacedRuntime(t *testing.T) {
	root := shortTestRoot(t)
	installTestPackage(t, root, "sample")
	setDesiredState(t, root, "sample", true)
	m := NewManager(root)
	m.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
	require.NoError(t, m.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	waitStartupFinished(t, m)
	failure := &failingRuntimeControl{entered: make(chan struct{}), release: make(chan struct{})}
	m.mu.Lock()
	item := m.running["sample"]
	originalControl := item.control
	item.control = failure
	m.mu.Unlock()
	select {
	case <-failure.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("runtime readiness was not monitored")
	}
	m.mu.Lock()
	replacement := *item
	replacement.control = originalControl
	m.running["sample"] = &replacement
	m.mu.Unlock()
	close(failure.release)
	// Wait beyond both the in-flight probe and the next observer tick.
	time.Sleep(2200 * time.Millisecond)
	m.mu.Lock()
	current := m.running["sample"]
	m.mu.Unlock()
	require.Same(t, &replacement, current)
	require.False(t, replacement.client.Exited(), "old health result killed the replacement runtime")
}

func TestManagerRetiresStoppedProcessWithoutWaitingForGracefulQuit(t *testing.T) {
	root := shortTestRoot(t)
	installTestPackage(t, root, "sample")
	setDesiredState(t, root, "sample", true)
	m := NewManager(root)
	m.env = []string{"FORGEJO_EXTENSION_TEST_HELPER=1"}
	require.NoError(t, m.Start(t.Context()))
	waitStartupFinished(t, m)
	m.mu.Lock()
	item := m.running["sample"]
	m.mu.Unlock()
	t.Cleanup(func() {
		// A failing assertion must also unblock the old graceful-Quit path.
		_ = item.process.Kill()
		require.NoError(t, m.Close())
	})
	require.NoError(t, item.process.Signal(syscall.SIGSTOP))
	retired := make(chan struct{})
	go func() {
		for {
			m.mu.Lock()
			active := m.running["sample"] != nil
			m.mu.Unlock()
			if !active {
				m.List()
				close(retired)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	select {
	case <-retired:
	case <-time.After(runtimeReadyTimeout + 3*time.Second):
		t.Fatal("stopped plugin blocked registry retirement in its graceful Quit RPC")
	}
	require.True(t, item.client.Exited())
	_, err := os.Stat(item.runtimeDir)
	require.True(t, os.IsNotExist(err))
}

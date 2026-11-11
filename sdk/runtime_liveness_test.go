// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	plugin "github.com/hashicorp/go-plugin"
)

func TestRuntimeListenerFailureHelper(t *testing.T) {
	if os.Getenv("FORGEJO_SDK_LISTENER_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("unix", os.Getenv(SocketEnv))
	if err != nil {
		t.Fatal(err)
	}
	application := Application{HTTP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			_ = listener.Close()
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	if err := serveRuntime(application, listener); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeListenerFailureDropsNormalHandshakeReadiness(t *testing.T) {
	// Use the real cookie, child process and RPC handshake: go-plugin Test
	// mode omits the handshake that production uses.
	directory, err := os.MkdirTemp("", "sdk-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "h")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestRuntimeListenerFailureHelper$")
	command.Env = append(os.Environ(), "FORGEJO_SDK_LISTENER_HELPER=1", SocketEnv+"="+socket)
	contract, plugins := ClientContract()
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: contract,
		Plugins:         plugins,
		Cmd:             command,
		StartTimeout:    5 * time.Second,
		Logger:          hclog.NewNullLogger(),
	})
	t.Cleanup(client.Kill)
	protocol, err := client.Client()
	if err != nil {
		t.Fatal(err)
	}
	control, err := protocol.Dispense("control")
	if err != nil {
		t.Fatal(err)
	}
	ready := control.(interface{ ReadyContext(context.Context) error })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := ready.ReadyContext(ctx); err != nil {
		t.Fatalf("initial readiness: %v", err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://extension/fail", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	for ready.ReadyContext(ctx) == nil {
		if ctx.Err() != nil {
			t.Fatal("listener failure remained ready")
		}
		time.Sleep(time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("listener failure was not reported before the deadline")
	}
	if client.Exited() {
		t.Fatal("fixture process exited before its listener-only failure could be observed")
	}
}

func TestRuntimeExpectedCloseIsNormal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{}
	liveness := &runtimeLiveness{}
	closed := make(chan struct{})
	go func() {
		liveness.serve(server, listener)
		close(closed)
	}()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("expected close did not finish serving")
	}
	var ready bool
	if err := (&controlRPC{liveness: liveness}).Ready(struct{}{}, &ready); err != nil || !ready {
		t.Fatalf("normal shutdown classified as listener failure: ready=%v err=%v", ready, err)
	}
}

func TestReadyContextBoundsUnresponsiveControl(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := rpc.NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { _ = serverConn.Close() })
	// The peer does not even read the RPC request.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := (&controlClient{client: client}).ReadyContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unresponsive control did not respect deadline: %v", err)
	}
}

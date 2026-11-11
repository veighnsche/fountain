// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"sort"
	"sync/atomic"

	plugin "github.com/hashicorp/go-plugin"
)

type Application struct {
	HTTP                  http.Handler
	AuthorizeContribution ContributionAuthorizer
	Policies              map[string]PolicyHandler
	// Lifecycle handles generic lifecycle operations for the required
	// controller extension. It runs on the SDK-owned lifecycle endpoint
	// before ordinary HTTP routes dispatch. Extensions without external
	// effects leave it nil and keep existing backend start/stop behavior.
	Lifecycle LifecycleHandler
}

type Registration struct {
	AuthorizesContribution bool     `json:"authorizes_contribution"`
	Policies               []string `json:"policies"`
	// Lifecycle reports whether the extension registered a lifecycle
	// handler. The handshake must match this to the installed manifest's
	// lifecycle capability (see ValidateLifecycleRegistration).
	Lifecycle bool `json:"lifecycle"`
}

func (a Application) registration() Registration {
	registration := Registration{
		AuthorizesContribution: a.AuthorizeContribution != nil,
		Lifecycle:              a.Lifecycle != nil,
	}
	for id, handler := range a.Policies {
		if handler != nil {
			registration.Policies = append(registration.Policies, id)
		}
	}
	sort.Strings(registration.Policies)
	return registration
}

func (a Application) handler() http.Handler {
	mux := http.NewServeMux()
	// The SDK-owned lifecycle endpoint runs before ordinary routes: the
	// manager's lifecycle call carries its own opaque authority, never a
	// browser Actor, so it must not fall through to Application.HTTP.
	mux.HandleFunc("POST "+LifecyclePath, func(w http.ResponseWriter, r *http.Request) {
		if a.Lifecycle == nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.RawQuery != "" {
			http.Error(w, "lifecycle calls accept no query parameters", http.StatusBadRequest)
			return
		}
		call, err := decodeLifecycleBody(r.Body)
		if err != nil {
			http.Error(w, "invalid lifecycle call", http.StatusBadRequest)
			return
		}
		authority, err := LifecycleContext(r)
		if err != nil {
			http.Error(w, "lifecycle authority is not admitted", http.StatusForbidden)
			return
		}
		observation, err := a.Lifecycle(r.Context(), authority, call)
		if err != nil {
			http.Error(w, "lifecycle operation unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, observation)
	})
	mux.HandleFunc("POST /v1/contribution/authorize", func(w http.ResponseWriter, r *http.Request) {
		if a.AuthorizeContribution == nil {
			http.NotFound(w, r)
			return
		}
		var request ContributionRequest
		if err := decodeRequest(r, &request); err != nil {
			http.Error(w, "invalid contribution request", http.StatusBadRequest)
			return
		}
		decision, err := a.AuthorizeContribution(r.Context(), request)
		if err != nil {
			http.Error(w, "contribution authorizer unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, decision)
	})
	mux.HandleFunc("POST /v1/policies/{id}", func(w http.ResponseWriter, r *http.Request) {
		handler := a.Policies[r.PathValue("id")]
		if handler == nil {
			http.NotFound(w, r)
			return
		}
		var request PolicyRequest
		if err := decodeRequest(r, &request); err != nil {
			http.Error(w, "invalid policy request", http.StatusBadRequest)
			return
		}
		decision, err := handler(r.Context(), request)
		if err != nil {
			http.Error(w, "policy unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, decision)
	})
	mux.Handle("/", a.HTTP)
	return mux
}

func decodeRequest(r *http.Request, target any) error {
	const maximumRequestBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(r.Body, maximumRequestBytes+1))
	if err != nil {
		return errors.New("read request body failed")
	}
	if len(data) > maximumRequestBytes {
		return errors.New("request body exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

// decodeLifecycleBody reads one lifecycle call body bounded at 16 KiB and
// strictly decodes it: unknown fields, duplicate keys, trailing data,
// wrong scalar types and missing required fields are rejected.
func decodeLifecycleBody(body io.Reader) (LifecycleCall, error) {
	data, err := io.ReadAll(io.LimitReader(body, MaximumLifecycleBodyBytes+1))
	if err != nil {
		return LifecycleCall{}, errors.New("read lifecycle call failed")
	}
	if len(data) > MaximumLifecycleBodyBytes {
		return LifecycleCall{}, errors.New("lifecycle call exceeds limit")
	}
	return DecodeLifecycleCall(data)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func DataDir() string { return os.Getenv(DataEnv) }

var handshake = plugin.HandshakeConfig{
	ProtocolVersion:  Protocol,
	MagicCookieKey:   "FORGEJO_EXTENSION_MAGIC_COOKIE",
	MagicCookieValue: "forgejo-extension-v1",
}

type runtimeLiveness struct{ failed atomic.Bool }

func (l *runtimeLiveness) serve(server *http.Server, listener net.Listener) {
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		l.failed.Store(true)
	}
}

type controlPlugin struct {
	registration Registration
	liveness     *runtimeLiveness
}

func (p controlPlugin) Server(*plugin.MuxBroker) (any, error) {
	return &controlRPC{registration: p.registration, liveness: p.liveness}, nil
}

func (controlPlugin) Client(_ *plugin.MuxBroker, client *rpc.Client) (any, error) {
	return &controlClient{client: client}, nil
}

type controlRPC struct {
	registration Registration
	liveness     *runtimeLiveness
}

func (c *controlRPC) Ready(_ struct{}, ready *bool) error {
	*ready = c.liveness != nil && !c.liveness.failed.Load()
	return nil
}

func (c *controlRPC) Registration(_ struct{}, registration *Registration) error {
	*registration = c.registration
	return nil
}

// DeliverBackgroundAdmission receives the host-issued runtime admission after
// registration. The SDK holds it in memory for RuntimeBackgroundClient.
func (*controlRPC) DeliverBackgroundAdmission(delivery BackgroundAdmissionDelivery, ack *bool) error {
	if !validBackgroundToken(delivery.Admission) || delivery.InstallationID == "" {
		return errors.New("invalid background admission delivery")
	}
	storeBackgroundDelivery(delivery)
	*ack = true
	return nil
}

type controlClient struct{ client *rpc.Client }

func (c *controlClient) Ready() error {
	return c.ReadyContext(context.Background())
}

func (c *controlClient) ReadyContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var ready bool
	done := make(chan error, 1)
	go func() { done <- c.client.Call("Plugin.Ready", struct{}{}, &ready) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return err
		}
	}
	if !ready {
		return errors.New("extension is not ready")
	}
	return nil
}

func (c *controlClient) Registration() (Registration, error) {
	var registration Registration
	err := c.client.Call("Plugin.Registration", struct{}{}, &registration)
	return registration, err
}

// DeliverBackgroundAdmission pushes a runtime admission to the extension. The
// host calls it after registration when the background capability is declared.
func (c *controlClient) DeliverBackgroundAdmission(delivery BackgroundAdmissionDelivery) error {
	var ack bool
	if err := c.client.Call("Plugin.DeliverBackgroundAdmission", delivery, &ack); err != nil {
		return err
	}
	if !ack {
		return errors.New("extension rejected background admission")
	}
	return nil
}

// Serve starts the extension listener, then joins the host process handshake.
func Serve(application Application) error {
	if application.HTTP == nil {
		return errors.New("extension HTTP handler is required")
	}
	if err := validateNames(application.registration().Policies, map[string]bool{PolicyForgejoUsername: true}, "policy"); err != nil {
		return err
	}
	socket := os.Getenv(SocketEnv)
	if socket == "" {
		return errors.New("extension socket is not configured")
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen on extension socket: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	return serveRuntime(application, listener)
}

func serveRuntime(application Application, listener net.Listener) error {
	server := &http.Server{Handler: application.handler()}
	liveness := &runtimeLiveness{}
	go liveness.serve(server, listener)
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins:         plugin.PluginSet{"control": controlPlugin{registration: application.registration(), liveness: liveness}},
	})
	return server.Close()
}

// ClientContract shares the exact plugin handshake with the host.
func ClientContract() (plugin.HandshakeConfig, plugin.PluginSet) {
	return handshake, plugin.PluginSet{"control": controlPlugin{}}
}

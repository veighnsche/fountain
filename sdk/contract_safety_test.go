// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLifecycleBooleansRejectNullWithoutChangingNullableGeneration(t *testing.T) {
	observation := LifecycleObservation{
		LifecycleCall: frozenCall(), State: LifecycleStateStopped,
		EffectGeneration: nil, AdmissionReady: false, Detail: "stopped",
	}
	rawObservation := mustEncodeLifecycle(t, observation)
	decoded, err := DecodeLifecycleObservation(rawObservation)
	if err != nil || decoded.AdmissionReady || decoded.EffectGeneration != nil || decoded.LifecycleCall != observation.LifecycleCall {
		t.Fatalf("explicit false and null generation must round trip: observation=%+v, err=%v", decoded, err)
	}
	if _, err := DecodeLifecycleObservation([]byte(strings.Replace(string(rawObservation), `"admission_ready":false`, `"admission_ready":null`, 1))); err == nil {
		t.Fatal("null admission_ready must not become an explicit false observation")
	}

	verification := LifecycleVerification{Call: frozenCall(), DesiredEnabled: false}
	rawVerification := mustEncodeLifecycle(t, verification)
	verified, err := DecodeLifecycleVerification(rawVerification)
	if err != nil || verified != verification {
		t.Fatalf("explicit false verification must round trip: verification=%+v, err=%v", verified, err)
	}
	if _, err := DecodeLifecycleVerification([]byte(strings.Replace(string(rawVerification), `"desired_enabled":false`, `"desired_enabled":null`, 1))); err == nil {
		t.Fatal("null desired_enabled must not become explicit disabled consent")
	}
}

func TestLifecycleRejectsEscapedDuplicateKeysAndTrailingObjects(t *testing.T) {
	observation := LifecycleObservation{
		LifecycleCall: frozenCall(), State: LifecycleStateStopped,
		Detail: "stopped", Error: &LifecycleError{Code: LifecycleErrUnavailable, Message: "unavailable"},
	}
	rawObservation := string(mustEncodeLifecycle(t, observation))
	rawVerification := string(mustEncodeLifecycle(t, LifecycleVerification{Call: frozenCall(), DesiredEnabled: false}))
	cases := []struct {
		name   string
		body   string
		decode func([]byte) error
	}{
		{
			name:   "observation escaped boolean duplicate",
			body:   strings.Replace(rawObservation, `"admission_ready":false`, `"admission_ready":false,"admission_\u0072eady":true`, 1),
			decode: func(body []byte) error { _, err := DecodeLifecycleObservation(body); return err },
		},
		{
			name:   "observation nested escaped duplicate",
			body:   strings.Replace(rawObservation, `"message":"unavailable"`, `"message":"unavailable","messa\u0067e":"other"`, 1),
			decode: func(body []byte) error { _, err := DecodeLifecycleObservation(body); return err },
		},
		{
			name:   "verification nested escaped duplicate",
			body:   strings.Replace(rawVerification, `"controller_id":"controller"`, `"controller_id":"controller","controller_\u0069d":"other"`, 1),
			decode: func(body []byte) error { _, err := DecodeLifecycleVerification(body); return err },
		},
		{
			name: "observation trailing object", body: rawObservation + " {}",
			decode: func(body []byte) error { _, err := DecodeLifecycleObservation(body); return err },
		},
		{
			name: "verification trailing object", body: rawVerification + " {}",
			decode: func(body []byte) error { _, err := DecodeLifecycleVerification(body); return err },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.decode([]byte(tc.body)) == nil {
				t.Fatal("ambiguous lifecycle wire input was accepted")
			}
		})
	}
}

func TestServiceBackgroundClientPinsEveryCredentialBearingDial(t *testing.T) {
	admission := strings.Repeat("a", 43)
	var requests atomic.Int32
	var credentialRequests atomic.Int32
	socket := unixBackgroundServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case BackgroundBootstrapPath:
			_ = json.NewEncoder(w).Encode(BackgroundAdmissionDelivery{Admission: admission, InstallationID: "install-1"})
		case BackgroundSubmitPath:
			var payload struct {
				OperationIntent
				Token string `json:"token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Token == "" || r.Header.Get(AdmissionHeader) != admission {
				http.Error(w, "missing credential or admission", http.StatusBadRequest)
				return
			}
			credentialRequests.Add(1)
			_ = json.NewEncoder(w).Encode(OperationRecord{InstallationID: "install-1", OperationID: payload.OperationID, Outcome: BackgroundOutcomeOperationsUnavailable})
		default:
			http.NotFound(w, r)
		}
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	hostUID := uint32(os.Getuid())
	client, err := BootstrapServiceBackground(ctx, ServiceBridgeOptions{SocketPath: socket, ExpectedHostUID: hostUID})
	if err != nil {
		t.Fatal(err)
	}
	serviceClient, ok := client.(*backgroundClient)
	if !ok || serviceClient.expectedHostUID == nil || *serviceClient.expectedHostUID != hostUID {
		t.Fatal("service bootstrap discarded its exact host UID pin")
	}
	credential := writeCredentialFile(t, 0o600)
	intent := OperationIntent{OperationID: "op-peer-pin", ActorID: "42", RepositoryID: "7", Kind: OperationKindMerge, AuthorizationRevision: "rev-1"}
	for range 2 {
		if _, err := client.SubmitOperation(ctx, credential, intent); err != nil {
			t.Fatalf("current host UID was rejected: %v", err)
		}
	}
	if credentialRequests.Load() != 2 {
		t.Fatal("matching host peer did not receive both credential-bearing requests")
	}

	wrongUIDs := []uint32{hostUID ^ 1}
	if hostUID != 0 && wrongUIDs[0] != 0 {
		wrongUIDs = append(wrongUIDs, 0)
	}
	for _, wrongUID := range wrongUIDs {
		before := requests.Load()
		pinned := *serviceClient
		pinned.expectedHostUID = &wrongUID
		for range 2 {
			if _, err := pinned.SubmitOperation(ctx, credential, intent); err == nil {
				t.Fatalf("host UID %d accepted with expected UID %d", hostUID, wrongUID)
			}
		}
		if requests.Load() != before {
			t.Fatalf("HTTP request reached an unverified host pinned as UID %d", wrongUID)
		}
	}
	if hostUID != 0 {
		before := requests.Load()
		if _, err := BootstrapServiceBackground(ctx, ServiceBridgeOptions{SocketPath: socket, ExpectedHostUID: 0}); err == nil {
			t.Fatal("expected UID 0 must be enforced during bootstrap")
		}
		if requests.Load() != before {
			t.Fatal("bootstrap request reached a host with an unexpected UID")
		}
	}
}

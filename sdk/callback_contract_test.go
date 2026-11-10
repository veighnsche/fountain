// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAcceptedCallbackCapabilitiesHaveTransport(t *testing.T) {
	for capability := range catalogCapabilities() {
		want := capability != CapabilityContributionAuthorize && capability != CapabilityServiceBridge
		if got := NeedsNativeCallback([]string{capability}); got != want {
			t.Errorf("capability %s: callback transport = %t, want %t", capability, got, want)
		}
	}
	if NeedsNativeCallback(nil) || NeedsNativeCallback([]string{"unknown.capability"}) {
		t.Fatal("unrecognized capability acquired callback transport")
	}
}

func TestCallbackSelectorsStayInTheirWireFamily(t *testing.T) {
	for _, test := range []struct {
		name    string
		request CallbackRequest
		allowed string
	}{
		{"manual merge intent", CallbackRequest{HumanManualMerge: &HumanManualMergeRequest{}}, OperationHumanManualMergeSubmit},
		{"pull creation lookup", CallbackRequest{PullCreateOperationID: "human-pull-create:2:7:command"}, OperationHumanPullCreateGet},
		{"comment installation", CallbackRequest{CommentInstallationID: "installation"}, OperationHumanCommentCancel},
		{"review request", CallbackRequest{HumanReviewRequest: &HumanReviewRequestChange{}}, OperationHumanReviewRequestSubmit},
		{"source actors", CallbackRequest{SourceActors: []string{"2"}}, OperationRepositorySource},
		{"complete issue bodies", CallbackRequest{CompleteIssueBodies: true}, OperationRepositoryIssues},
		{"runtime authorization", CallbackRequest{RuntimeAuthorization: &ApplianceOperationAuthorization{}}, OperationRuntimeControllerAuthorize},
		{"thread preparation", CallbackRequest{ThreadSnapshot: &RepositoryThreadSnapshotRequest{}}, OperationRepositoryThreadSnapshot},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.request.Operation = test.allowed
			if err := ValidateCallbackSelectors(test.request); err != nil {
				t.Fatalf("own selector rejected: %v", err)
			}
			test.request.Operation = OperationCurrentActor
			if err := ValidateCallbackSelectors(test.request); err == nil {
				t.Fatal("another operation accepted the selector")
			}
			test.request.Operation = "unknown.operation"
			if err := ValidateCallbackSelectors(test.request); err == nil {
				t.Fatal("unknown operation accepted the selector")
			}
		})
	}
}

func TestCallbackResponseRejectsDuplicateDecodedKeys(t *testing.T) {
	request := CallbackRequest{Operation: OperationCurrentActor}
	for _, body := range []string{
		`{"actor":{"id":"41"},"actor":{"id":"42"}}`,
		`{"actor":{"id":"41","\u0069d":"42"}}`,
		`{"actor":{"id":"42"},"\u0061ctor":{"id":"42"}}`,
	} {
		if _, err := decodeCallbackResponse(strings.NewReader(body), request); err == nil {
			t.Errorf("duplicate key accepted: %s", body)
		}
	}
}

func TestNativeCallbackStatusKeepsFamilyErrors(t *testing.T) {
	directory, err := os.MkdirTemp("", "cb-status-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "host.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var status atomic.Int64
	status.Store(http.StatusBadRequest)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	client := &nativeClient{socket: socket, admission: "private-admission", expectedUID: uint32(os.Geteuid())}
	for _, test := range []struct {
		operation string
		status    int
		want      error
	}{
		{OperationHumanCommentSubmit, http.StatusBadRequest, ErrHumanCommentQuery},
		{OperationHumanEditGet, http.StatusUnauthorized, ErrHumanEditAdmission},
		{OperationHumanStateCancel, http.StatusForbidden, ErrHumanStateAdmission},
		{OperationHumanLockSubmit, http.StatusConflict, ErrHumanLockConflict},
		{OperationHumanReviewGet, http.StatusServiceUnavailable, ErrHumanReviewUnavailable},
		{OperationHumanReviewRequestCancel, http.StatusConflict, ErrHumanReviewRequestConflict},
		{OperationHumanThreadSubmit, http.StatusBadRequest, ErrHumanThreadQuery},
		{OperationHumanInlineGet, http.StatusUnauthorized, ErrHumanInlineAdmission},
		{OperationHumanIssueCreateSubmit, http.StatusConflict, ErrHumanIssueCreateConflict},
		{OperationHumanPullCreateGet, http.StatusServiceUnavailable, ErrHumanPullCreateUnavailable},
		{OperationHumanManualMergeCancel, http.StatusForbidden, ErrHumanManualMergeAdmission},
		{OperationHumanMergeGet, http.StatusNotFound, ErrRepositoryNotVisible},
		{OperationRepositoryThreadSnapshot, http.StatusUnprocessableEntity, ErrThreadSnapshotTarget},
		{OperationRepositoryInlineSnapshot, http.StatusUnprocessableEntity, ErrInlineSnapshotAnchor},
		{OperationRepositoryManualMergeSnapshot, http.StatusUnprocessableEntity, ErrManualMergeSnapshotCommit},
		{OperationRepositoryManualMergeSnapshot, http.StatusInternalServerError, ErrManualMergeSnapshotUnavailable},
		{OperationRepositoryMergeSnapshot, http.StatusInternalServerError, ErrMergeSnapshotUnavailable},
		{OperationRepositoryPullCreateSnapshot, http.StatusUnprocessableEntity, ErrPullCreateSnapshotQuery},
		{OperationRepositoryPullCreateSnapshot, http.StatusTeapot, ErrPullCreateSnapshotUnavailable},
		{OperationRepositoryChecks, http.StatusRequestEntityTooLarge, ErrChecksTooLarge},
		{OperationRepositoryPullFiles, http.StatusConflict, ErrPullFilesChanged},
		{OperationRepositoryCapabilities, http.StatusBadRequest, ErrRepositoryCapabilitiesQuery},
	} {
		status.Store(int64(test.status))
		_, err := client.call(context.Background(), CallbackRequest{Operation: test.operation})
		if !errors.Is(err, test.want) {
			t.Errorf("%s HTTP %d: error %v, want %v", test.operation, test.status, err, test.want)
		}
	}
	for _, operation := range []string{OperationCurrentActor, OperationHumanCommentGet, OperationRepositoryCommentSnapshot} {
		status.Store(http.StatusTeapot)
		_, err := client.call(context.Background(), CallbackRequest{Operation: operation})
		if err == nil || err.Error() != "native callback returned HTTP 418" {
			t.Errorf("%s: generic HTTP fallback changed: %v", operation, err)
		}
	}
}

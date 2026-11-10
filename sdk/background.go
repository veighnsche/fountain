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
	"os"
	"strconv"
	"strings"
	"sync"

	"forgejo.org/extension-sdk/operationcontract"
	"forgejo.org/extension-sdk/wirejson"
)

// Background operation kinds from the conditional-native-mutations contract.
// Authorization for one kind never grants another.
const (
	OperationKindIssueGraph      = operationcontract.KindIssueGraph
	OperationKindRefPublish      = operationcontract.KindRefPublish
	OperationKindPRCreate        = operationcontract.KindPRCreate
	OperationKindReviewSubmit    = operationcontract.KindReviewSubmit
	OperationKindThreadResolve   = operationcontract.KindThreadResolve
	OperationKindMerge           = operationcontract.KindMerge
	OperationKindLifecycleIssue  = operationcontract.KindLifecycleIssue
	OperationKindLifecycleReview = operationcontract.KindLifecycleReview
	OperationKindLifecycleNote   = operationcontract.KindLifecycleNote
)

// Background dispatcher outcomes returned with HTTP 200 once the caller is
// admitted. Authentication/authorization failures use HTTP 401/403 instead.
// FT02 authenticates and authorizes; durable operation semantics arrive later,
// so an admitted submit/cancel honestly reports operations_unavailable and an
// admitted lookup reports not_observed.
const (
	BackgroundOutcomeOperationsUnavailable = "operations_unavailable"
	BackgroundOutcomeNotObserved           = "not_observed"
)

const (
	BackgroundSubmitPath     = "/v1/background/submit"
	BackgroundGetPath        = "/v1/background/get"
	BackgroundCancelPath     = "/v1/background/cancel"
	BackgroundBootstrapPath  = "/v1/background/bootstrap"
	BackgroundRevisionPath   = "/v1/background/revision"
	BackgroundSnapshotPath   = "/v1/background/snapshot"
	BackgroundCreationsPath  = "/v1/background/creations"
	BackgroundOperationsPath = "/v1/background/operations"
)

const maxBackgroundBodyBytes = 64 << 10

// CredentialFile is an SDK-local restricted secret input: a path to a file
// holding one native personal access token. The SDK reads it and presents the
// secret over the private transport; the path itself is never sent for
// privileged host reading.
type CredentialFile string

func (path CredentialFile) read() (string, error) {
	name := string(path)
	if name == "" || len(name) > 4096 {
		return "", errors.New("background credential file is not configured")
	}
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 4096 {
		return "", errors.New("background credential file is unavailable")
	}
	if info.Mode().Perm()&0o007 != 0 {
		return "", errors.New("background credential file must not be accessible to other users")
	}
	file, err := os.Open(name)
	if err != nil {
		return "", errors.New("background credential file is unavailable")
	}
	defer file.Close()
	secret, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(secret) == 0 || len(secret) > 4096 {
		return "", errors.New("background credential file is unavailable")
	}
	return strings.TrimSuffix(string(secret), "\n"), nil
}

// OperationIntent carries the validated common intent fields. The host
// derives installation identity from the authenticated transport; an
// InstallationID in the payload must equal it and can never select another
// installation. ExpectedNativeRevision binds the idle native revision the
// intent was prepared against; NotAfter is the host-time admission deadline
// in Unix seconds. Payload carries exactly the selected kind's semantic
// fields and participates in intent identity.
type OperationIntent struct {
	// Required for Factory controller operations; the host verifies the
	// exact current grant and retains it independently of token renewal.
	ControllerGrantID      string          `json:"controller_grant_id,omitempty"`
	OperationID            string          `json:"operation_id"`
	InstallationID         string          `json:"installation_id,omitempty"`
	ActorID                string          `json:"actor_id"`
	RepositoryID           string          `json:"repository_id"`
	Kind                   string          `json:"kind"`
	AuthorizationRevision  string          `json:"authorization_revision"`
	ExpectedNativeRevision int64           `json:"expected_native_revision"`
	NotAfter               int64           `json:"not_after"`
	Payload                json.RawMessage `json:"payload,omitempty"`
}

// MergePayload is the pull_request.merge intent payload: the exact PR,
// source, target and OIDs the reviewed candidate was verified against. Only
// fast-forward-only is supported; there is no fallback method.
type MergePayload struct {
	PullRequestNumber int64  `json:"pull_request_number"`
	HeadRepositoryID  int64  `json:"head_repository_id"`
	HeadRef           string `json:"head_ref"`
	BaseRef           string `json:"base_ref"`
	ExpectedHeadOID   string `json:"expected_head_oid"`
	ExpectedBaseOID   string `json:"expected_base_oid"`
	Method            string `json:"method"`
}

// Operation effect, cancellation and completion states from the
// conditional-native-mutations contract.
const (
	EffectPending       = "pending"
	EffectNotCommitted  = "not_committed"
	EffectCommitted     = "committed"
	EffectIndeterminate = "indeterminate"

	CancellationNone          = "none"
	CancellationPending       = "pending"
	CancellationCancelled     = "cancelled"
	CancellationTooLate       = "too_late"
	CancellationIndeterminate = "indeterminate"

	CompletionPending           = "pending"
	CompletionComplete          = "complete"
	CompletionNeedsIntervention = "needs_intervention"
)

// OperationRecord echoes host-derived identity with the durable outcome.
// Outcome carries the effect state for recorded operations, or
// operations_unavailable for an admitted kind whose stage is not implemented
// yet. Actor, repository, kind and intent fields stay unavailable on
// pre-submit cancellation tombstones rather than invented.
type OperationRecord struct {
	ControllerGrantID  string          `json:"controller_grant_id,omitempty"`
	InstallationID     string          `json:"installation_id"`
	OperationID        string          `json:"operation_id"`
	Outcome            string          `json:"outcome"`
	ReasonCode         string          `json:"reason_code,omitempty"`
	ActorID            string          `json:"actor_id,omitempty"`
	RepositoryID       string          `json:"repository_id,omitempty"`
	Kind               string          `json:"kind,omitempty"`
	EffectState        string          `json:"effect_state,omitempty"`
	CancellationStatus string          `json:"cancellation_status,omitempty"`
	CompletionState    string          `json:"completion_state,omitempty"`
	IntentDigest       string          `json:"intent_digest,omitempty"`
	Receipt            json.RawMessage `json:"receipt,omitempty"`
}

// OperationLookup distinguishes not_observed from a saved operation. Absence
// never proves that an earlier request cannot still arrive or that a write
// never occurred.
type OperationLookup struct {
	InstallationID string           `json:"installation_id"`
	OperationID    string           `json:"operation_id"`
	Status         string           `json:"status"`
	Record         *OperationRecord `json:"record,omitempty"`
}

// NativeRevisionObservation is one atomic native revision/occupancy
// observation. Authoritative input reads bracket between two equal idle
// observations before binding that revision to an intent.
type NativeRevisionObservation struct {
	Revision int64 `json:"revision"`
	Idle     bool  `json:"idle"`
}

// BackgroundClient performs background calls without a browser.
type BackgroundClient interface {
	ReadOperations(context.Context) (OperationsHandshake, error)
	ReadNativeRevision(context.Context) (NativeRevisionObservation, error)
	ReadSnapshot(context.Context, CredentialFile, SnapshotRequest) (NativeSnapshot, error)
	ReadCreations(context.Context, CredentialFile, CreationRecoveryRequest) (CreationRecoveryPage, error)
	SubmitOperation(context.Context, CredentialFile, OperationIntent) (OperationRecord, error)
	GetOperation(context.Context, string) (OperationLookup, error)
	CancelOperation(context.Context, string) (OperationRecord, error)
}

// ServiceBridgeOptions configures external-service bootstrap. SocketPath is
// the operator-configured shared service callback socket; ExpectedHostUID is
// the configured native host identity verified against the actual Unix peer.
// InstallationID optionally pins the expected installation and must equal the
// peer-mapped installation when set.
type ServiceBridgeOptions struct {
	SocketPath      string
	ExpectedHostUID uint32
	InstallationID  string
}

var (
	backgroundAdmissionMu       sync.Mutex
	backgroundAdmissionDelivery *BackgroundAdmissionDelivery
)

// BackgroundAdmissionDelivery is pushed by the host over the control channel
// after registration. The SDK holds it in memory only.
type BackgroundAdmissionDelivery struct {
	Admission      string `json:"admission"`
	InstallationID string `json:"installation_id"`
}

func validBackgroundToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	for _, c := range token {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func storeBackgroundDelivery(delivery BackgroundAdmissionDelivery) {
	backgroundAdmissionMu.Lock()
	defer backgroundAdmissionMu.Unlock()
	backgroundAdmissionDelivery = &delivery
}

func loadBackgroundDelivery() (BackgroundAdmissionDelivery, bool) {
	backgroundAdmissionMu.Lock()
	defer backgroundAdmissionMu.Unlock()
	if backgroundAdmissionDelivery == nil {
		return BackgroundAdmissionDelivery{}, false
	}
	return *backgroundAdmissionDelivery, true
}

// RuntimeBackgroundClient returns a client for the in-extension runtime
// channel using the host-delivered admission. It fails when the host never
// delivered one, for example when the background capability is not declared.
func RuntimeBackgroundClient() (BackgroundClient, error) {
	delivery, ok := loadBackgroundDelivery()
	if !ok || !validBackgroundToken(delivery.Admission) || delivery.InstallationID == "" {
		return nil, errors.New("background admission is unavailable")
	}
	socket := os.Getenv(CallbackEnv)
	if socket == "" {
		return nil, errors.New("background callback is unavailable")
	}
	return &backgroundClient{socket: socket, admission: delivery.Admission, installation: delivery.InstallationID}, nil
}

// BootstrapServiceBackground verifies the host peer and bootstraps a service
// admission on the shared service callback socket. The admission stays in the
// returned client's memory; restart bootstraps again.
func BootstrapServiceBackground(ctx context.Context, options ServiceBridgeOptions) (BackgroundClient, error) {
	if options.SocketPath == "" || len(options.SocketPath) > 4096 {
		return nil, errors.New("service callback socket is not configured")
	}
	transport := backgroundTransport(options.SocketPath, &options.ExpectedHostUID)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	body, err := json.Marshal(struct {
		InstallationID string `json:"installation_id,omitempty"`
	}{InstallationID: options.InstallationID})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://extensionHost"+BackgroundBootstrapPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("background bootstrap request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("background bootstrap returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBackgroundBodyBytes+1))
	if err != nil || len(data) > maxBackgroundBodyBytes {
		return nil, errors.New("invalid background bootstrap response")
	}
	var bootstrap struct {
		Admission      string `json:"admission"`
		InstallationID string `json:"installation_id"`
	}
	if _, err := wirejson.ObjectFields(data, "background bootstrap response"); err != nil {
		return nil, errors.New("invalid background bootstrap response")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bootstrap); err != nil {
		return nil, errors.New("invalid background bootstrap response")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid background bootstrap response")
	}
	if !validBackgroundToken(bootstrap.Admission) || bootstrap.InstallationID == "" {
		return nil, errors.New("invalid background bootstrap response")
	}
	if options.InstallationID != "" && options.InstallationID != bootstrap.InstallationID {
		return nil, errors.New("background bootstrap installation mismatch")
	}
	return &backgroundClient{socket: options.SocketPath, admission: bootstrap.Admission, installation: bootstrap.InstallationID, expectedHostUID: &options.ExpectedHostUID}, nil
}

type backgroundClient struct {
	expectedHostUID *uint32
	socket          string
	admission       string
	installation    string
}

func backgroundTransport(socket string, expectedHostUID *uint32) *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, err
		}
		if expectedHostUID != nil {
			peer, err := PeerCredential(conn)
			if err != nil || peer.UID != *expectedHostUID {
				_ = conn.Close()
				return nil, errors.New("service callback host peer is not permitted")
			}
		}
		return conn, nil
	}}
}

func (c *backgroundClient) post(ctx context.Context, path string, payload any, target any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	transport := backgroundTransport(c.socket, c.expectedHostUID)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://extensionHost"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(AdmissionHeader, c.admission)
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("background request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("background request returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBackgroundBodyBytes+1))
	if err != nil || len(body) > maxBackgroundBodyBytes {
		return errors.New("invalid background response")
	}
	if _, err := wirejson.ObjectFields(body, "background response"); err != nil {
		return errors.New("invalid background response")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid background response")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("invalid background response")
	}
	return nil
}

func validOperationID(id string) bool {
	return operationcontract.ValidID(id)
}

func (c *backgroundClient) SubmitOperation(ctx context.Context, credential CredentialFile, intent OperationIntent) (OperationRecord, error) {
	if intent.InstallationID != "" && intent.InstallationID != c.installation {
		return OperationRecord{}, errors.New("background installation mismatch")
	}
	intent.InstallationID = c.installation
	secret, err := credential.read()
	if err != nil {
		return OperationRecord{}, err
	}
	request := struct {
		OperationIntent
		Token string `json:"token"`
	}{OperationIntent: intent, Token: secret}
	var record OperationRecord
	if err := c.post(ctx, BackgroundSubmitPath, request, &record); err != nil {
		return OperationRecord{}, err
	}
	if record.InstallationID != c.installation || record.OperationID != intent.OperationID || record.Outcome == "" {
		return OperationRecord{}, errors.New("invalid background response")
	}
	return record, nil
}

func (c *backgroundClient) ReadNativeRevision(ctx context.Context) (NativeRevisionObservation, error) {
	var observation NativeRevisionObservation
	if err := c.post(ctx, BackgroundRevisionPath, struct{}{}, &observation); err != nil {
		return NativeRevisionObservation{}, err
	}
	if observation.Revision < 1 {
		return NativeRevisionObservation{}, errors.New("invalid background response")
	}
	return observation, nil
}

// ReadSnapshot reads one permission-checked native snapshot. The request is
// validated before sending; the secret travels privately and the host echoes
// the requested repository. The caller brackets the read between two equal
// idle ReadNativeRevision observations; the snapshot itself carries no
// revision.
func (c *backgroundClient) ReadSnapshot(ctx context.Context, credential CredentialFile, req SnapshotRequest) (NativeSnapshot, error) {
	if err := ValidateSnapshotRequest(req); err != nil {
		return NativeSnapshot{}, err
	}
	secret, err := credential.read()
	if err != nil {
		return NativeSnapshot{}, err
	}
	request := struct {
		SnapshotRequest
		Token string `json:"token"`
	}{SnapshotRequest: req, Token: secret}
	var snapshot NativeSnapshot
	if err := c.post(ctx, BackgroundSnapshotPath, request, &snapshot); err != nil {
		return NativeSnapshot{}, err
	}
	if snapshot.RepositoryID != req.RepositoryID {
		return NativeSnapshot{}, errors.New("invalid background response")
	}
	return snapshot, nil
}

// ReadCreations recovers original creation observations missed by HTTP delivery.
// The bounded read uses the same private credential transport as snapshots.
func (c *backgroundClient) ReadCreations(ctx context.Context, credential CredentialFile, req CreationRecoveryRequest) (CreationRecoveryPage, error) {
	if err := ValidateCreationRecoveryRequest(req); err != nil {
		return CreationRecoveryPage{}, err
	}
	secret, err := credential.read()
	if err != nil {
		return CreationRecoveryPage{}, err
	}
	request := struct {
		CreationRecoveryRequest
		Token string `json:"token"`
	}{CreationRecoveryRequest: req, Token: secret}
	var page CreationRecoveryPage
	if err := c.post(ctx, BackgroundCreationsPath, request, &page); err != nil {
		return CreationRecoveryPage{}, err
	}
	if page.RepositoryID != req.RepositoryID || len(page.Creations) > req.Limit ||
		(page.NextCursor != "" && !creationNativeID(page.NextCursor)) ||
		(page.OldestRetainedTaskID != "" && !creationNativeID(page.OldestRetainedTaskID)) ||
		(!page.Complete && (page.NextCursor == "" || page.NextCursor == req.AfterID)) {
		return CreationRecoveryPage{}, errors.New("invalid background response")
	}
	return page, nil
}

func (c *backgroundClient) GetOperation(ctx context.Context, operationID string) (OperationLookup, error) {
	if !validOperationID(operationID) {
		return OperationLookup{}, errors.New("invalid operation id")
	}
	var lookup OperationLookup
	if err := c.post(ctx, BackgroundGetPath, struct {
		OperationID string `json:"operation_id"`
	}{OperationID: operationID}, &lookup); err != nil {
		return OperationLookup{}, err
	}
	if lookup.InstallationID != c.installation || lookup.OperationID != operationID || lookup.Status == "" {
		return OperationLookup{}, errors.New("invalid background response")
	}
	return lookup, nil
}

func (c *backgroundClient) CancelOperation(ctx context.Context, operationID string) (OperationRecord, error) {
	if !validOperationID(operationID) {
		return OperationRecord{}, errors.New("invalid operation id")
	}
	var record OperationRecord
	if err := c.post(ctx, BackgroundCancelPath, struct {
		OperationID string `json:"operation_id"`
	}{OperationID: operationID}, &record); err != nil {
		return OperationRecord{}, err
	}
	if record.InstallationID != c.installation || record.OperationID != operationID || record.Outcome == "" {
		return OperationRecord{}, errors.New("invalid background response")
	}
	return record, nil
}

// UnixPeer identifies a Unix socket peer from kernel credentials.
type UnixPeer struct {
	UID uint32
	GID uint32
	PID int32
}

// FormatUnixPeer encodes kernel peer credentials for an HTTP RemoteAddr so
// private-socket handlers can enforce peer policy without side channels.
func FormatUnixPeer(peer UnixPeer) string {
	return "uid=" + strconv.FormatUint(uint64(peer.UID), 10) + ";gid=" + strconv.FormatUint(uint64(peer.GID), 10) + ";pid=" + strconv.FormatInt(int64(peer.PID), 10)
}

// ParseUnixPeer decodes a FormatUnixPeer RemoteAddr.
func ParseUnixPeer(remoteAddr string) (UnixPeer, error) {
	var peer UnixPeer
	parts := strings.Split(remoteAddr, ";")
	if len(parts) != 3 {
		return peer, errors.New("invalid unix peer address")
	}
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || value == "" {
			return peer, errors.New("invalid unix peer address")
		}
		switch key {
		case "uid", "gid":
			number, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return peer, errors.New("invalid unix peer address")
			}
			if key == "uid" {
				peer.UID = uint32(number)
			} else {
				peer.GID = uint32(number)
			}
		case "pid":
			number, err := strconv.ParseInt(value, 10, 32)
			// PID 0 round-trips a peer in another PID namespace (the
			// cross-container service bridge); only negative PIDs, which
			// the kernel never reports, are malformed.
			if err != nil || number < 0 {
				return peer, errors.New("invalid unix peer address")
			}
			peer.PID = int32(number)
		default:
			return peer, errors.New("invalid unix peer address")
		}
	}
	return peer, nil
}

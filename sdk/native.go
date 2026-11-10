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

	"forgejo.org/extension-sdk/wirejson"
)

const NativeCallbackPath = "/v1/native"

const maxCallbackResponseBytes = 1 << 20

// ErrRepositoryNotVisible is returned only when the host's repository callback
// confirms that a repository is missing or not readable by the admitted actor.
var ErrRepositoryNotVisible = errors.New("repository is not visible")

const (
	OperationCurrentActor              = "actor.current"
	OperationRepository                = "repository.get"
	OperationRepositorySource          = "repository.source"
	OperationRepositoryIssues          = "repository.issues"
	OperationRepositoryComments        = "repository.comments"
	OperationRepositoryCommentSnapshot = "repository.comment_snapshot"
	OperationRepositoryPulls           = "repository.pulls"
	OperationRepositoryPullFiles       = "repository.pull_files"
	OperationRepositoryPullCommits     = "repository.pull_commits"
	OperationRepositoryReviews         = "repository.reviews"
	OperationRepositoryThreads         = "repository.threads"
	OperationRepositoryChecks          = "repository.checks"
	OperationReadableRepositories      = "repositories.readable"
	OperationRepositoryCapabilities    = "repository.capabilities"
	OperationOwnedRepositories         = "repositories.owned.search"
	OperationOrganizationOwner         = "organization.owned"
	OperationPublicSSHKeys             = "user.public_keys"
)

// CallbackRequest is sent only on the private host socket.
// The host resolves the actor from the admission, never from this body.
type CallbackRequest struct {
	RuntimeGrantID              string                                  `json:"runtime_grant_id,omitempty"`
	RuntimeAuthorization        *ApplianceOperationAuthorization        `json:"runtime_authorization,omitempty"`
	RuntimeSelection            *ApplianceRuntimeSelection              `json:"runtime_selection,omitempty"`
	HumanPullCreate             *HumanPullCreateRequest                 `json:"human_pull_create,omitempty"`
	PullCreateOperationID       string                                  `json:"pull_create_operation_id,omitempty"`
	PullCreateInstallationID    string                                  `json:"pull_create_installation_id,omitempty"`
	HumanMerge                  *HumanMergeRequest                      `json:"human_merge,omitempty"`
	MergeOperationID            string                                  `json:"merge_operation_id,omitempty"`
	MergeInstallationID         string                                  `json:"merge_installation_id,omitempty"`
	HumanManualMerge            *HumanManualMergeRequest                `json:"human_manual_merge,omitempty"`
	ManualMergeOperationID      string                                  `json:"manual_merge_operation_id,omitempty"`
	ManualMergeInstallationID   string                                  `json:"manual_merge_installation_id,omitempty"`
	HumanReviewRequest          *HumanReviewRequestChange               `json:"human_review_request,omitempty"`
	ReviewRequestOperationID    string                                  `json:"review_request_operation_id,omitempty"`
	ReviewRequestInstallationID string                                  `json:"review_request_installation_id,omitempty"`
	ReviewRequestSnapshot       *RepositoryReviewRequestSnapshotRequest `json:"review_request_snapshot,omitempty"`
	HumanThread                 *HumanThreadRequest                     `json:"human_thread,omitempty"`
	ThreadOperationID           string                                  `json:"thread_operation_id,omitempty"`
	ThreadInstallationID        string                                  `json:"thread_installation_id,omitempty"`
	ThreadSnapshot              *RepositoryThreadSnapshotRequest        `json:"thread_snapshot,omitempty"`
	HumanInline                 *HumanInlineRequest                     `json:"human_inline,omitempty"`
	InlineOperationID           string                                  `json:"inline_operation_id,omitempty"`
	InlineInstallationID        string                                  `json:"inline_installation_id,omitempty"`
	InlineSnapshot              *RepositoryInlineSnapshotRequest        `json:"inline_snapshot,omitempty"`
	HumanReview                 *HumanReviewRequest                     `json:"human_review,omitempty"`
	ReviewOperationID           string                                  `json:"review_operation_id,omitempty"`
	ReviewInstallationID        string                                  `json:"review_installation_id,omitempty"`
	PullCreateSnapshot          *RepositoryPullCreateSnapshotRequest    `json:"pull_create_snapshot,omitempty"`
	MergeSnapshot               *RepositoryMergeSnapshotRequest         `json:"merge_snapshot,omitempty"`
	ManualMergeSnapshot         *RepositoryManualMergeSnapshotRequest   `json:"manual_merge_snapshot,omitempty"`
	ReviewSnapshot              *RepositoryReviewSnapshotRequest        `json:"review_snapshot,omitempty"`
	HumanState                  *HumanStateRequest                      `json:"human_state,omitempty"`
	StateOperationID            string                                  `json:"state_operation_id,omitempty"`
	StateInstallationID         string                                  `json:"state_installation_id,omitempty"`
	StateSnapshot               *RepositoryStateSnapshotRequest         `json:"state_snapshot,omitempty"`
	LockSnapshot                *RepositoryLockSnapshotRequest          `json:"lock_snapshot,omitempty"`
	HumanLock                   *HumanLockRequest                       `json:"human_lock,omitempty"`
	LockOperationID             string                                  `json:"lock_operation_id,omitempty"`
	LockInstallationID          string                                  `json:"lock_installation_id,omitempty"`
	HumanIssueCreate            *HumanIssueCreateRequest                `json:"human_issue_create,omitempty"`
	IssueCreateOperationID      string                                  `json:"issue_create_operation_id,omitempty"`
	IssueCreateInstallationID   string                                  `json:"issue_create_installation_id,omitempty"`
	HumanEdit                   *HumanEditRequest                       `json:"human_edit,omitempty"`
	EditOperationID             string                                  `json:"edit_operation_id,omitempty"`
	EditInstallationID          string                                  `json:"edit_installation_id,omitempty"`
	EditSnapshot                *RepositoryEditSnapshotRequest          `json:"edit_snapshot,omitempty"`
	HumanComment                *HumanCommentRequest                    `json:"human_comment,omitempty"`
	CommentOperationID          string                                  `json:"comment_operation_id,omitempty"`
	CommentInstallationID       string                                  `json:"comment_installation_id,omitempty"`
	CommentSnapshot             *RepositoryCommentSnapshotRequest       `json:"comment_snapshot,omitempty"`
	Capabilities                *RepositoryCapabilitiesRequest          `json:"capabilities,omitempty"`
	Catalog                     *RepositoryCatalogRequest               `json:"catalog,omitempty"`
	Checks                      *RepositoryChecksRequest                `json:"checks,omitempty"`
	Threads                     *RepositoryThreadsRequest               `json:"threads,omitempty"`
	Reviews                     *RepositoryReviewsRequest               `json:"reviews,omitempty"`
	PullFiles                   *RepositoryPullFilesRequest             `json:"pull_files,omitempty"`
	PullCommits                 *RepositoryPullCommitsRequest           `json:"pull_commits,omitempty"`
	Pulls                       *RepositoryPullsRequest                 `json:"pulls,omitempty"`
	Comments                    *RepositoryCommentsRequest              `json:"comments,omitempty"`
	IssueState                  string                                  `json:"issue_state,omitempty"`
	CompleteIssueBodies         bool                                    `json:"complete_issue_bodies,omitempty"`
	IssueIntake                 *IssueIntakeAuthorizationRequest        `json:"issue_intake,omitempty"`
	Operation                   string                                  `json:"operation"`
	RepositoryID                string                                  `json:"repository_id,omitempty"`
	Query                       string                                  `json:"query,omitempty"`
	Cursor                      string                                  `json:"cursor,omitempty"`
	Limit                       int                                     `json:"limit,omitempty"`
	Organization                string                                  `json:"organization,omitempty"`
	SourceCommit                string                                  `json:"source_commit,omitempty"`
	SourcePaths                 []string                                `json:"source_paths,omitempty"`
	SourceActors                []string                                `json:"source_actors,omitempty"`
	Authority                   Authority                               `json:"authority"`
}

type CallbackResponse struct {
	RuntimeGrant          *ApplianceOperationGrant         `json:"runtime_grant,omitempty"`
	RuntimeController     *RuntimeControllerView           `json:"runtime_controller,omitempty"`
	RuntimeSelection      *RuntimeSelectionReceipt         `json:"runtime_selection,omitempty"`
	HumanPullCreate       *OperationLookup                 `json:"human_pull_create,omitempty"`
	HumanMerge            *OperationLookup                 `json:"human_merge,omitempty"`
	HumanManualMerge      *OperationLookup                 `json:"human_manual_merge,omitempty"`
	HumanReviewRequest    *OperationLookup                 `json:"human_review_request,omitempty"`
	ReviewRequestSnapshot *RepositoryReviewRequestSnapshot `json:"review_request_snapshot,omitempty"`
	HumanThread           *OperationLookup                 `json:"human_thread,omitempty"`
	ThreadSnapshot        *RepositoryThreadSnapshot        `json:"thread_snapshot,omitempty"`
	HumanInline           *OperationLookup                 `json:"human_inline,omitempty"`
	InlineSnapshot        *RepositoryInlineSnapshot        `json:"inline_snapshot,omitempty"`
	HumanReview           *OperationLookup                 `json:"human_review,omitempty"`
	PullCreateSnapshot    *RepositoryPullCreateSnapshot    `json:"pull_create_snapshot,omitempty"`
	MergeSnapshot         *RepositoryMergeSnapshot         `json:"merge_snapshot,omitempty"`
	ManualMergeSnapshot   *RepositoryManualMergeSnapshot   `json:"manual_merge_snapshot,omitempty"`
	ReviewSnapshot        *RepositoryReviewSnapshot        `json:"review_snapshot,omitempty"`
	HumanState            *OperationLookup                 `json:"human_state,omitempty"`
	StateSnapshot         *RepositoryStateSnapshot         `json:"state_snapshot,omitempty"`
	LockSnapshot          *RepositoryLockSnapshot          `json:"lock_snapshot,omitempty"`
	HumanLock             *OperationLookup                 `json:"human_lock,omitempty"`
	HumanIssueCreate      *OperationLookup                 `json:"human_issue_create,omitempty"`
	IssueCreateSnapshot   *RepositoryIssueCreateSnapshot   `json:"issue_create_snapshot,omitempty"`
	HumanEdit             *OperationLookup                 `json:"human_edit,omitempty"`
	EditSnapshot          *RepositoryEditSnapshot          `json:"edit_snapshot,omitempty"`
	HumanComment          *OperationLookup                 `json:"human_comment,omitempty"`
	CommentSnapshot       *RepositoryCommentSnapshot       `json:"comment_snapshot,omitempty"`
	Capabilities          *RepositoryCapabilities          `json:"capabilities,omitempty"`
	Checks                *RepositoryChecks                `json:"checks,omitempty"`
	Threads               *RepositoryThreadPage            `json:"threads,omitempty"`
	Reviews               *RepositoryReviewPage            `json:"reviews,omitempty"`
	PullFiles             *RepositoryPullFiles             `json:"pull_files,omitempty"`
	PullCommits           *RepositoryPullCommits           `json:"pull_commits,omitempty"`
	Pulls                 *RepositoryPullPage              `json:"pulls,omitempty"`
	Comments              *RepositoryCommentPage           `json:"comments,omitempty"`
	IssueIntake           *IssueIntakeGrant                `json:"issue_intake,omitempty"`
	Actor                 *Actor                           `json:"actor,omitempty"`
	Repository            *Repository                      `json:"repository,omitempty"`
	Source                *RepositorySource                `json:"source,omitempty"`
	Issues                *RepositoryIssuePage             `json:"issues,omitempty"`
	Page                  *RepositoryPage                  `json:"page,omitempty"`
	Owner                 *bool                            `json:"owner,omitempty"`
	PublicKeys            []PublicKey                      `json:"public_keys"`
	ErrorCode             string                           `json:"error_code,omitempty"`
}

// RequestContext accepts authority only from the private extension listener.
// The admission is read separately so encoding Authority cannot expose it.
func RequestContext(r *http.Request) (Authority, error) {
	var authority Authority
	value := r.Header.Get(ContextHeader)
	if value == "" || len(value) > 8192 {
		return authority, errors.New("missing or oversized extension authority")
	}
	dec := json.NewDecoder(strings.NewReader(value))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&authority); err != nil {
		return Authority{}, fmt.Errorf("decode extension authority: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Authority{}, errors.New("extension authority must contain one object")
	}
	if authority.ExtensionID == "" || authority.InstanceID == "" || authority.SessionGeneration == "" ||
		authority.Contribution.ID == "" || authority.Contribution.Kind == "" || authority.Contribution.Scope == "" ||
		authority.Contribution.Action == "" || !decimalID(authority.Actor.ID) {
		return Authority{}, errors.New("incomplete extension authority")
	}
	if authority.Repository != nil && !decimalID(authority.Repository.ID) {
		return Authority{}, errors.New("invalid repository id")
	}
	authority.admission = r.Header.Get(AdmissionHeader)
	if authority.admission == "" {
		return Authority{}, errors.New("missing extension admission")
	}
	authority.callbackSocket = os.Getenv(CallbackEnv)
	if serviceSocket := os.Getenv(ServiceCallbackEnv); serviceSocket != "" {
		authority.callbackSocket = serviceSocket
		authority.serviceCallback = true
		if raw := os.Getenv(ServiceCallbackPeerEnv); raw != "" {
			uid, err := strconv.ParseUint(raw, 10, 32)
			if err != nil {
				return Authority{}, errors.New("invalid service callback peer identity")
			}
			authority.servicePeerUID = uint32(uid)
			authority.servicePeerPinned = true
		}
	}
	return authority, nil
}

// LifecycleContext accepts a lifecycle authority only from the private
// extension listener. The opaque manager-issued token arrives in the
// lifecycle admission header; it is held unexported so encoding the
// authority can never expose it. The callback socket selection and
// server-peer pinning reuse the existing callback transport primitives.
// LifecycleContext never consults RequestContext, CurrentActor or a
// fabricated site administrator: authority exists only because the manager
// has already admitted an admin request or is reconciling persisted
// authorized intent. An ordinary Actor admission is rejected here, and a
// lifecycle token is rejected by RequestContext: the two admissions are
// never interchangeable.
func LifecycleContext(r *http.Request) (LifecycleAuthority, error) {
	var authority LifecycleAuthority
	token := r.Header.Get(LifecycleAdmissionHeader)
	if token == "" || len(token) > 8192 {
		return LifecycleAuthority{}, errors.New("missing or oversized lifecycle admission")
	}
	authority.admission = token
	authority.callbackSocket = os.Getenv(CallbackEnv)
	if serviceSocket := os.Getenv(ServiceCallbackEnv); serviceSocket != "" {
		authority.callbackSocket = serviceSocket
		authority.serviceCallback = true
		if raw := os.Getenv(ServiceCallbackPeerEnv); raw != "" {
			uid, err := strconv.ParseUint(raw, 10, 32)
			if err != nil {
				return LifecycleAuthority{}, errors.New("invalid service callback peer identity")
			}
			authority.servicePeerUID = uint32(uid)
			authority.servicePeerPinned = true
		}
	}
	if authority.callbackSocket == "" {
		return LifecycleAuthority{}, errors.New("lifecycle callback is unavailable")
	}
	return authority, nil
}

// lifecycleExpectedUID resolves the pinned server UID for a lifecycle
// authority: per-instance hosts run the callback listener as this
// process's own UID, while the shared service callback pins the fixed
// service identity from the environment.
func (a LifecycleAuthority) lifecycleExpectedUID() uint32 {
	if a.serviceCallback && a.servicePeerPinned {
		return a.servicePeerUID
	}
	return uint32(os.Geteuid())
}

func decimalID(id string) bool {
	if id == "" || id[0] == '0' {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (a Authority) Native() NativeClient {
	return a.nativeClient()
}

func (a Authority) nativeClient() *nativeClient {
	// Per-instance hosts spawn the extension without changing users, so that
	// callback listener runs as this process's own UID. The shared service
	// callback instead runs inside Forgejo under a fixed service identity,
	// pinned by FORGEJO_EXTENSION_SERVICE_CALLBACK_PEER_UID. The dial
	// verifies the listener against that identity before sending anything.
	expected := uint32(os.Geteuid())
	if a.serviceCallback && a.servicePeerPinned {
		expected = a.servicePeerUID
	}
	return &nativeClient{socket: a.callbackSocket, admission: a.admission, authority: a, expectedUID: expected}
}

type nativeClient struct {
	socket      string
	admission   string
	authority   Authority
	expectedUID uint32
}

// dialPinnedUnixSocket dials a private host callback socket and pins the
// listener against the expected server UID via kernel peer credentials,
// reusing the existing server-peer pinning for every callback flavor.
// Merely reaching a writable socket is insufficient: other extension
// processes may share the UID, so the dial verifies the listener identity
// before anything is sent.
func dialPinnedUnixSocket(ctx context.Context, socket string, expectedUID uint32) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	peer, err := PeerCredential(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if peer.UID != expectedUID {
		_ = conn.Close()
		return nil, errors.New("callback host peer is not permitted")
	}
	return conn, nil
}

func (c *nativeClient) exchange(ctx context.Context, request CallbackRequest) (CallbackResponse, error) {
	if c.socket == "" || c.admission == "" {
		return CallbackResponse{}, errors.New("native callback is unavailable")
	}
	request.Authority = c.authority
	data, err := json.Marshal(request)
	if err != nil {
		return CallbackResponse{}, err
	}
	expected := c.expectedUID
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialPinnedUnixSocket(ctx, c.socket, expected)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://extension-host"+NativeCallbackPath, bytes.NewReader(data))
	if err != nil {
		return CallbackResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(AdmissionHeader, c.admission)
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return CallbackResponse{}, ctx.Err()
		}
		return CallbackResponse{}, errors.New("native callback request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return CallbackResponse{}, &callbackHTTPStatus{status: response.StatusCode}
	}
	return decodeCallbackResponse(response.Body, request)
}

func decodeCallbackResponse(body io.Reader, request CallbackRequest) (CallbackResponse, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxCallbackResponseBytes+1))
	if err != nil {
		return CallbackResponse{}, errors.New("read native callback response failed")
	}
	if len(data) > maxCallbackResponseBytes {
		return CallbackResponse{}, errors.New("native callback response exceeds limit")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return CallbackResponse{}, errors.New("native callback response must be an object")
	}
	if err := wirejson.RejectDuplicateKeys(data, "native callback response"); err != nil {
		return CallbackResponse{}, errors.New("invalid native callback response")
	}
	var result CallbackResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return CallbackResponse{}, errors.New("invalid native callback response")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return CallbackResponse{}, errors.New("native callback response must contain one object")
	}
	if result.ErrorCode != "" {
		return CallbackResponse{}, errors.New("native callback rejected request")
	}
	if !validCallbackResult(result, request) {
		return CallbackResponse{}, errors.New("native callback returned an invalid result")
	}
	return result, nil
}

func validCallbackResult(result CallbackResponse, request CallbackRequest) bool {
	if !validCallbackResultEnvelope(result, request) {
		return false
	}
	switch request.Operation {
	case OperationRuntimeControllerRead:
		return result.RuntimeController != nil && validInstallationID(result.RuntimeController.InstallationID) && runtimeHex(result.RuntimeController.RuntimeID) && ValidRuntimePlacement(result.RuntimeController.Placement, request.RepositoryID)
	case OperationRuntimeControllerSelect:
		q, r := request.RuntimeSelection, result.RuntimeSelection
		return q != nil && r != nil && r.CommandID == q.CommandID && (r.Outcome == "applied" || r.Outcome == "refused") && ValidRuntimePlacement(r.Placement, request.RepositoryID)
	case OperationRuntimeControllerAuthorize:
		q, r := request.RuntimeAuthorization, result.RuntimeGrant
		return q != nil && r != nil && r.ID == q.CommandID && r.InstallationID == q.InstallationID && r.RuntimeID == q.RuntimeID && r.RepositoryID == request.RepositoryID && r.ActorID == q.ActorID && r.TokenID == q.TokenID && r.Generation == q.Generation && r.SelectionRevision == q.ExpectedPlacement && r.NotAfter == q.NotAfter && sameRuntimeOperations(q.Operations, r.Operations)
	case OperationRuntimeControllerGrant:
		r := result.RuntimeGrant
		return r != nil && r.ID == request.RuntimeGrantID && r.RepositoryID == request.RepositoryID && validInstallationID(r.InstallationID) && runtimeHex(r.RuntimeID) && runtimeHex(r.SelectionRevision) && decimalID(r.ActorID) && decimalID(r.TokenID) && r.Generation > 0 && r.NotAfter > 0 && len(r.Operations) > 0 && len(r.Operations) <= 10
	case OperationHumanMergeSubmit, OperationHumanMergeGet, OperationHumanMergeCancel:
		return validHumanMergeResult(result.HumanMerge, request)
	case OperationHumanPullCreateSubmit, OperationHumanPullCreateGet, OperationHumanPullCreateCancel:
		return validHumanPullCreateResult(result.HumanPullCreate, request)
	case OperationHumanManualMergeSubmit, OperationHumanManualMergeGet, OperationHumanManualMergeCancel:
		return validHumanManualMergeResult(result.HumanManualMerge, request)
	case OperationHumanReviewRequestSubmit, OperationHumanReviewRequestGet, OperationHumanReviewRequestCancel:
		return validHumanReviewRequestResult(result.HumanReviewRequest, request)
	case OperationHumanThreadSubmit, OperationHumanThreadGet, OperationHumanThreadCancel:
		return validHumanThreadResult(result.HumanThread, request)
	case OperationHumanInlineSubmit, OperationHumanInlineGet, OperationHumanInlineCancel:
		return validHumanInlineResult(result.HumanInline, request)
	case OperationRepositoryReviewRequestSnapshot:
		return request.ReviewRequestSnapshot != nil && ValidRepositoryReviewRequestSnapshot(result.ReviewRequestSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.ReviewRequestSnapshot)
	case OperationRepositoryThreadSnapshot:
		return request.ThreadSnapshot != nil && ValidRepositoryThreadSnapshot(result.ThreadSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.ThreadSnapshot)
	case OperationRepositoryInlineSnapshot:
		return request.InlineSnapshot != nil && validRepositoryInlineSnapshot(result.InlineSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.InlineSnapshot)
	case OperationRepositoryPullCreateSnapshot:
		return request.PullCreateSnapshot != nil && ValidRepositoryPullCreateSnapshot(result.PullCreateSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.PullCreateSnapshot)
	case OperationRepositoryMergeSnapshot:
		return request.MergeSnapshot != nil && ValidRepositoryMergeSnapshot(result.MergeSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.MergeSnapshot)
	case OperationRepositoryManualMergeSnapshot:
		return request.ManualMergeSnapshot != nil && ValidRepositoryManualMergeSnapshot(result.ManualMergeSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.ManualMergeSnapshot)
	case OperationRepositoryReviewSnapshot:
		return request.ReviewSnapshot != nil && validRepositoryReviewSnapshot(result.ReviewSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.ReviewSnapshot)
	case OperationRepositoryStateSnapshot:
		return request.StateSnapshot != nil && validRepositoryStateSnapshot(result.StateSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.StateSnapshot)
	case OperationRepositoryLockSnapshot:
		return request.LockSnapshot != nil && validRepositoryLockSnapshot(result.LockSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.LockSnapshot)
	case OperationHumanReviewSubmit, OperationHumanReviewGet, OperationHumanReviewCancel:
		return validHumanReviewResult(result.HumanReview, request)
	case OperationHumanStateSubmit, OperationHumanStateGet, OperationHumanStateCancel:
		return validHumanStateResult(result.HumanState, request)
	case OperationHumanLockSubmit, OperationHumanLockGet, OperationHumanLockCancel:
		return validHumanLockResult(result.HumanLock, request)
	case OperationHumanIssueCreateSubmit, OperationHumanIssueCreateGet, OperationHumanIssueCreateCancel:
		return validHumanIssueCreateResult(result.HumanIssueCreate, request)
	case OperationRepositoryIssueCreateSnapshot:
		return validRepositoryIssueCreateSnapshot(result.IssueCreateSnapshot, request.RepositoryID, request.Authority.Actor.ID)
	case OperationHumanEditSubmit, OperationHumanEditGet, OperationHumanEditCancel:
		return validHumanEditResult(result.HumanEdit, request)
	case OperationHumanCommentSubmit, OperationHumanCommentGet, OperationHumanCommentCancel:
		return validHumanCommentResult(result.HumanComment, request)
	case OperationRepositoryCapabilities:
		return request.Capabilities != nil && validRepositoryCapabilities(result.Capabilities, request.RepositoryID, *request.Capabilities, request.Authority.Actor.ID)
	case OperationReadableRepositories:
		return request.Catalog != nil && validRepositoryCatalog(result.Page, *request.Catalog)
	case OperationRepositoryChecks:
		return request.Checks != nil && ValidRepositoryChecks(result.Checks, request.RepositoryID, *request.Checks)
	case OperationRepositoryThreads:
		return request.Threads != nil && ValidRepositoryThreadPage(result.Threads, request.RepositoryID, *request.Threads)
	case OperationRepositoryReviews:
		return request.Reviews != nil && ValidRepositoryReviewPage(result.Reviews, request.RepositoryID, *request.Reviews)
	case OperationRepositoryPullFiles:
		return request.PullFiles != nil && ValidRepositoryPullFiles(result.PullFiles, request.RepositoryID, *request.PullFiles)
	case OperationRepositoryPullCommits:
		return request.PullCommits != nil && ValidRepositoryPullCommits(result.PullCommits, request.RepositoryID, *request.PullCommits)
	case OperationRepositoryPulls:
		return request.Pulls != nil && ValidRepositoryPullPage(result.Pulls, request.RepositoryID, *request.Pulls)
	case OperationRepositoryComments:
		return request.Comments != nil && ValidRepositoryCommentPage(result.Comments, request.RepositoryID, *request.Comments)
	case OperationRepositoryEditSnapshot:
		return request.EditSnapshot != nil && validRepositoryEditSnapshot(result.EditSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.EditSnapshot)
	case OperationRepositoryCommentSnapshot:
		return request.CommentSnapshot != nil && validRepositoryCommentSnapshot(result.CommentSnapshot, request.RepositoryID, request.Authority.Actor.ID, *request.CommentSnapshot)
	case OperationIssueIntakeAuthorize:
		return request.IssueIntake != nil && result.IssueIntake != nil && result.IssueIntake.Token != "" && result.IssueIntake.Claims.Repository == request.RepositoryID && result.IssueIntake.Claims.Source == request.IssueIntake.Source && result.IssueIntake.Claims.Revision == request.IssueIntake.Revision && result.IssueIntake.Claims.Actor == request.Authority.Actor.ID
	case OperationRepositoryIssues:
		if result.Issues == nil || result.Issues.RepositoryID != request.RepositoryID || result.Issues.Items == nil || len(result.Issues.Items) > request.Limit {
			return false
		}
		for _, issue := range result.Issues.Items {
			if !decimalID(issue.Index) || request.Query != "" && issue.Index != request.Query {
				return false
			}
			if request.CompleteIssueBodies && (!decimalID(issue.ID) || !issue.BodyComplete || issue.Assignees == nil || issue.Labels == nil) {
				return false
			}
		}
		return result.Issues.NextCursor == "" || decimalID(result.Issues.NextCursor)
	case OperationRepositorySource:
		return ValidRepositorySource(result.Source, request)
	case OperationCurrentActor:
		return result.Actor != nil && decimalID(result.Actor.ID)
	case OperationRepository:
		if result.Repository == nil || !decimalID(result.Repository.ID) || result.Repository.ID != request.RepositoryID || result.Repository.Owner == "" || result.Repository.Name == "" {
			return false
		}
		if len(result.Repository.DefaultBranch) > 1024 || strings.ContainsAny(result.Repository.DefaultBranch, "\x00\r\n") {
			return false
		}
		switch result.Repository.Permission {
		case "read", "issues", "write", "admin":
			return true
		default:
			return false
		}
	case OperationOwnedRepositories:
		if result.Page == nil || result.Page.Items == nil || len(result.Page.Items) > request.Limit {
			return false
		}
		for _, repository := range result.Page.Items {
			if !decimalID(repository.ID) {
				return false
			}
		}
		return true
	case OperationOrganizationOwner:
		return result.Owner != nil
	case OperationPublicSSHKeys:
		if result.PublicKeys == nil {
			return false
		}
		for _, key := range result.PublicKeys {
			if !decimalID(key.ID) || key.Key == "" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (c *nativeClient) CurrentActor(ctx context.Context) (Actor, error) {
	result, err := c.call(ctx, CallbackRequest{Operation: OperationCurrentActor})
	if err != nil {
		return Actor{}, err
	}
	return *result.Actor, nil
}

func (c *nativeClient) Repository(ctx context.Context, id string) (Repository, error) {
	if !decimalID(id) {
		return Repository{}, errors.New("invalid repository id")
	}
	result, err := c.call(ctx, CallbackRequest{Operation: OperationRepository, RepositoryID: id})
	if err != nil {
		return Repository{}, err
	}
	return *result.Repository, nil
}

func (c *nativeClient) SearchOwnedRepositories(ctx context.Context, query, cursor string, limit int) (RepositoryPage, error) {
	if limit < 1 || limit > 100 || len(query) > 256 || len(cursor) > 4096 {
		return RepositoryPage{}, errors.New("invalid repository search bounds")
	}
	result, err := c.call(ctx, CallbackRequest{Operation: OperationOwnedRepositories, Query: query, Cursor: cursor, Limit: limit})
	if err != nil {
		return RepositoryPage{}, err
	}
	return *result.Page, nil
}

func (c *nativeClient) OrganizationOwner(ctx context.Context, organization string) (bool, error) {
	if organization == "" || len(organization) > 255 {
		return false, errors.New("invalid organization")
	}
	result, err := c.call(ctx, CallbackRequest{Operation: OperationOrganizationOwner, Organization: organization})
	if err != nil {
		return false, err
	}
	return *result.Owner, nil
}

func (c *nativeClient) PublicSSHKeys(ctx context.Context) ([]PublicKey, error) {
	result, err := c.call(ctx, CallbackRequest{Operation: OperationPublicSSHKeys})
	if err != nil {
		return nil, err
	}
	return result.PublicKeys, nil
}

func repositoryCallbackStatusError(status int) error {
	return (callbackStatusProfile{
		notFound: ErrRepositoryNotVisible,
	}).forStatus(status)
}

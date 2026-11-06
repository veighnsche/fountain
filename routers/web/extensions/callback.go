// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package extensions

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	extension "forgejo.org/extension-sdk"
	"forgejo.org/models/asymkey"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	access_model "forgejo.org/models/perm/access"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/setting"
	app_context "forgejo.org/services/context"
	extensions_service "forgejo.org/services/extensions"
	nativeoperation_service "forgejo.org/services/nativeoperation"

	"code.forgejo.org/go-chi/session"
)

const maxCallbackRequestBytes = 64 << 10

type callbackAdmission struct {
	method       string
	instanceID   string
	actorID      int64
	sessionID    string
	session      session.Store
	authority    extension.Authority
	capabilities map[string]struct{}
	requestCtx   context.Context
	cancel       context.CancelFunc
}

type admissionRegistry struct {
	mu         sync.Mutex
	entries    map[string]*callbackAdmission
	byInstance map[string]map[string]struct{}
}

var nativeAdmissions = admissionRegistry{entries: make(map[string]*callbackAdmission), byInstance: make(map[string]map[string]struct{})}

func (registry *admissionRegistry) issue(entry *callbackAdmission) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	registry.mu.Lock()
	if registry.entries == nil {
		registry.entries = make(map[string]*callbackAdmission)
	}
	if registry.byInstance == nil {
		registry.byInstance = make(map[string]map[string]struct{})
	}
	registry.entries[token] = entry
	if registry.byInstance[entry.instanceID] == nil {
		registry.byInstance[entry.instanceID] = make(map[string]struct{})
	}
	registry.byInstance[entry.instanceID][token] = struct{}{}
	registry.mu.Unlock()
	return token, nil
}

func (registry *admissionRegistry) get(token string) *callbackAdmission {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.entries[token]
}

func (registry *admissionRegistry) revoke(token string) {
	registry.mu.Lock()
	entry := registry.entries[token]
	delete(registry.entries, token)
	if entry != nil {
		delete(registry.byInstance[entry.instanceID], token)
		if len(registry.byInstance[entry.instanceID]) == 0 {
			delete(registry.byInstance, entry.instanceID)
		}
	}
	registry.mu.Unlock()
	if entry != nil && entry.cancel != nil {
		entry.cancel()
	}
}

func (registry *admissionRegistry) revokeInstance(instanceID string) {
	registry.mu.Lock()
	tokens := registry.byInstance[instanceID]
	entries := make([]*callbackAdmission, 0, len(tokens))
	for token := range tokens {
		if entry := registry.entries[token]; entry != nil {
			entries = append(entries, entry)
			delete(registry.entries, token)
		}
	}
	delete(registry.byInstance, instanceID)
	registry.mu.Unlock()
	for _, entry := range entries {
		if entry.cancel != nil {
			entry.cancel()
		}
	}
}

// RevokeAdmissionsForSession detaches active extension requests at native logout.
func RevokeAdmissionsForSession(sessionID string) {
	nativeAdmissions.mu.Lock()
	var tokens []string
	for token, entry := range nativeAdmissions.entries {
		if entry.sessionID == sessionID {
			tokens = append(tokens, token)
		}
	}
	nativeAdmissions.mu.Unlock()
	for _, token := range tokens {
		nativeAdmissions.revoke(token)
	}
}

// RevokeAdmissionsForInstance cancels all live requests owned by a stopped runtime.
func RevokeAdmissionsForInstance(instanceID string) {
	nativeAdmissions.revokeInstance(instanceID)
}

func sessionGeneration(ctx *app_context.Context) (string, error) {
	if !nativeSession(ctx) || ctx.Session.ID() == "" || setting.SecretKey == "" {
		return "", errors.New("native session generation unavailable")
	}
	mac := hmac.New(sha256.New, []byte(setting.SecretKey))
	_, _ = mac.Write([]byte("forgejo-native-extension-session\x00"))
	_, _ = mac.Write([]byte(ctx.Session.ID()))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strconv.FormatInt(ctx.Doer.ID, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func createAdmission(requestCtx context.Context, ctx *app_context.Context, descriptor extensions_service.Descriptor, generation string, authority extension.Authority) (string, extension.Authority, error) {
	if requestCtx == nil || requestCtx.Err() != nil || descriptor.InstanceID == "" || authority.ExtensionID != descriptor.Manifest.ID || ctx.Doer == nil || authority.Actor.ID != strconv.FormatInt(ctx.Doer.ID, 10) {
		return "", extension.Authority{}, errors.New("native extension admission unavailable")
	}
	if generation == "" {
		return "", extension.Authority{}, errors.New("native extension admission unavailable")
	}
	authority.InstanceID = descriptor.InstanceID
	authority.SessionGeneration = generation
	capabilities := make(map[string]struct{}, len(descriptor.Manifest.Capabilities))
	for _, capability := range descriptor.Manifest.Capabilities {
		capabilities[capability] = struct{}{}
	}
	admissionCtx, cancel := context.WithCancel(requestCtx)
	entry := &callbackAdmission{
		method:       ctx.Req.Method,
		instanceID:   descriptor.InstanceID,
		actorID:      ctx.Doer.ID,
		sessionID:    ctx.Session.ID(),
		session:      ctx.Session,
		authority:    authority,
		capabilities: capabilities,
		requestCtx:   admissionCtx,
		cancel:       cancel,
	}
	token, err := nativeAdmissions.issue(entry)
	if err != nil {
		cancel()
		return "", extension.Authority{}, errors.New("native extension admission unavailable")
	}
	return token, authority, nil
}

// CallbackHandlerForInstance returns the private native capability endpoint
// bound to one extension process instance.
func CallbackHandlerForInstance(instanceID string) http.Handler {
	return callbackHandler(instanceID, false)
}

// CallbackHandlerForService returns the shared application IPC endpoint. It
// accepts only admissions explicitly declared for the service bridge.
func CallbackHandlerForService() http.Handler {
	return callbackHandler("", true)
}

func callbackHandler(instanceID string, service bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != extension.NativeCallbackPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "invalid callback request", http.StatusBadRequest)
			return
		}
		token := r.Header.Get(extension.AdmissionHeader)
		if len(token) != 43 {
			http.Error(w, "native extension admission required", http.StatusUnauthorized)
			return
		}
		entry := nativeAdmissions.get(token)
		if entry == nil || entry.requestCtx.Err() != nil {
			http.Error(w, "native extension admission expired", http.StatusUnauthorized)
			return
		}
		// A fenced package consumes no callbacks: ordinary admission
		// closed after the admission was issued, or the issuing
		// instance was replaced.
		if manager := extensions_service.GetManager(); manager != nil && !manager.VerifyOrdinaryToken(entry.authority.ExtensionID, entry.instanceID) {
			http.Error(w, "native extension admission rejected", http.StatusForbidden)
			return
		}
		if service {
			if _, declared := entry.capabilities[extension.CapabilityServiceBridge]; !declared {
				http.Error(w, "native service bridge not declared", http.StatusForbidden)
				return
			}
		} else if instanceID == "" || entry.instanceID != instanceID {
			http.Error(w, "native extension admission rejected", http.StatusForbidden)
			return
		}
		request, ok := decodeCallbackRequest(w, r)
		if !ok {
			return
		}
		humanMerge := request.Operation == extension.OperationHumanMergeSubmit || request.Operation == extension.OperationHumanMergeGet || request.Operation == extension.OperationHumanMergeCancel
		if !humanMerge && (request.HumanMerge != nil || request.MergeOperationID != "" || request.MergeInstallationID != "") {
			http.Error(w, "unexpected human merge selector", http.StatusBadRequest)
			return
		}
		humanPullCreate := request.Operation == extension.OperationHumanPullCreateSubmit || request.Operation == extension.OperationHumanPullCreateGet || request.Operation == extension.OperationHumanPullCreateCancel
		if !humanPullCreate && (request.HumanPullCreate != nil || request.PullCreateOperationID != "" || request.PullCreateInstallationID != "") {
			http.Error(w, "unexpected human pull creation selector", http.StatusBadRequest)
			return
		}
		humanManualMerge := request.Operation == extension.OperationHumanManualMergeSubmit || request.Operation == extension.OperationHumanManualMergeGet || request.Operation == extension.OperationHumanManualMergeCancel
		if !humanManualMerge && (request.HumanManualMerge != nil || request.ManualMergeOperationID != "" || request.ManualMergeInstallationID != "") {
			http.Error(w, "unexpected human manual merge selector", http.StatusBadRequest)
			return
		}
		humanComment := request.Operation == extension.OperationHumanCommentSubmit || request.Operation == extension.OperationHumanCommentGet || request.Operation == extension.OperationHumanCommentCancel
		humanEdit := request.Operation == extension.OperationHumanEditSubmit || request.Operation == extension.OperationHumanEditGet || request.Operation == extension.OperationHumanEditCancel
		humanReview := request.Operation == extension.OperationHumanReviewSubmit || request.Operation == extension.OperationHumanReviewGet || request.Operation == extension.OperationHumanReviewCancel
		humanReviewRequest := request.Operation == extension.OperationHumanReviewRequestSubmit || request.Operation == extension.OperationHumanReviewRequestGet || request.Operation == extension.OperationHumanReviewRequestCancel
		if !humanReviewRequest && (request.HumanReviewRequest != nil || request.ReviewRequestOperationID != "" || request.ReviewRequestInstallationID != "") {
			http.Error(w, "unexpected human reviewer selector", http.StatusBadRequest)
			return
		}
		humanThread := request.Operation == extension.OperationHumanThreadSubmit || request.Operation == extension.OperationHumanThreadGet || request.Operation == extension.OperationHumanThreadCancel
		if !humanThread && (request.HumanThread != nil || request.ThreadOperationID != "" || request.ThreadInstallationID != "") {
			http.Error(w, "unexpected human thread selector", http.StatusBadRequest)
			return
		}
		humanInline := request.Operation == extension.OperationHumanInlineSubmit || request.Operation == extension.OperationHumanInlineGet || request.Operation == extension.OperationHumanInlineCancel
		if !humanInline && (request.HumanInline != nil || request.InlineOperationID != "" || request.InlineInstallationID != "") {
			http.Error(w, "unexpected human inline selector", http.StatusBadRequest)
			return
		}
		if !humanReview && (request.HumanReview != nil || request.ReviewOperationID != "" || request.ReviewInstallationID != "") {
			http.Error(w, "unexpected human review selector", http.StatusBadRequest)
			return
		}
		humanState := request.Operation == extension.OperationHumanStateSubmit || request.Operation == extension.OperationHumanStateGet || request.Operation == extension.OperationHumanStateCancel
		if !humanState && (request.HumanState != nil || request.StateOperationID != "" || request.StateInstallationID != "") {
			http.Error(w, "unexpected human state selector", http.StatusBadRequest)
			return
		}
		humanLock := request.Operation == extension.OperationHumanLockSubmit || request.Operation == extension.OperationHumanLockGet || request.Operation == extension.OperationHumanLockCancel
		if !humanLock && (request.HumanLock != nil || request.LockOperationID != "" || request.LockInstallationID != "") {
			http.Error(w, "unexpected human lock selector", http.StatusBadRequest)
			return
		}
		humanIssueCreate := request.Operation == extension.OperationHumanIssueCreateSubmit || request.Operation == extension.OperationHumanIssueCreateGet || request.Operation == extension.OperationHumanIssueCreateCancel
		if !humanIssueCreate && (request.HumanIssueCreate != nil || request.IssueCreateOperationID != "" || request.IssueCreateInstallationID != "") {
			http.Error(w, "unexpected human issue creation selector", http.StatusBadRequest)
			return
		}
		if !humanEdit && (request.HumanEdit != nil || request.EditOperationID != "" || request.EditInstallationID != "") {
			http.Error(w, "unexpected human edit selector", http.StatusBadRequest)
			return
		}
		if !humanComment && (request.HumanComment != nil || request.CommentOperationID != "" || request.CommentInstallationID != "") {
			http.Error(w, "unexpected human comment selector", http.StatusBadRequest)
			return
		}
		if !sameAuthority(request.Authority, entry.authority) {
			http.Error(w, "native extension authority rejected", http.StatusForbidden)
			return
		}
		capability, supported := callbackCapability(request.Operation)
		if !supported {
			http.Error(w, "unsupported native capability", http.StatusNotImplemented)
			return
		}
		if _, declared := entry.capabilities[capability]; !declared {
			http.Error(w, "native capability not declared", http.StatusForbidden)
			return
		}
		if request.Operation != extension.OperationRepositorySource && (request.SourceCommit != "" || len(request.SourcePaths) != 0 || len(request.SourceActors) != 0) {
			http.Error(w, "invalid native capability request", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationIssueIntakeAuthorize && request.IssueIntake != nil {
			http.Error(w, "unexpected issue intake fields", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryIssues && request.CompleteIssueBodies {
			http.Error(w, "unexpected issue body selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryIssues && request.IssueState != "" {
			http.Error(w, "unexpected issue state", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryComments && request.Comments != nil {
			http.Error(w, "unexpected discussion selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryCommentSnapshot && request.CommentSnapshot != nil {
			http.Error(w, "unexpected comment snapshot selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryEditSnapshot && request.EditSnapshot != nil {
			http.Error(w, "unexpected edit snapshot selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryReviewRequestSnapshot && request.ReviewRequestSnapshot != nil {
			http.Error(w, "unexpected reviewer selection", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryThreadSnapshot && request.ThreadSnapshot != nil {
			http.Error(w, "unexpected thread preparation selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryInlineSnapshot && request.InlineSnapshot != nil {
			http.Error(w, "unexpected inline preparation selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryPullCreateSnapshot && request.PullCreateSnapshot != nil {
			http.Error(w, "unexpected pull creation preparation selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryMergeSnapshot && request.MergeSnapshot != nil {
			http.Error(w, "unexpected merge preparation selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryManualMergeSnapshot && request.ManualMergeSnapshot != nil {
			http.Error(w, "unexpected manual merge preparation selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryReviewSnapshot && request.ReviewSnapshot != nil {
			http.Error(w, "unexpected review snapshot selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryStateSnapshot && request.StateSnapshot != nil {
			http.Error(w, "unexpected state snapshot selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryLockSnapshot && request.LockSnapshot != nil {
			http.Error(w, "unexpected lock snapshot selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryPulls && request.Pulls != nil {
			http.Error(w, "unexpected pull selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryPullFiles && request.PullFiles != nil {
			http.Error(w, "unexpected pull files selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryPullCommits && request.PullCommits != nil {
			http.Error(w, "unexpected pull commits selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryReviews && request.Reviews != nil {
			http.Error(w, "unexpected review selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryThreads && request.Threads != nil {
			http.Error(w, "unexpected thread selector", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryCapabilities && request.Capabilities != nil {
			http.Error(w, "unexpected capability fields", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationReadableRepositories && request.Catalog != nil {
			http.Error(w, "unexpected repository catalogue fields", http.StatusBadRequest)
			return
		}
		if request.Operation != extension.OperationRepositoryChecks && request.Checks != nil {
			http.Error(w, "unexpected check selector", http.StatusBadRequest)
			return
		}
		switch request.Operation {
		case extension.OperationHumanMergeSubmit, extension.OperationHumanMergeGet, extension.OperationHumanMergeCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanMergeGet && (entry.method != http.MethodPost || !extension.ValidHumanMergeInstallationID(request.MergeInstallationID)) ||
				request.Operation == extension.OperationHumanMergeGet && request.MergeInstallationID != "" {
				http.Error(w, "invalid human merge request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanMergeSubmit {
				q := request.HumanMerge
				if q == nil || request.MergeOperationID != "" || !extension.ValidHumanMergeRequest(*q) || q.Expected.RepositoryID != request.RepositoryID || q.Expected.ActorID != strconv.FormatInt(entry.actorID, 10) {
					http.Error(w, "invalid human merge intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanMerge != nil || !extension.ValidHumanMergeOperationID(request.MergeOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human merge operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanPullCreateSubmit, extension.OperationHumanPullCreateGet, extension.OperationHumanPullCreateCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanPullCreateGet && (entry.method != http.MethodPost || !extension.ValidHumanPullCreateInstallationID(request.PullCreateInstallationID)) ||
				request.Operation == extension.OperationHumanPullCreateGet && request.PullCreateInstallationID != "" {
				http.Error(w, "invalid human pull creation request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanPullCreateSubmit {
				q := request.HumanPullCreate
				if q == nil || request.PullCreateOperationID != "" || !extension.ValidHumanPullCreateRequest(*q) || q.Expected.RepositoryID != request.RepositoryID || q.Expected.ActorID != strconv.FormatInt(entry.actorID, 10) {
					http.Error(w, "invalid human pull creation intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanPullCreate != nil || !extension.ValidHumanPullCreateOperationID(request.PullCreateOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human pull creation operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanManualMergeSubmit, extension.OperationHumanManualMergeGet, extension.OperationHumanManualMergeCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanManualMergeGet && (entry.method != http.MethodPost || !extension.ValidHumanManualMergeInstallationID(request.ManualMergeInstallationID)) ||
				request.Operation == extension.OperationHumanManualMergeGet && request.ManualMergeInstallationID != "" {
				http.Error(w, "invalid human manual merge request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanManualMergeSubmit {
				q := request.HumanManualMerge
				if q == nil || request.ManualMergeOperationID != "" || !extension.ValidHumanManualMergeRequest(*q) || q.Expected.RepositoryID != request.RepositoryID || q.Expected.ActorID != strconv.FormatInt(entry.actorID, 10) {
					http.Error(w, "invalid human manual merge intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanManualMerge != nil || !extension.ValidHumanManualMergeOperationID(request.ManualMergeOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human manual merge operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanReviewRequestSubmit, extension.OperationHumanReviewRequestGet, extension.OperationHumanReviewRequestCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanReviewRequestGet && (entry.method != http.MethodPost || !extension.ValidHumanReviewRequestInstallationID(request.ReviewRequestInstallationID)) ||
				request.Operation == extension.OperationHumanReviewRequestGet && request.ReviewRequestInstallationID != "" {
				http.Error(w, "invalid human reviewer request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanReviewRequestSubmit {
				q := request.HumanReviewRequest
				if q == nil || request.ReviewRequestOperationID != "" || !extension.ValidHumanReviewRequestChange(*q) || q.Expected.Review.RepositoryID != request.RepositoryID || q.Expected.Review.ActorID != strconv.FormatInt(entry.actorID, 10) || !extension.ValidHumanReviewRequestOperationID(q.OperationID, q.Expected.Review.ActorID, request.RepositoryID) {
					http.Error(w, "invalid human reviewer intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanReviewRequest != nil || !extension.ValidHumanReviewRequestOperationID(request.ReviewRequestOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human reviewer operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanThreadSubmit, extension.OperationHumanThreadGet, extension.OperationHumanThreadCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanThreadGet && (entry.method != http.MethodPost || !extension.ValidHumanThreadInstallationID(request.ThreadInstallationID)) ||
				request.Operation == extension.OperationHumanThreadGet && request.ThreadInstallationID != "" {
				http.Error(w, "invalid human thread request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanThreadSubmit {
				q := request.HumanThread
				if q == nil || request.ThreadOperationID != "" || !extension.ValidHumanThreadRequest(*q) || q.Expected.Review.RepositoryID != request.RepositoryID || q.Expected.Review.ActorID != strconv.FormatInt(entry.actorID, 10) || !extension.ValidHumanThreadOperationID(q.OperationID, q.Expected.Review.ActorID, request.RepositoryID) {
					http.Error(w, "invalid human thread intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanThread != nil || !extension.ValidHumanThreadOperationID(request.ThreadOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human thread operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanInlineSubmit, extension.OperationHumanInlineGet, extension.OperationHumanInlineCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanInlineGet && (entry.method != http.MethodPost || !extension.ValidHumanInlineInstallationID(request.InlineInstallationID)) ||
				request.Operation == extension.OperationHumanInlineGet && request.InlineInstallationID != "" {
				http.Error(w, "invalid human inline request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanInlineSubmit {
				q := request.HumanInline
				if q == nil || request.InlineOperationID != "" || !extension.ValidHumanInlineRequest(*q) || q.Expected.Review.RepositoryID != request.RepositoryID || q.Expected.Review.ActorID != strconv.FormatInt(entry.actorID, 10) || !extension.ValidHumanInlineOperationID(q.OperationID, q.Expected.Review.ActorID, request.RepositoryID) {
					http.Error(w, "invalid human inline intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanInline != nil || !extension.ValidHumanInlineOperationID(request.InlineOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human inline operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanReviewSubmit, extension.OperationHumanReviewGet, extension.OperationHumanReviewCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanReviewGet && (entry.method != http.MethodPost || !extension.ValidHumanReviewInstallationID(request.ReviewInstallationID)) ||
				request.Operation == extension.OperationHumanReviewGet && request.ReviewInstallationID != "" {
				http.Error(w, "invalid human review request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanReviewSubmit {
				q := request.HumanReview
				if q == nil || request.ReviewOperationID != "" || !extension.ValidHumanReviewRequest(*q) || q.Expected.RepositoryID != request.RepositoryID || q.Expected.ActorID != strconv.FormatInt(entry.actorID, 10) || !extension.ValidHumanReviewOperationID(q.OperationID, q.Expected.ActorID, request.RepositoryID) {
					http.Error(w, "invalid human review intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanReview != nil || !extension.ValidHumanReviewOperationID(request.ReviewOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human review operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanStateSubmit, extension.OperationHumanStateGet, extension.OperationHumanStateCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanStateGet && (entry.method != http.MethodPost || !extension.ValidHumanStateInstallationID(request.StateInstallationID)) ||
				request.Operation == extension.OperationHumanStateGet && request.StateInstallationID != "" {
				http.Error(w, "invalid human state request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanStateSubmit {
				q := request.HumanState
				if q == nil || request.StateOperationID != "" || !extension.ValidHumanStateRequest(*q) || q.Expected.RepositoryID != request.RepositoryID || q.Expected.ActorID != strconv.FormatInt(entry.actorID, 10) || !extension.ValidHumanStateOperationID(q.OperationID, q.Expected.ActorID, request.RepositoryID) {
					http.Error(w, "invalid human state intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanState != nil || !extension.ValidHumanStateOperationID(request.StateOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human state operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanLockSubmit, extension.OperationHumanLockGet, extension.OperationHumanLockCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanLockGet && (entry.method != http.MethodPost || !extension.ValidHumanLockInstallationID(request.LockInstallationID)) ||
				request.Operation == extension.OperationHumanLockGet && request.LockInstallationID != "" {
				http.Error(w, "invalid human lock request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanLockSubmit {
				q := request.HumanLock
				if q == nil || request.LockOperationID != "" || !extension.ValidHumanLockRequest(*q) || q.Expected.RepositoryID != request.RepositoryID || q.Expected.ActorID != strconv.FormatInt(entry.actorID, 10) || !extension.ValidHumanLockOperationID(q.OperationID, q.Expected.ActorID, request.RepositoryID) {
					http.Error(w, "invalid human lock intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanLock != nil || !extension.ValidHumanLockOperationID(request.LockOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human lock operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanIssueCreateSubmit, extension.OperationHumanIssueCreateGet, extension.OperationHumanIssueCreateCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanIssueCreateGet && (entry.method != http.MethodPost || !extension.ValidHumanIssueCreateInstallationID(request.IssueCreateInstallationID)) ||
				request.Operation == extension.OperationHumanIssueCreateGet && request.IssueCreateInstallationID != "" {
				http.Error(w, "invalid human issue creation request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanIssueCreateSubmit {
				q := request.HumanIssueCreate
				if q == nil || request.IssueCreateOperationID != "" || !extension.ValidHumanIssueCreateRequest(*q) || q.Expected.RepositoryID != request.RepositoryID || q.Expected.ActorID != strconv.FormatInt(entry.actorID, 10) || !extension.ValidHumanIssueCreateOperationID(q.OperationID, q.Expected.ActorID, request.RepositoryID) {
					http.Error(w, "invalid human issue creation intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanIssueCreate != nil || !extension.ValidHumanIssueCreateOperationID(request.IssueCreateOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human issue creation operation", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryIssueCreateSnapshot:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid issue creation preparation query", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanEditSubmit, extension.OperationHumanEditGet, extension.OperationHumanEditCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanEditGet && (entry.method != http.MethodPost || !extension.ValidHumanEditInstallationID(request.EditInstallationID)) ||
				request.Operation == extension.OperationHumanEditGet && request.EditInstallationID != "" {
				http.Error(w, "invalid human edit request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanEditSubmit {
				q := request.HumanEdit
				if q == nil || request.EditOperationID != "" || !extension.ValidHumanEditRequest(*q) || q.Expected.RepositoryID != request.RepositoryID || q.Expected.ActorID != strconv.FormatInt(entry.actorID, 10) || !extension.ValidHumanEditOperationID(q.OperationID, q.Expected.ActorID, request.RepositoryID) {
					http.Error(w, "invalid human edit intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanEdit != nil || !extension.ValidHumanEditOperationID(request.EditOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human edit operation", http.StatusBadRequest)
				return
			}
		case extension.OperationHumanCommentSubmit, extension.OperationHumanCommentGet, extension.OperationHumanCommentCancel:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" ||
				request.Operation != extension.OperationHumanCommentGet && (entry.method != http.MethodPost || !extension.ValidHumanCommentInstallationID(request.CommentInstallationID)) ||
				request.Operation == extension.OperationHumanCommentGet && request.CommentInstallationID != "" {
				http.Error(w, "invalid human comment request", http.StatusBadRequest)
				return
			}
			if request.Operation == extension.OperationHumanCommentSubmit {
				q := request.HumanComment
				if q == nil || request.CommentOperationID != "" || !extension.ValidHumanCommentRequest(*q) || q.Expected.RepositoryID != request.RepositoryID || q.Expected.ActorID != strconv.FormatInt(entry.actorID, 10) || !extension.ValidHumanCommentOperationID(q.OperationID, q.Expected.ActorID, request.RepositoryID) {
					http.Error(w, "invalid human comment intent", http.StatusBadRequest)
					return
				}
			} else if request.HumanComment != nil || !extension.ValidHumanCommentOperationID(request.CommentOperationID, strconv.FormatInt(entry.actorID, 10), request.RepositoryID) {
				http.Error(w, "invalid human comment operation", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryEditSnapshot:
			if request.EditSnapshot == nil || !extension.ValidRepositoryEditSnapshotRequest(*request.EditSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid edit snapshot query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryReviewRequestSnapshot:
			if request.ReviewRequestSnapshot == nil || !extension.ValidRepositoryReviewRequestSnapshotRequest(*request.ReviewRequestSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid reviewer selection", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryThreadSnapshot:
			if request.ThreadSnapshot == nil || !extension.ValidRepositoryThreadSnapshotRequest(*request.ThreadSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid thread preparation query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryInlineSnapshot:
			if request.InlineSnapshot == nil || !extension.ValidRepositoryInlineSnapshotRequest(*request.InlineSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid inline preparation query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryPullCreateSnapshot:
			if request.PullCreateSnapshot == nil || !extension.ValidRepositoryPullCreateSnapshotRequest(*request.PullCreateSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid pull creation preparation", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryMergeSnapshot:
			if request.MergeSnapshot == nil || !extension.ValidRepositoryMergeSnapshotRequest(*request.MergeSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid merge preparation query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryManualMergeSnapshot:
			if request.ManualMergeSnapshot == nil || !extension.ValidRepositoryManualMergeSnapshotRequest(*request.ManualMergeSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid manual merge preparation query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryReviewSnapshot:
			if request.ReviewSnapshot == nil || !extension.ValidRepositoryReviewSnapshotRequest(*request.ReviewSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid review preparation query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryStateSnapshot:
			if request.StateSnapshot == nil || !extension.ValidRepositoryStateSnapshotRequest(*request.StateSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid state preparation query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryLockSnapshot:
			if request.LockSnapshot == nil || !extension.ValidRepositoryLockSnapshotRequest(*request.LockSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid lock preparation query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryCommentSnapshot:
			if request.CommentSnapshot == nil || !extension.ValidRepositoryCommentSnapshotRequest(*request.CommentSnapshot) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid comment snapshot query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryCapabilities:
			if request.Capabilities == nil || !extension.ValidRepositoryCapabilitiesRequest(*request.Capabilities) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid capability query", http.StatusBadRequest)
				return
			}
		case extension.OperationReadableRepositories:
			if request.Catalog == nil || !extension.ValidRepositoryCatalogRequest(*request.Catalog) || request.RepositoryID != "" || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid native catalogue query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryChecks:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Checks == nil || !extension.ValidRepositoryChecksRequest(*request.Checks) || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid check query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryThreads:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Threads == nil || !extension.ValidRepositoryThreadsRequest(*request.Threads) || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid thread query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryReviews:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Reviews == nil || !extension.ValidRepositoryReviewsRequest(*request.Reviews) || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid review query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryPullFiles:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.PullFiles == nil || !extension.ValidRepositoryPullFilesRequest(*request.PullFiles) || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid pull files query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryPullCommits:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.PullCommits == nil || !extension.ValidRepositoryPullCommitsRequest(*request.PullCommits) || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid pull commits query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryPulls:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Pulls == nil || !extension.ValidRepositoryPullsRequest(*request.Pulls) || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid pull query", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryComments:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Comments == nil || !extension.ValidRepositoryCommentsRequest(*request.Comments) || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid discussion request", http.StatusBadRequest)
				return
			}
		case extension.OperationIssueIntakeAuthorize:
			if entry.method != http.MethodPost || request.IssueIntake == nil || !extension.ValidIssueIntakeRequest(*request.IssueIntake) || mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid issue intake authorization", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositoryIssues:
			if request.IssueState != "" && request.IssueState != "open" && request.IssueState != "closed" && request.IssueState != "all" || request.Query != "" && request.IssueState != "" {
				http.Error(w, "invalid issue state", http.StatusBadRequest)
				return
			}
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Limit < 1 || request.Limit > 50 || request.Organization != "" || request.Cursor != "" && mustParsePositiveID(request.Cursor) == 0 || request.Query != "" && (mustParsePositiveID(request.Query) == 0 || request.Cursor != "") {
				http.Error(w, "invalid native issue request", http.StatusBadRequest)
				return
			}
		case extension.OperationRepositorySource:
			if extension.ValidateRepositorySourceRequest(request) != nil || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid native capability request", http.StatusBadRequest)
				return
			}
		case extension.OperationCurrentActor:
			if request.RepositoryID != "" || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid native capability request", http.StatusBadRequest)
				return
			}
		case extension.OperationRepository:
			if mustParsePositiveID(request.RepositoryID) == 0 || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid native capability request", http.StatusBadRequest)
				return
			}
		case extension.OperationOwnedRepositories:
			if request.RepositoryID != "" || request.Organization != "" || request.Limit < 1 || request.Limit > 100 || len(request.Query) > 256 || len(request.Cursor) > 4096 {
				http.Error(w, "invalid native capability request", http.StatusBadRequest)
				return
			}
		case extension.OperationOrganizationOwner:
			if request.RepositoryID != "" || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization == "" || len(request.Organization) > 255 {
				http.Error(w, "invalid native capability request", http.StatusBadRequest)
				return
			}
		case extension.OperationPublicSSHKeys:
			if request.RepositoryID != "" || request.Query != "" || request.Cursor != "" || request.Limit != 0 || request.Organization != "" {
				http.Error(w, "invalid native capability request", http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "unsupported native capability", http.StatusNotImplemented)
			return
		}
		callbackCtx, cancel := context.WithTimeout(entry.requestCtx, 5*time.Second)
		stop := context.AfterFunc(r.Context(), cancel)
		defer func() {
			stop()
			cancel()
		}()
		var response extension.CallbackResponse
		var status int
		switch request.Operation {
		case extension.OperationRepositoryReviewRequestSnapshot:
			response.ReviewRequestSnapshot, status = resolveReviewRequestSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "reviewer selection unavailable", status)
				return
			}
		case extension.OperationRepositoryThreadSnapshot:
			response.ThreadSnapshot, status = resolveThreadSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "thread preparation is unavailable", status)
				return
			}
		case extension.OperationRepositoryInlineSnapshot:
			response.InlineSnapshot, status = resolveInlineSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "inline preparation is unavailable", status)
				return
			}
		case extension.OperationRepositoryPullCreateSnapshot:
			response.PullCreateSnapshot, status = resolvePullCreateSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "pull creation preparation unavailable", status)
				return
			}
		case extension.OperationRepositoryMergeSnapshot:
			response.MergeSnapshot, status = resolveMergeSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "merge preparation unavailable", status)
				return
			}
		case extension.OperationRepositoryManualMergeSnapshot:
			response.ManualMergeSnapshot, status = resolveManualMergeSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "manual merge preparation unavailable", status)
				return
			}
		case extension.OperationRepositoryReviewSnapshot:
			response.ReviewSnapshot, status = resolveReviewSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "review preparation is unavailable", status)
				return
			}
		case extension.OperationRepositoryStateSnapshot:
			response.StateSnapshot, status = resolveStateSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "state preparation is unavailable", status)
				return
			}
		case extension.OperationRepositoryLockSnapshot:
			response.LockSnapshot, status = resolveLockSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "lock preparation is unavailable", status)
				return
			}
		case extension.OperationHumanMergeSubmit, extension.OperationHumanMergeGet, extension.OperationHumanMergeCancel:
			response.HumanMerge, status = resolveHumanMerge(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human merge operation is unavailable", status)
				return
			}
		case extension.OperationHumanPullCreateSubmit, extension.OperationHumanPullCreateGet, extension.OperationHumanPullCreateCancel:
			response.HumanPullCreate, status = resolveHumanPullCreate(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human pull creation operation is unavailable", status)
				return
			}
		case extension.OperationHumanManualMergeSubmit, extension.OperationHumanManualMergeGet, extension.OperationHumanManualMergeCancel:
			response.HumanManualMerge, status = resolveHumanManualMerge(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human manual merge operation is unavailable", status)
				return
			}
		case extension.OperationHumanReviewRequestSubmit, extension.OperationHumanReviewRequestGet, extension.OperationHumanReviewRequestCancel:
			response.HumanReviewRequest, status = resolveHumanReviewRequest(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human thread operation is unavailable", status)
				return
			}
		case extension.OperationHumanThreadSubmit, extension.OperationHumanThreadGet, extension.OperationHumanThreadCancel:
			response.HumanThread, status = resolveHumanThread(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human thread operation is unavailable", status)
				return
			}
		case extension.OperationHumanInlineSubmit, extension.OperationHumanInlineGet, extension.OperationHumanInlineCancel:
			response.HumanInline, status = resolveHumanInline(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human inline operation is unavailable", status)
				return
			}
		case extension.OperationHumanReviewSubmit, extension.OperationHumanReviewGet, extension.OperationHumanReviewCancel:
			response.HumanReview, status = resolveHumanReview(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human review operation is unavailable", status)
				return
			}
		case extension.OperationHumanStateSubmit, extension.OperationHumanStateGet, extension.OperationHumanStateCancel:
			response.HumanState, status = resolveHumanState(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human state operation is unavailable", status)
				return
			}
		case extension.OperationHumanLockSubmit, extension.OperationHumanLockGet, extension.OperationHumanLockCancel:
			response.HumanLock, status = resolveHumanLock(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human lock operation is unavailable", status)
				return
			}
		case extension.OperationHumanIssueCreateSubmit, extension.OperationHumanIssueCreateGet, extension.OperationHumanIssueCreateCancel:
			response.HumanIssueCreate, status = resolveHumanIssueCreate(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human issue creation operation is unavailable", status)
				return
			}
		case extension.OperationRepositoryIssueCreateSnapshot:
			response.IssueCreateSnapshot, status = resolveIssueCreateSnapshot(callbackCtx, entry, request.RepositoryID)
			if status != http.StatusOK {
				http.Error(w, "issue creation preparation is unavailable", status)
				return
			}
		case extension.OperationHumanEditSubmit, extension.OperationHumanEditGet, extension.OperationHumanEditCancel:
			response.HumanEdit, status = resolveHumanEdit(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human edit operation is unavailable", status)
				return
			}
		case extension.OperationHumanCommentSubmit, extension.OperationHumanCommentGet, extension.OperationHumanCommentCancel:
			response.HumanComment, status = resolveHumanComment(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native human comment operation is unavailable", status)
				return
			}
		case extension.OperationRepositoryEditSnapshot:
			response.EditSnapshot, status = resolveEditSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "edit preparation is unavailable", status)
				return
			}
		case extension.OperationRepositoryCommentSnapshot:
			response.CommentSnapshot, status = resolveCommentSnapshot(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "comment preparation is unavailable", status)
				return
			}
		case extension.OperationIssueIntakeAuthorize:
			if _, status = resolveRepository(callbackCtx, entry, request.RepositoryID); status != http.StatusOK {
				http.Error(w, "repository is unavailable", status)
				return
			}
			user, userStatus := resolveCurrentUser(callbackCtx, entry)
			if userStatus != http.StatusOK {
				http.Error(w, "actor is unavailable", userStatus)
				return
			}
			manager := extensions_service.GetManager()
			if manager == nil {
				http.Error(w, "installation is unavailable", http.StatusServiceUnavailable)
				return
			}
			installation, ok := manager.AdmittedInstallation(entry.authority.ExtensionID, entry.instanceID)
			if !ok {
				http.Error(w, "installation is unavailable", http.StatusForbidden)
				return
			}
			grant, err := nativeoperation_service.MintIssueIntakeGrant(callbackCtx, extension.IssueIntakeClaims{Installation: installation, Actor: strconv.FormatInt(user.ID, 10), Repository: request.RepositoryID, Source: request.IssueIntake.Source, Revision: request.IssueIntake.Revision, Digest: request.IssueIntake.Digest})
			if err != nil {
				http.Error(w, "native issue-write authority is unavailable", http.StatusForbidden)
				return
			}
			response.IssueIntake = &grant
		case extension.OperationRepositoryIssues:
			response.Issues, status = resolveRepositoryIssues(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native repository issues are unavailable", status)
				return
			}
		case extension.OperationRepositoryCapabilities:
			response.Capabilities, status = resolveRepositoryCapabilities(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native capabilities unavailable", status)
				return
			}
		case extension.OperationReadableRepositories:
			response.Page, status = readableRepositoryCatalog(callbackCtx, entry, *request.Catalog)
			if status != http.StatusOK {
				http.Error(w, "native repository catalogue unavailable", status)
				return
			}
		case extension.OperationRepositoryChecks:
			response.Checks, status = resolveRepositoryChecks(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native checks unavailable", status)
				return
			}
		case extension.OperationRepositoryThreads:
			response.Threads, status = resolveRepositoryThreads(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native threads unavailable", status)
				return
			}
		case extension.OperationRepositoryReviews:
			response.Reviews, status = resolveRepositoryReviews(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native reviews unavailable", status)
				return
			}
		case extension.OperationRepositoryPullFiles:
			response.PullFiles, status = resolveRepositoryPullFiles(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native pull files unavailable", status)
				return
			}
		case extension.OperationRepositoryPullCommits:
			response.PullCommits, status = resolveRepositoryPullCommits(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native pull commits unavailable", status)
				return
			}
		case extension.OperationRepositoryPulls:
			response.Pulls, status = resolveRepositoryPulls(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native pulls unavailable", status)
				return
			}
		case extension.OperationRepositoryComments:
			response.Comments, status = resolveRepositoryComments(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native discussion unavailable", status)
				return
			}
		case extension.OperationRepositorySource:
			response.Source, status = resolveRepositorySource(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native repository source is unavailable", status)
				return
			}
		case extension.OperationCurrentActor:
			actor, actorStatus := resolveCurrentActor(callbackCtx, entry)
			status = actorStatus
			if status != http.StatusOK {
				http.Error(w, "native actor is unavailable", status)
				return
			}
			response.Actor = &actor
		case extension.OperationRepository:
			response.Repository, status = resolveRepository(callbackCtx, entry, request.RepositoryID)
			if status != http.StatusOK {
				http.Error(w, "native repository is unavailable", status)
				return
			}
		case extension.OperationOwnedRepositories:
			response.Page, status = searchOwnedRepositories(callbackCtx, entry, request)
			if status != http.StatusOK {
				http.Error(w, "native repositories are unavailable", status)
				return
			}
		case extension.OperationOrganizationOwner:
			response.Owner, status = organizationOwner(callbackCtx, entry, request.Organization)
			if status != http.StatusOK {
				http.Error(w, "native organization authority is unavailable", status)
				return
			}
		case extension.OperationPublicSSHKeys:
			response.PublicKeys, status = publicSSHKeys(callbackCtx, entry)
			if status != http.StatusOK {
				http.Error(w, "native public keys are unavailable", status)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(response)
	})
}

type repositorySearchCursor struct {
	Page  int    `json:"page"`
	Limit int    `json:"limit"`
	Query string `json:"query"`
}

func searchOwnedRepositories(ctx context.Context, admission *callbackAdmission, request extension.CallbackRequest) (*extension.RepositoryPage, int) {
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	page := 1
	if request.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		var cursor repositorySearchCursor
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err != nil || decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Page < 2 || cursor.Page > 10000 || cursor.Limit != request.Limit || cursor.Query != request.Query {
			return nil, http.StatusBadRequest
		}
		page = cursor.Page
	}
	options := &repo_model.SearchRepoOptions{
		ListOptions: db.ListOptions{Page: page, PageSize: request.Limit},
		Actor:       user,
		Keyword:     request.Query,
		OwnerID:     user.ID,
		Private:     true,
		Collaborate: optional.Some(false),
		UnitType:    unit.TypeCode,
	}
	repositories, total, err := repo_model.SearchRepositoryByName(ctx, options)
	if ctx.Err() != nil {
		return nil, http.StatusUnauthorized
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	pageResult := &extension.RepositoryPage{Items: make([]extension.Repository, 0, min(len(repositories), request.Limit))}
	hasMore := int64(page*request.Limit) < total
	for _, repository := range repositories {
		resolved, resultStatus := resolveRepository(ctx, admission, strconv.FormatInt(repository.ID, 10))
		if resultStatus == http.StatusNotFound {
			continue
		}
		if resultStatus != http.StatusOK {
			return nil, resultStatus
		}
		pageResult.Items = append(pageResult.Items, *resolved)
	}
	if hasMore {
		if page == 10000 {
			return nil, http.StatusBadRequest
		}
		cursor, err := json.Marshal(repositorySearchCursor{Page: page + 1, Limit: request.Limit, Query: request.Query})
		if err != nil {
			return nil, http.StatusServiceUnavailable
		}
		pageResult.NextCursor = base64.RawURLEncoding.EncodeToString(cursor)
	}
	return pageResult, http.StatusOK
}

func organizationOwner(ctx context.Context, admission *callbackAdmission, name string) (*bool, int) {
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	org, err := organization.GetOrgByName(ctx, name)
	if ctx.Err() != nil {
		return nil, http.StatusUnauthorized
	}
	if organization.IsErrOrgNotExist(err) {
		owner := false
		return &owner, http.StatusOK
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	ownerValue, err := organization.IsOrganizationOwner(ctx, org.ID, user.ID)
	if ctx.Err() != nil {
		return nil, http.StatusUnauthorized
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	return &ownerValue, http.StatusOK
}

func publicSSHKeys(ctx context.Context, admission *callbackAdmission) ([]extension.PublicKey, int) {
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	keys, count, err := db.FindAndCount[asymkey.PublicKey](ctx, asymkey.FindPublicKeyOptions{
		ListOptions: db.ListOptions{Page: 1, PageSize: 101},
		OwnerID:     user.ID,
		KeyTypes:    []asymkey.KeyType{asymkey.KeyTypeUser},
	})
	if ctx.Err() != nil {
		return nil, http.StatusUnauthorized
	}
	if err != nil || count > 100 {
		return nil, http.StatusServiceUnavailable
	}
	result := make([]extension.PublicKey, 0, len(keys))
	for _, key := range keys {
		result = append(result, extension.PublicKey{ID: strconv.FormatInt(key.ID, 10), Key: key.Content})
	}
	return result, http.StatusOK
}

func sameAuthority(actual, expected extension.Authority) bool {
	if actual.ExtensionID != expected.ExtensionID || actual.InstanceID != expected.InstanceID ||
		actual.SessionGeneration != expected.SessionGeneration || actual.Contribution != expected.Contribution ||
		actual.Actor != expected.Actor {
		return false
	}
	if actual.Repository == nil || expected.Repository == nil {
		return actual.Repository == nil && expected.Repository == nil
	}
	return *actual.Repository == *expected.Repository
}

func callbackCapability(operation string) (string, bool) {
	switch operation {
	case extension.OperationHumanMergeSubmit, extension.OperationHumanMergeGet, extension.OperationHumanMergeCancel:
		return extension.CapabilityHumanMerges, true
	case extension.OperationHumanPullCreateSubmit, extension.OperationHumanPullCreateGet, extension.OperationHumanPullCreateCancel:
		return extension.CapabilityHumanPullCreate, true
	case extension.OperationHumanManualMergeSubmit, extension.OperationHumanManualMergeGet, extension.OperationHumanManualMergeCancel:
		return extension.CapabilityHumanMerges, true
	case extension.OperationHumanReviewRequestSubmit, extension.OperationHumanReviewRequestGet, extension.OperationHumanReviewRequestCancel:
		return extension.CapabilityHumanReviews, true
	case extension.OperationHumanThreadSubmit, extension.OperationHumanThreadGet, extension.OperationHumanThreadCancel:
		return extension.CapabilityHumanReviews, true
	case extension.OperationHumanInlineSubmit, extension.OperationHumanInlineGet, extension.OperationHumanInlineCancel:
		return extension.CapabilityHumanReviews, true
	case extension.OperationHumanReviewSubmit, extension.OperationHumanReviewGet, extension.OperationHumanReviewCancel:
		return extension.CapabilityHumanReviews, true
	case extension.OperationHumanStateSubmit, extension.OperationHumanStateGet, extension.OperationHumanStateCancel:
		return extension.CapabilityHumanStates, true
	case extension.OperationHumanLockSubmit, extension.OperationHumanLockGet, extension.OperationHumanLockCancel:
		return extension.CapabilityHumanLocks, true
	case extension.OperationHumanIssueCreateSubmit, extension.OperationHumanIssueCreateGet, extension.OperationHumanIssueCreateCancel:
		return extension.CapabilityHumanIssueCreate, true
	case extension.OperationHumanEditSubmit, extension.OperationHumanEditGet, extension.OperationHumanEditCancel:
		return extension.CapabilityHumanEdits, true
	case extension.OperationHumanCommentSubmit, extension.OperationHumanCommentGet, extension.OperationHumanCommentCancel:
		return extension.CapabilityHumanComments, true
	case extension.OperationIssueIntakeAuthorize:
		return extension.CapabilityIssueIntake, true
	case extension.OperationCurrentActor:
		return extension.CapabilityActorRead, true
	case extension.OperationRepositoryManualMergeSnapshot:
		return extension.CapabilityRepositoryRead, true
	case extension.OperationRepositoryPullCreateSnapshot, extension.OperationRepositoryMergeSnapshot, extension.OperationRepositoryReviewRequestSnapshot, extension.OperationRepositoryThreadSnapshot, extension.OperationRepositoryInlineSnapshot, extension.OperationRepositoryReviewSnapshot, extension.OperationRepositoryStateSnapshot, extension.OperationRepositoryLockSnapshot, extension.OperationRepositoryIssueCreateSnapshot, extension.OperationRepositoryEditSnapshot, extension.OperationRepositoryCommentSnapshot, extension.OperationRepositoryCapabilities, extension.OperationReadableRepositories, extension.OperationRepository, extension.OperationRepositorySource, extension.OperationRepositoryIssues, extension.OperationRepositoryComments, extension.OperationRepositoryPulls, extension.OperationRepositoryPullFiles, extension.OperationRepositoryPullCommits, extension.OperationRepositoryReviews, extension.OperationRepositoryThreads, extension.OperationRepositoryChecks:
		return extension.CapabilityRepositoryRead, true
	case extension.OperationOwnedRepositories:
		return extension.CapabilityOwnedRepositoriesSearch, true
	case extension.OperationOrganizationOwner:
		return extension.CapabilityOrganizationOwnership, true
	case extension.OperationPublicSSHKeys:
		return extension.CapabilityPublicKeysRead, true
	default:
		return "", false
	}
}

func decodeCallbackRequest(w http.ResponseWriter, r *http.Request) (extension.CallbackRequest, bool) {
	const maximum = max(extension.MaxHumanPullCreateCallbackBytes, extension.MaxHumanInlineCallbackBytes, extension.MaxHumanReviewCallbackBytes, extension.MaxHumanCommentCallbackBytes, extension.MaxHumanEditCallbackBytes, extension.MaxHumanIssueCreateCallbackBytes, extension.MaxHumanLockCallbackBytes, extension.MaxHumanMergeCallbackBytes)
	if r.ContentLength > maximum {
		http.Error(w, "callback request exceeds limits", http.StatusRequestEntityTooLarge)
		return extension.CallbackRequest{}, false
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		http.Error(w, "invalid callback request", http.StatusBadRequest)
		return extension.CallbackRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request extension.CallbackRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid callback request", http.StatusBadRequest)
		return extension.CallbackRequest{}, false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid callback request", http.StatusBadRequest)
		return extension.CallbackRequest{}, false
	}
	if request.Operation == extension.OperationHumanPullCreateSubmit && len(data) > extension.MaxHumanPullCreateCallbackBytes {
		http.Error(w, "callback request exceeds limits", http.StatusRequestEntityTooLarge)
		return extension.CallbackRequest{}, false
	}
	if request.Operation == extension.OperationHumanMergeSubmit && len(data) > extension.MaxHumanMergeCallbackBytes {
		http.Error(w, "callback request exceeds limits", http.StatusRequestEntityTooLarge)
		return extension.CallbackRequest{}, false
	}
	if request.Operation != extension.OperationHumanPullCreateSubmit && request.Operation != extension.OperationHumanInlineSubmit && request.Operation != extension.OperationHumanReviewSubmit && request.Operation != extension.OperationHumanCommentSubmit && request.Operation != extension.OperationHumanEditSubmit && request.Operation != extension.OperationHumanIssueCreateSubmit && request.Operation != extension.OperationHumanLockSubmit && request.Operation != extension.OperationHumanMergeSubmit && len(data) > maxCallbackRequestBytes {
		http.Error(w, "callback request exceeds limits", http.StatusRequestEntityTooLarge)
		return extension.CallbackRequest{}, false
	}
	return request, true
}

func resolveCurrentActor(ctx context.Context, admission *callbackAdmission) (extension.Actor, int) {
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return extension.Actor{}, status
	}
	return extension.Actor{ID: strconv.FormatInt(user.ID, 10), Username: user.Name, SiteAdmin: user.IsAdmin}, http.StatusOK
}

func resolveRepository(ctx context.Context, admission *callbackAdmission, repositoryID string) (*extension.Repository, int) {
	user, status := resolveCurrentUser(ctx, admission)
	if status != http.StatusOK {
		return nil, status
	}
	id := mustParsePositiveID(repositoryID)
	if id == 0 {
		return nil, http.StatusBadRequest
	}
	repository, err := repo_model.GetRepositoryByID(ctx, id)
	if ctx.Err() != nil {
		return nil, http.StatusUnauthorized
	}
	if repo_model.IsErrRepoNotExist(err) {
		return nil, http.StatusNotFound
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repository, user)
	if ctx.Err() != nil {
		return nil, http.StatusUnauthorized
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if !permission.CanRead(unit.TypeCode) {
		return nil, http.StatusNotFound
	}
	if err := repository.LoadOwner(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, http.StatusUnauthorized
		}
		return nil, http.StatusServiceUnavailable
	}
	level := "read"
	if permission.IsAdmin() {
		level = "admin"
	} else if permission.CanWrite(unit.TypeCode) {
		level = "write"
	}
	return &extension.Repository{ID: strconv.FormatInt(repository.ID, 10), Owner: repository.Owner.Name, Name: repository.Name, Permission: level, DefaultBranch: repository.DefaultBranch}, http.StatusOK
}

func mustParsePositiveID(value string) int64 {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != value {
		return 0
	}
	return id
}

func resolveCurrentUser(ctx context.Context, admission *callbackAdmission) (*user_model.User, int) {
	if err := ctx.Err(); err != nil {
		return nil, http.StatusUnauthorized
	}
	store, err := admission.session.Read(admission.sessionID)
	if err != nil || store == nil {
		return nil, http.StatusUnauthorized
	}
	uid, ok := store.Get("uid").(int64)
	if !ok || uid != admission.actorID {
		return nil, http.StatusUnauthorized
	}
	user, err := user_model.GetUserByID(ctx, admission.actorID)
	if ctx.Err() != nil {
		return nil, http.StatusUnauthorized
	}
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	if !user.IsAccessAllowed(ctx) {
		return nil, http.StatusUnauthorized
	}
	if ctx.Err() != nil {
		return nil, http.StatusUnauthorized
	}
	return user, http.StatusOK
}

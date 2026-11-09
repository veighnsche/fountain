// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

import (
	"net/http"
	"strings"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/modules/log"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/services/context"
	"forgejo.org/services/extensions"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

// hasPublishBindingRequest reports whether the receive-pack request carries
// any conditional-publish operation binding. Either binding header present
// selects the conditional receive; discovery and upload-pack never consult
// this, so reads never consume an execution.
func hasPublishBindingRequest(ctx *context.Context) bool {
	return ctx.Req.Header.Get(PublishOperationHeader) != "" ||
		ctx.Req.Header.Get(sdk.PublishDesktopGrantHeader) != "" ||
		ctx.Req.Header.Get(sdk.AdmissionHeader) != ""
}

// servePublishReceive serves one bound conditional publish receive: it
// verifies the installation admission, kind, recorded actor and credential
// generation and route repository, atomically claims the waiting execution,
// launches exactly one bound receive-pack with a host-only execution
// environment, then reconciles the attributable result with its receipt and
// completion state. The caller admission never enters the child environment
// or diagnostics. Failures before the claim refuse without launching; the
// publisher discovers lost replies through GetOperation, never a
// replacement push.
func servePublishReceive(ctx *context.Context, h *serviceHandler) {
	var binding PublishBinding
	grant := ctx.Req.Header.Get(sdk.PublishDesktopGrantHeader)
	var oauthGrantID int64
	if grant != "" {
		identity, ok := ctx.Authentication.(interface{ OAuth2GrantID() int64 })
		if !ok || identity.OAuth2GrantID() <= 0 || ctx.Doer == nil || len(ctx.Req.Header.Values("Authorization")) != 1 || !strings.HasPrefix(ctx.Req.Header.Get("Authorization"), "Bearer ") ||
			len(ctx.Req.Header.Values(sdk.PublishDesktopGrantHeader)) != 1 || len(ctx.Req.Header.Values(PublishOperationHeader)) != 1 || len(ctx.Req.Header.Values("X-Factory-Desktop-Proof")) != 1 ||
			len(ctx.Req.Header.Values(sdk.AdmissionHeader)) != 0 || ctx.Req.URL.RawQuery != "" || ctx.Req.URL.ForceQuery || !validPublishOperationID(ctx.Req.Header.Get(PublishOperationHeader)) {
			ctx.PlainText(http.StatusForbidden, "desktop publication authorization is required")
			return
		}
		oauthGrantID = identity.OAuth2GrantID()
		binding.OperationID = ctx.Req.Header.Get(PublishOperationHeader)
	} else {
		var present bool
		var err error
		binding, present, err = ParsePublishBinding(ctx.Req.Header.Get(PublishOperationHeader), ctx.Req.Header.Get(sdk.AdmissionHeader))
		if err != nil || !present || len(ctx.Req.Header.Values("X-Factory-Desktop-Proof")) != 0 {
			ctx.PlainText(http.StatusBadRequest, "operation binding is malformed")
			return
		}
	}
	if h.isWiki {
		ctx.PlainText(http.StatusForbidden, "conditional publish is not served for wikis")
		return
	}
	if err := ValidatePublishService("receive-pack"); err != nil {
		ctx.PlainText(http.StatusBadRequest, "service cannot serve a conditional publish")
		return
	}
	if err := ValidatePublishGitProtocol(ctx.Req.Header.Get("Git-Protocol")); err != nil {
		ctx.PlainText(http.StatusBadRequest, "git protocol header is not allowed on a conditional publish")
		return
	}
	var prepared *nativeoperation_service.PreparedPublishReceive
	var outcome *nativeoperation_service.PublishReceiveOutcome
	var err error
	if grant != "" {
		prepared, outcome, err = nativeoperation_service.Default().PrepareDesktopPublishReceive(ctx, grant, binding.OperationID, ctx.Req.Header.Get("X-Factory-Desktop-Proof"), ctx.Doer.ID, h.repo.ID, oauthGrantID)
	} else {
		manager := extensions.GetManager()
		if manager == nil {
			ctx.PlainText(http.StatusServiceUnavailable, "conditional publish is unavailable")
			return
		}
		admission, ok := manager.VerifyBackgroundAdmission(binding.Admission)
		if !ok {
			ctx.PlainText(http.StatusUnauthorized, "background admission expired")
			return
		}
		// The network route cannot check the Unix peer of a runtime or service
		// admission; the presented native PAT below binds the actor instead.
		// Either admission form may attach to a still-waiting intent, so a
		// caller restart with fresh admission continues the same operation.
		if _, declared := admission.Capabilities[sdk.CapabilityBackgroundOperations]; !declared {
			ctx.PlainText(http.StatusForbidden, "background capability not declared")
			return
		}
		_, secret, ok := ctx.Req.BasicAuth()
		if !ok || secret == "" {
			ctx.PlainText(http.StatusUnauthorized, "native credential is required")
			return
		}
		prepared, outcome, err = nativeoperation_service.Default().PreparePublishReceive(ctx, nativeoperation_service.PublishReceiveRequest{
			InstallationID: admission.InstallationID,
			OperationID:    binding.OperationID,
			ActorID:        ctx.Doer.ID,
			TokenSecret:    secret,
			RepositoryID:   h.repo.ID,
		})
	}
	if err != nil {
		log.Error("Publish receive for operation %q in repository %d failed before its claim: %v", binding.OperationID, h.repo.ID, err)
		ctx.PlainText(http.StatusServiceUnavailable, "conditional publish is unavailable")
		return
	}
	if outcome != nil {
		writePublishOutcome(ctx, binding.OperationID, outcome)
		return
	}
	h.environ = execcontext.AppendExecEnv(h.environ, prepared.Execution)
	receiveErr := serviceRPC(ctx, h, "receive-pack")
	record, terminal, err := nativeoperation_service.Default().ReconcilePublish(ctx, prepared, receiveErr)
	if err != nil {
		// The git response is already written; the owner stays held for
		// offline recovery. Log identity only, never credentials.
		log.Error("Publish receive for operation %q in repository %d failed to reconcile: %v", binding.OperationID, h.repo.ID, err)
		return
	}
	if !terminal.Retained() {
		prepared.RetireCapability()
	}
	log.Trace("Publish receive for operation %q in repository %d reconciled effect %q completion %q",
		binding.OperationID, h.repo.ID, record.EffectState, record.CompletionState)
}

// writePublishOutcome reports a publish refusal that launched no receiver.
// Recorded and replayed outcomes name their effect and reason; unrecorded
// refusals name only the failed check. Bodies carry bounded codes only.
func writePublishOutcome(ctx *context.Context, operationID string, outcome *nativeoperation_service.PublishReceiveOutcome) {
	body := outcome.Code
	if outcome.Record != nil {
		body += " effect=" + outcome.Record.EffectState
		if outcome.Record.ReasonCode != "" {
			body += " reason=" + outcome.Record.ReasonCode
		}
	}
	log.Trace("Publish receive for operation %q refused: %s", operationID, body)
	ctx.PlainText(outcome.Status, body)
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package repo

// Conditional publish (git.ref.publish) receive protocol contract.
//
// The conditional receive is a new generic Fountain protocol on the smart-HTTP
// transport: a registered operation's publisher pushes exactly its authorized
// tuple through receive-pack, and the server binds that receive to the
// installation/actor/operation before Git starts. This file pins the
// transport-shape points of that contract that are decidable without the held
// operation seam:
//
//   - Only git-receive-pack serves a conditional publish. Upload-pack,
//     upload-archive and the dumb-HTTP endpoints never consume a registration;
//     discovery reads do not consume execution.
//   - The Git-Protocol header passes through under the existing native
//     allowlist, unchanged. Operation binding never travels in that header,
//     so no new header value is accepted here.
//   - Operation binding is never a push option. The conditional receive
//     rejects every push option, which also keeps policy push options from
//     smuggling intent past the registered tuple.
//
// Appliance operation binding travels in two request headers on the supported
// receive-pack route, alongside native PAT basic authentication:
//   - PublishOperationHeader carries the registered operation ID.
//   - The background admission header (sdk.AdmissionHeader) carries the
//     current installation runtime/service admission.
//
// Either header present selects the conditional receive; a half-present or
// malformed binding refuses without touching the reservation. Unmodified
// Forgejo implements no such protocol: it ignores both headers and serves
// an ordinary push. The admission value is a bearer secret: it must never
// appear in command arguments, child environments, logs or diagnostics.
//
// Desktop publication uses OAuth bearer authentication, the registered
// operation header, sdk.PublishDesktopGrantHeader and the original native
// profile proof. It never accepts appliance admission. Both paths share the
// same conditional receiver, reservation, hook checks and causal journal.

import (
	"errors"
	"fmt"
	"strings"
)

// PublishReceiveService is the only Git service that may serve a conditional
// publish receive.
const PublishReceiveService = "receive-pack"

// ErrPublishServiceRejected reports a Git service that cannot serve a
// conditional publish.
var ErrPublishServiceRejected = errors.New("service cannot serve a conditional publish")

// ValidatePublishService accepts only the receive-pack service for a
// conditional publish. Every other service name, including an empty one,
// rejects: a registration is consumed only by its matched receive.
func ValidatePublishService(service string) error {
	if service != PublishReceiveService {
		return fmt.Errorf("%w: %q", ErrPublishServiceRejected, service)
	}
	return nil
}

// ErrPublishProtocolRejected reports a Git-Protocol header the conditional
// receive cannot pass through.
var ErrPublishProtocolRejected = errors.New("git protocol header is not allowed on a conditional publish")

// ValidatePublishGitProtocol enforces the native header allowlist on the
// conditional receive. An empty header stays valid; a present header must
// satisfy the same safe shape the ordinary receive-pack path requires. The
// operation binding never travels here, so no additional value is accepted.
func ValidatePublishGitProtocol(header string) error {
	if header == "" {
		return nil
	}
	if !safeGitProtocolHeader.MatchString(header) {
		return fmt.Errorf("%w: %q", ErrPublishProtocolRejected, header)
	}
	return nil
}

// ErrPublishPushOptionsRejected reports push options on a conditional
// publish. The registered tuple is the complete intent; options cannot extend
// or select it.
var ErrPublishPushOptionsRejected = errors.New("push options are not allowed on a conditional publish")

// ValidatePublishPushOptions rejects every push option on a conditional
// publish receive, including an empty-valued map entry: presence alone
// rejects, regardless of content.
func ValidatePublishPushOptions(options map[string]string) error {
	if len(options) > 0 {
		return ErrPublishPushOptionsRejected
	}
	return nil
}

// PublishOperationHeader carries the registered operation ID of a
// conditional publish on the supported receive-pack route.
const PublishOperationHeader = "X-Forgejo-Operation"

// PublishBinding is one parsed conditional-publish operation binding: the
// registered operation ID plus the current installation admission. The
// admission value is a Bearer [REDACTED] and must never be logged.
type PublishBinding struct {
	OperationID string
	Admission   string
}

// ErrPublishBindingRejected reports a malformed or half-present operation
// binding on the conditional receive.
var ErrPublishBindingRejected = errors.New("operation binding is malformed")

// ParsePublishBinding parses the conditional-receive operation binding from
// its two header values. Empty values for both headers mean no binding is
// present and the receive stays ordinary. Any other shape must carry a
// well-formed operation ID plus a well-formed admission, else it rejects.
func ParsePublishBinding(operationValue, admissionValue string) (binding PublishBinding, present bool, err error) {
	operationValue = strings.TrimSpace(operationValue)
	admissionValue = strings.TrimSpace(admissionValue)
	if operationValue == "" && admissionValue == "" {
		return PublishBinding{}, false, nil
	}
	if !validPublishOperationID(operationValue) || !validPublishAdmission(admissionValue) {
		return PublishBinding{}, true, ErrPublishBindingRejected
	}
	return PublishBinding{OperationID: operationValue, Admission: admissionValue}, true, nil
}

func validPublishOperationID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}

func validPublishAdmission(token string) bool {
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

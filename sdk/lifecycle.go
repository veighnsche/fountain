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
	"strconv"
	"strings"

	"forgejo.org/extension-sdk/wirejson"
)

// Generic extension lifecycle wire (RCX-2 F01 frozen contract). The manager
// calls the required lifecycle controller over its private transport; the
// controller verifies each call through the manager's authorize callback
// before running any bounded effect, and reports observed facts back.
// Fountain alone persists requested enablement; this wire carries no
// desired-state record, no browser Actor, and no product-specific behavior.
//
// Operations are exactly observe, activate (start) and deactivate (stop).
const (
	LifecycleObserve    = "observe"
	LifecycleActivate   = "activate"
	LifecycleDeactivate = "deactivate"
)

// Lifecycle effect states reported by the controller: factual presence of
// the controlled external effects, not the requested UI state.
const (
	LifecycleStateRunning = "running"
	LifecycleStateStopped = "stopped"
	LifecycleStateUnknown = "unknown"
)

// Bounded lifecycle error codes. Only these codes may appear on the wire.
const (
	LifecycleErrUnavailable         = "unavailable"
	LifecycleErrUnsupported         = "unsupported"
	LifecycleErrIdentityChanged     = "identity_changed"
	LifecycleErrOperationInProgress = "operation_in_progress"
	LifecycleErrUnconfirmed         = "unconfirmed"
	LifecycleErrEffectFailed        = "effect_failed"
)

// Lifecycle wire size bounds: manager and controller enforce 16 KiB on
// lifecycle request and response bodies.
const (
	MaximumLifecycleBodyBytes = 16 << 10
	// MaximumLifecycleDetailBytes bounds the free-text detail field.
	MaximumLifecycleDetailBytes = 256
	// MaximumLifecycleInstanceIDBytes bounds the opaque controller
	// instance identifier.
	MaximumLifecycleInstanceIDBytes = 128
)

// LifecycleCall pins one lifecycle operation to its full identity tuple:
// the required controller and its current instance, the catalog-selected
// target artifact pin, the installation UUID, the desired generation, and
// a UTC Unix-second deadline bounding the call. It pins target identity,
// never a browser Actor.
type LifecycleCall struct {
	ControllerID         string `json:"controller_id"`
	ControllerInstanceID string `json:"controller_instance_id"`
	PackageID            string `json:"package_id"`
	PackageVersion       string `json:"package_version"`
	PackageSHA256        string `json:"package_sha256"`
	InstallationID       string `json:"installation_id"`
	Generation           string `json:"generation"`
	Operation            string `json:"operation"`
	DeadlineUnix         int64  `json:"deadline_unix"`
}

// LifecycleObservation echoes the full call identity tuple, operation and
// deadline, plus the observed facts: state, the actual recorded effect
// generation (or null), whether the host holds a current operational grant
// for this tuple (admission_ready), short detail text, and an optional
// bounded error.
type LifecycleObservation struct {
	LifecycleCall
	State            string          `json:"state"`
	EffectGeneration *string         `json:"effect_generation"`
	AdmissionReady   bool            `json:"admission_ready"`
	Detail           string          `json:"detail"`
	Error            *LifecycleError `json:"error,omitempty"`
}

// LifecycleError is a bounded machine-readable failure with short human
// text. Detail text never carries secrets.
type LifecycleError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LifecycleHandler implements one lifecycle operation for the required
// controller extension. It reports observed facts, never merely "command
// accepted": activate success needs a fresh observation of running with a
// current grant, deactivate success needs stopped without one.
type LifecycleHandler func(ctx context.Context, authority LifecycleAuthority, call LifecycleCall) (LifecycleObservation, error)

// LifecycleVerification is the manager authorize callback's answer: the
// exact verified call plus the current desired-enabled flag for its target.
// A cleanup authorization issued inside a still-active failed-enable
// command echoes the deactivate call with desired_enabled true; that echo
// authorizes only that deactivate tuple, never an activate.
type LifecycleVerification struct {
	Call           LifecycleCall `json:"call"`
	DesiredEnabled bool          `json:"desired_enabled"`
}

// LifecycleAuthority is the opaque lifecycle authority admitted by the
// manager for exactly one call tuple and deadline. Its token, callback
// socket and peer pin are unexported: encoding an authority (for example
// for browser code) never serializes the secret, and authority exists only
// because the manager has already admitted an admin request or is
// reconciling persisted authorized intent. It is distinct from an Actor
// Authority: lifecycle admissions and browser/ordinary admissions are never
// interchangeable, and LifecycleContext never consults RequestContext,
// CurrentActor or a fabricated site administrator.
//
// Construct it with LifecycleContext on the SDK-owned endpoint; verify the
// call it carries with Verify, which invokes the manager's authorize
// callback over the existing callback socket selection with server-peer
// pinning.
type LifecycleAuthority struct {
	admission         string
	callbackSocket    string
	serviceCallback   bool
	servicePeerUID    uint32
	servicePeerPinned bool
}

// requiredLifecycleCallFields must all be present in every lifecycle call
// object. "deadline_unix" is presence-checked so an omitted deadline is
// never mistaken for a zero value.
var requiredLifecycleCallFields = []string{
	"controller_id", "controller_instance_id", "package_id",
	"package_version", "package_sha256", "installation_id",
	"generation", "operation", "deadline_unix",
}

func validLifecycleOperation(operation string) bool {
	return operation == LifecycleObserve || operation == LifecycleActivate || operation == LifecycleDeactivate
}

func validLifecycleState(state string) bool {
	switch state {
	case LifecycleStateRunning, LifecycleStateStopped, LifecycleStateUnknown:
		return true
	}
	return false
}

func validLifecycleCode(code string) bool {
	switch code {
	case LifecycleErrUnavailable, LifecycleErrUnsupported, LifecycleErrIdentityChanged,
		LifecycleErrOperationInProgress, LifecycleErrUnconfirmed, LifecycleErrEffectFailed:
		return true
	}
	return false
}

// validGeneration reports whether a generation is a canonical unsigned-64
// decimal string: digits only, no leading zeros unless the value is "0",
// within uint64 range. Persisted generations start at 1; an absent intent
// is virtual generation "0".
func validGeneration(value string) bool {
	if value == "" || len(value) > 20 {
		return false
	}
	if len(value) > 1 && value[0] == '0' {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

// validInstallationID reports whether a value is a UUID in the canonical
// 8-4-4-4-12 hexadecimal shape.
func validInstallationID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if c < '0' || c > '9' && c < 'A' || c > 'F' && c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

// ValidateLifecycleCall checks the structural call rules: controller and
// target identity, the catalog artifact pin, installation UUID, canonical
// generation, known operation, and a deadline that has not passed relative
// to nowUnix (an injected UTC Unix-second clock). It does not authorize:
// the manager's authorize callback owns admission, deadlines per operation
// stay with the issuer, and cross-catalog checks (required set, controller
// readiness, intent agreement) need manager state.
func ValidateLifecycleCall(call LifecycleCall, nowUnix int64) error {
	if !ValidID(call.ControllerID) || len(call.ControllerID) > maximumPackageIDBytes {
		return errors.New("invalid lifecycle controller id")
	}
	if call.ControllerInstanceID == "" || len(call.ControllerInstanceID) > MaximumLifecycleInstanceIDBytes {
		return errors.New("invalid lifecycle controller instance id")
	}
	if strings.TrimSpace(call.ControllerInstanceID) != call.ControllerInstanceID {
		return errors.New("invalid lifecycle controller instance id")
	}
	if !ValidID(call.PackageID) || len(call.PackageID) > maximumPackageIDBytes {
		return errors.New("invalid lifecycle package id")
	}
	if !validPackageVersion(call.PackageVersion) {
		return errors.New("invalid lifecycle package version")
	}
	if !validSHA256Hex(call.PackageSHA256) {
		return errors.New("invalid lifecycle package digest")
	}
	if !validInstallationID(call.InstallationID) {
		return errors.New("invalid lifecycle installation id")
	}
	if !validGeneration(call.Generation) {
		return errors.New("invalid lifecycle generation")
	}
	if !validLifecycleOperation(call.Operation) {
		return errors.New("invalid lifecycle operation")
	}
	if call.DeadlineUnix < 1 || call.DeadlineUnix <= nowUnix {
		return errors.New("lifecycle call deadline has passed")
	}
	return nil
}

// Echoes reports whether an observation echoes a call's full identity
// tuple, operation and deadline. Any mismatch is unknown, never adopted.
func (o LifecycleObservation) Echoes(call LifecycleCall) bool {
	return o.LifecycleCall == call
}

// ValidateLifecycleObservation checks one observation against the call it
// answers, with nowUnix as the injected UTC Unix-second clock:
//   - the echoed tuple, operation and deadline must equal the call exactly;
//   - state is running, stopped or unknown;
//   - effect_generation is present for running and null otherwise; a
//     running resource reports its actual recorded generation, which may be
//     lower than the request generation and must never be required equal;
//   - admission_ready true implies running and no reported error: stopped
//     or unknown state, or any reported error, never carries a grant;
//   - detail is at most 256 bytes with no secrets;
//   - a present error uses a bounded code and nonempty message.
//
// Running with admission_ready false is valid: after a restart the host
// reports factual presence without a current grant until an idempotent
// activate refreshes it. That state is not ready and never opens ordinary
// admission.
func ValidateLifecycleObservation(observation LifecycleObservation, call LifecycleCall, nowUnix int64) error {
	if err := ValidateLifecycleCall(call, nowUnix); err != nil {
		return err
	}
	if !observation.Echoes(call) {
		return errors.New("lifecycle observation does not echo its call")
	}
	if !validLifecycleState(observation.State) {
		return errors.New("invalid lifecycle observation state")
	}
	if observation.State == LifecycleStateRunning {
		if observation.EffectGeneration == nil || !validGeneration(*observation.EffectGeneration) {
			return errors.New("running lifecycle observation requires a recorded effect generation")
		}
	} else if observation.EffectGeneration != nil {
		return errors.New("non-running lifecycle observation must not report an effect generation")
	}
	if observation.AdmissionReady && observation.State != LifecycleStateRunning {
		return errors.New("lifecycle admission requires a running observation")
	}
	if len(observation.Detail) > MaximumLifecycleDetailBytes {
		return errors.New("lifecycle observation detail exceeds limit")
	}
	if observation.Error != nil {
		if !validLifecycleCode(observation.Error.Code) || strings.TrimSpace(observation.Error.Message) == "" {
			return fmt.Errorf("invalid lifecycle error %q", observation.Error.Code)
		}
		if observation.AdmissionReady {
			return errors.New("lifecycle observation with an error cannot report admission")
		}
	}
	return nil
}

// ValidateLifecycleRegistration matches a controller extension's handshake
// registration to its installed manifest capability: a declared lifecycle
// capability requires the handler registration, and a handler registration
// requires the declared capability. A controller that is missing or unready
// is a failure, never an implicit no-op hook.
func ValidateLifecycleRegistration(manifest Manifest, registration Registration) error {
	declares := false
	for _, capability := range manifest.Capabilities {
		if capability == CapabilityExtensionLifecycle {
			declares = true
			break
		}
	}
	if declares && !registration.Lifecycle {
		return fmt.Errorf("extension %q declares the lifecycle capability but registers no lifecycle handler", manifest.ID)
	}
	if registration.Lifecycle && !declares {
		return fmt.Errorf("extension %q registers a lifecycle handler without declaring the lifecycle capability", manifest.ID)
	}
	return nil
}

// DecodeLifecycleCall strictly decodes one call: unknown fields, duplicate
// keys, trailing data and wrong scalar types are rejected, and every
// required field must be present.
func DecodeLifecycleCall(data []byte) (LifecycleCall, error) {
	call, err := decodeLifecycleValue[LifecycleCall](
		data, "lifecycle call", requiredLifecycleCallFields,
	)
	if err != nil {
		return LifecycleCall{}, err
	}
	return call, nil
}

// DecodeLifecycleObservation strictly decodes one observation: unknown
// fields, duplicate keys, trailing data and wrong scalar types are
// rejected; the full call tuple, state and detail must be present; and
// admission_ready must be explicitly present (an omitted boolean is never
// read as false).
func DecodeLifecycleObservation(data []byte) (LifecycleObservation, error) {
	observation, err := decodeLifecycleValue[LifecycleObservation](
		data, "lifecycle observation", []string{
			"controller_id", "controller_instance_id", "package_id",
			"package_version", "package_sha256", "installation_id",
			"generation", "operation", "deadline_unix",
			"state", "effect_generation", "admission_ready", "detail",
		},
	)
	if err != nil {
		return LifecycleObservation{}, err
	}
	return observation, nil
}

// DecodeLifecycleVerification strictly decodes one authorize-callback
// answer: unknown fields, duplicate keys, trailing data and wrong scalar
// types are rejected, and both the exact verified call and the desired
// flag must be present (an omitted desired_enabled is never read as
// false).
func DecodeLifecycleVerification(data []byte) (LifecycleVerification, error) {
	verification, err := decodeLifecycleValue[LifecycleVerification](
		data, "lifecycle verification", []string{"call", "desired_enabled"},
	)
	if err != nil {
		return LifecycleVerification{}, err
	}
	return verification, nil
}

// decodeLifecycleValue strictly decodes one lifecycle JSON object: the
// body is bounded at 16 KiB, duplicate keys anywhere in the document are
// rejected, unknown fields and trailing data fail, and every required
// top-level field must be present. Wrong scalar types fail in the typed
// decode; LifecycleObservation's embedded call fields decode through the
// same strict field set.
func decodeLifecycleValue[T any](data []byte, what string, required []string) (T, error) {
	var zero T
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || len(trimmed) > MaximumLifecycleBodyBytes {
		return zero, fmt.Errorf("%s exceeds size limit", what)
	}
	if trimmed[0] != '{' {
		return zero, fmt.Errorf("%s must be an object", what)
	}
	fields, err := wirejson.ObjectFields(trimmed, what)
	if err != nil {
		return zero, err
	}
	if err := wirejson.RequireFields(fields, what, required, []string{"admission_ready", "desired_enabled"}); err != nil {
		return zero, err
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("decode %s: %w", what, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return zero, fmt.Errorf("%s must contain one object", what)
	}
	return value, nil
}

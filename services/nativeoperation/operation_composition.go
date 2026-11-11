// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"

	sdk "forgejo.org/extension-sdk"
	extensionauth_model "forgejo.org/models/extensionauth"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
)

type operationExecutionMode uint8

const (
	executeOnSubmit operationExecutionMode = iota
	executeOnReceive
)

type operationImplementation struct {
	kind    string
	family  string
	mode    operationExecutionMode
	parse   func(*ValidIntent, []byte) error
	submit  func(*Service, context.Context, extensionauth_model.SubmissionDecision, string, *ValidIntent) (sdk.OperationRecord, error)
	execute func(*Service, context.Context, *nativeoperation_model.Operation, *ValidIntent, Scope, *execcontext.Execution) (sdk.OperationRecord, terminalResult, error)
	recover func(*Service, context.Context, *RecoveryAssessment, *nativeoperation_model.Reservation, Scope, *nativeoperation_model.Operation) (RecoveryAssessment, error)
}

// Each call returns the closed host composition. There is no registration path;
// stable journal names remain owned by the lower-level operation contract.
func operationImplementations() []operationImplementation {
	return []operationImplementation{
		{
			kind: nativeoperation_model.KindRefPublish, family: "conditional-publish", mode: executeOnReceive,
			parse: func(intent *ValidIntent, payload []byte) error {
				var err error
				intent.Publish, err = ParsePublishPayload(payload)
				if err != nil {
					return ErrInvalidIntent
				}
				return nil
			},
			submit: (*Service).submitPublish, recover: (*Service).recoverConditionalPublish,
		},
		{
			kind: nativeoperation_model.KindLifecycleNote, family: "conditional-lifecycle-note",
			parse: func(intent *ValidIntent, payload []byte) (err error) {
				intent.LifecycleNote, err = parseLifecycleNotePayload(payload, intent.RepositoryID)
				return err
			},
			submit: (*Service).submitLifecycleNote, execute: (*Service).executeLifecycleNote, recover: (*Service).recoverConditionalLifecycleNote,
		},
		{
			kind: nativeoperation_model.KindIssueGraph, family: "conditional-issue-graph",
			parse: func(intent *ValidIntent, payload []byte) (err error) {
				intent.IssueGraph, err = parseIssueGraphPayload(payload)
				return err
			},
			submit: (*Service).submitIssueGraph, execute: (*Service).executeIssueGraph, recover: (*Service).recoverConditionalIssueGraph,
		},
		{
			kind: nativeoperation_model.KindLifecycleIssue, family: "conditional-lifecycle-issue",
			parse: func(intent *ValidIntent, payload []byte) (err error) {
				intent.LifecycleIssue, err = parseLifecycleIssuePayload(payload, intent.RepositoryID)
				return err
			},
			submit: (*Service).submitLifecycleIssue, execute: (*Service).executeLifecycleIssue, recover: (*Service).recoverConditionalLifecycleIssue,
		},
		{
			kind: nativeoperation_model.KindPRCreate, family: "conditional-prcreate",
			parse: func(intent *ValidIntent, payload []byte) (err error) {
				intent.PRCreate, err = parsePRCreatePayload(payload, intent.RepositoryID)
				return err
			},
			submit: (*Service).submitPRCreate, execute: (*Service).executePRCreate, recover: (*Service).recoverConditionalPRCreate,
		},
		{
			kind: nativeoperation_model.KindMerge, family: "conditional-merge",
			parse: func(intent *ValidIntent, payload []byte) (err error) {
				intent.Merge, err = parseMergeIntent(payload, intent.RepositoryID)
				return err
			},
			submit: (*Service).submitMerge, execute: (*Service).executeMerge, recover: (*Service).recoverConditionalMerge,
		},
		{
			kind: nativeoperation_model.KindLifecycleReview, family: "conditional-lifecycle-review",
			parse: func(intent *ValidIntent, payload []byte) (err error) {
				intent.LifecycleReview, err = parseLifecycleReviewPayload(payload, intent.RepositoryID)
				return err
			},
			submit: (*Service).submitLifecycleReview, execute: (*Service).executeLifecycleReview, recover: (*Service).recoverConditionalLifecycleReview,
		},
		{
			kind: nativeoperation_model.KindReviewSubmit, family: "conditional-review",
			parse: func(intent *ValidIntent, payload []byte) (err error) {
				intent.ReviewSubmit, err = parseReviewSubmitPayload(payload, intent.RepositoryID)
				return err
			},
			submit: (*Service).submitReviewSubmit, execute: (*Service).executeReviewSubmit, recover: (*Service).recoverConditionalReviewSubmit,
		},
		{
			kind: nativeoperation_model.KindThreadResolve, family: "conditional-thread-resolve",
			parse: func(intent *ValidIntent, payload []byte) (err error) {
				intent.ThreadResolve, err = parseThreadResolvePayload(payload, intent.RepositoryID)
				return err
			},
			submit: (*Service).submitThreadResolve, execute: (*Service).executeThreadResolve, recover: (*Service).recoverConditionalThreadResolve,
		},
	}
}

func implementationFor(kind string) (operationImplementation, bool) {
	for _, implementation := range operationImplementations() {
		if implementation.kind == kind {
			return implementation, true
		}
	}
	return operationImplementation{}, false
}

func validateOperationImplementations(implementations []operationImplementation) error {
	seen := make(map[string]bool, len(implementations))
	for _, implementation := range implementations {
		if seen[implementation.kind] || !nativeoperation_model.ValidKind(implementation.kind) || implementation.family == "" || implementation.parse == nil || implementation.submit == nil || implementation.recover == nil {
			return ErrKindUnavailable
		}
		switch implementation.mode {
		case executeOnSubmit:
			if implementation.kind == nativeoperation_model.KindRefPublish || implementation.execute == nil {
				return ErrKindUnavailable
			}
		case executeOnReceive:
			if implementation.kind != nativeoperation_model.KindRefPublish || implementation.execute != nil {
				return ErrKindUnavailable
			}
		default:
			return ErrKindUnavailable
		}
		seen[implementation.kind] = true
	}
	for _, kind := range nativeoperation_model.SupportedKinds() {
		if !seen[kind] {
			return ErrKindUnavailable
		}
	}
	return nil
}

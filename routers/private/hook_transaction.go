// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"net/http"

	"forgejo.org/modules/log"
	"forgejo.org/modules/private"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

// HookReferenceTransaction enforces the native-operation reservation at
// Git's reference-transaction checkpoint. The channel's internal token
// authenticates the hook transport; the execution proof additionally
// identifies the permitted owner. Refusals fail the ref update; errors fail
// closed without exposing secrets.
func HookReferenceTransaction(ctx *context.PrivateContext) {
	opts := web.GetForm(ctx).(*private.TransactionOptions)
	ownerName := ctx.Params(":owner")
	repoName := ctx.Params(":repo")
	if len(opts.OldCommitIDs) != len(opts.NewCommitIDs) || len(opts.OldCommitIDs) != len(opts.RefFullNames) {
		ctx.JSON(http.StatusBadRequest, private.Response{Err: "mismatched transaction inputs"})
		return
	}
	lines := make([]nativeoperation_service.RefLine, 0, len(opts.OldCommitIDs))
	scoped := make([]nativeoperation_service.ScopedRef, 0, len(opts.OldCommitIDs))
	for i := range opts.OldCommitIDs {
		lines = append(lines, nativeoperation_service.RefLine{Old: opts.OldCommitIDs[i], New: opts.NewCommitIDs[i], Ref: string(opts.RefFullNames[i])})
		scoped = append(scoped, nativeoperation_service.ScopedRef{Ref: string(opts.RefFullNames[i]), OldOID: opts.OldCommitIDs[i], NewOID: opts.NewCommitIDs[i]})
	}
	// Record the proposed tuples before classifying: a receive owner
	// declares its own effects, which the gate then admits as listed
	// scope members. Unproven or non-receive refinements are silent
	// no-ops, so other families keep their strict checking. A
	// pull-creation owner likewise declares its derived PR ref, whose
	// name is unknowable at claim time.
	_, refineErr := nativeoperation_service.RefineReceiveScopeChecked(ctx, opts.ExecProof, scoped)
	if refineErr == nil {
		_, refineErr = nativeoperation_service.RefineCollabPullScopeChecked(ctx, opts.ExecProof, scoped)
	}
	if refineErr != nil {
		log.Error("Reference transaction scope refinement failed for %s/%s: %v", ownerName, repoName, refineErr)
		ctx.JSON(http.StatusInternalServerError, private.Response{Err: "transaction check unavailable"})
		return
	}
	decision, err := nativeoperation_service.Default().ClassifyTransaction(ctx, nativeoperation_service.TransactionRequest{
		OwnerName: ownerName,
		RepoName:  repoName,
		Phase:     opts.Phase,
		Origin:    opts.Origin,
		Lines:     lines,
		Proof:     opts.ExecProof,
	})
	if err != nil {
		log.Error("Reference transaction check failed for %s/%s: %v", ownerName, repoName, err)
		ctx.JSON(http.StatusInternalServerError, private.Response{Err: "transaction check unavailable"})
		return
	}
	if !decision.Allowed {
		ctx.JSON(http.StatusForbidden, private.Response{UserMsg: decision.Reason})
		return
	}
	ctx.PlainText(http.StatusOK, "ok")
}

// checkCompletionBinding binds synchronous post-receive bookkeeping to the
// held reservation owner. It returns false after writing the refusal. Idle
// pushes skip the binding without loading anything new.
func checkCompletionBinding(ctx *context.PrivateContext, opts *private.HookOptions, ownerName, repoName string) bool {
	if len(opts.RefFullNames) == 0 {
		return true
	}
	idle, err := nativeoperation_service.Default().GateState(ctx)
	if err != nil {
		log.Error("Completion binding check failed for %s/%s: %v", ownerName, repoName, err)
		ctx.JSON(http.StatusInternalServerError, private.HookPostReceiveResult{
			Err: "completion binding unavailable",
		})
		return false
	}
	if idle {
		return true
	}
	repo := loadRepository(ctx, ownerName, repoName)
	if ctx.Written() {
		return false
	}
	refNames := make([]string, 0, len(opts.RefFullNames))
	scoped := make([]nativeoperation_service.ScopedRef, 0, len(opts.RefFullNames))
	for i, ref := range opts.RefFullNames {
		refNames = append(refNames, string(ref))
		tuple := nativeoperation_service.ScopedRef{Ref: string(ref)}
		if i < len(opts.OldCommitIDs) {
			tuple.OldOID = opts.OldCommitIDs[i]
		}
		if i < len(opts.NewCommitIDs) {
			tuple.NewOID = opts.NewCommitIDs[i]
		}
		scoped = append(scoped, tuple)
	}
	// Record the observed ref tuples before classifying: a receive owner
	// declares its own effects, which the gate then admits as listed
	// scope members. Idle pushes skip above; unproven or non-receive
	// refinements are silent no-ops.
	if _, err := nativeoperation_service.RefineReceiveScopeChecked(ctx, opts.ExecProof, scoped); err != nil {
		log.Error("Completion scope refinement failed for %s/%s: %v", ownerName, repoName, err)
		ctx.JSON(http.StatusInternalServerError, private.HookPostReceiveResult{Err: "completion binding unavailable"})
		return false
	}
	decision, err := nativeoperation_service.Default().ClassifyCompletion(ctx, repo.ID, refNames, opts.ExecProof, !opts.GetGitPushOptions().Empty())
	if err != nil {
		log.Error("Completion binding check failed for %s/%s: %v", ownerName, repoName, err)
		ctx.JSON(http.StatusInternalServerError, private.HookPostReceiveResult{
			Err: "completion binding unavailable",
		})
		return false
	}
	if !decision.Allowed {
		ctx.JSON(http.StatusForbidden, private.HookPostReceiveResult{
			Err: decision.Reason,
		})
		return false
	}
	return true
}

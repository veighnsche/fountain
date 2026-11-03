// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"unicode/utf8"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	"forgejo.org/models/organization"
	user_model "forgejo.org/models/user"
)

// Each history row is freshly loaded, without hydrated associations, both at
// atomic commit and recovery. The digest includes complete stored event facts
// and timestamps. No native model object or prose crosses the SDK receipt seam.
func humanReviewRequestHistoryDigest(ctx context.Context, r sdk.HumanReviewRequestReceipt, requested bool) (string, error) {
	if len(r.HistoryIDs) == 0 {
		return sdk.HumanReviewBodyDigest("[]"), nil
	}
	ids := make([]int64, 0, len(r.HistoryIDs))
	for _, id := range r.HistoryIDs {
		ids = append(ids, nativeCommentID(id))
	}
	var comments []*issues_model.Comment
	if err := db.GetEngine(ctx).In("id", ids).Asc("id").Find(&comments); err != nil {
		return "", err
	}
	if len(comments) != len(ids) {
		return "", errors.New("recorded reviewer history is missing")
	}
	userID, teamID := int64(0), int64(0)
	if r.ReviewerKind == "user" {
		userID = r.ReviewerID
	} else {
		teamID = r.ReviewerID
	}
	for i, c := range comments {
		if c.ID != ids[i] || c.Type != issues_model.CommentTypeReviewRequest || c.IssueID != r.IssueID || c.PosterID != r.ActorID || c.AssigneeID != userID || c.AssigneeTeamID != teamID || c.RemovedAssignee == requested || c.OriginalAuthorID != 0 || c.OriginalAuthor != "" || c.Content != "" || c.OriginalAnchor != nil {
			return "", errors.New("recorded reviewer history differs")
		}
		if requested && (len(r.After.RequestIDs) != 1 || c.ReviewID != nativeCommentID(r.After.RequestIDs[0])) || !requested && c.ReviewID != 0 {
			return "", errors.New("recorded reviewer history target differs")
		}
		for _, value := range []string{c.OriginalAuthor, c.OldTitle, c.NewTitle, c.OldRef, c.NewRef, c.TreePath, c.Content, c.PatchQuoted, c.CommitSHA} {
			if !utf8.ValidString(value) {
				return "", errors.New("invalid persisted reviewer history text")
			}
		}
	}
	raw, err := json.Marshal(comments)
	if err != nil {
		return "", err
	}
	if len(raw) > sdk.MaxReviewRequestSnapshotBodyBytes {
		return "", ErrHumanReviewRequestTooLarge
	}
	return sdk.HumanReviewBodyDigest(string(raw)), nil
}

// Offline recovery attributes exact SQL evidence under the stopped writer's
// intact fence. It never reapplies desired state or repeats notifications.
func (s *Service) recoverHumanReviewRequest(ctx context.Context, a *RecoveryAssessment, r *nativeoperation_model.Reservation, scope Scope, op *nativeoperation_model.Operation) (RecoveryAssessment, error) {
	var saved struct {
		Payload sdk.HumanReviewRequestChange `json:"payload"`
	}
	if json.Unmarshal([]byte(op.Intent), &saved) != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human reviewer request intent is missing")
	}
	q := saved.Payload
	intent, err := humanReviewRequestIntent(q)
	expectedScope := Scope{Kind: nativeoperation_model.OwnerConditional, RepositoryID: op.RepositoryID, IssueID: nativeCommentID(q.Expected.Review.IssueID)}
	if err != nil || intent.Canonical != op.Intent || intent.Digest != op.IntentDigest || intent.OperationID != op.OperationID || intent.Kind != op.Kind || intent.ActorID != op.ActorID || intent.RepositoryID != op.RepositoryID || intent.AuthRevision != op.AuthRevision || intent.ExpectedNativeRevision != op.ExpectedNativeRevision || intent.NotAfter != op.NotAfter || !reflect.DeepEqual(scope, expectedScope) || r.Revision < 1 || r.Revision-1 != op.ExpectedNativeRevision || op.TokenID != 0 || op.CredentialFingerprint != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human reviewer request scope and intent differ")
	}
	if !op.Admitted && op.EffectState == nativeoperation_model.EffectPending && op.Receipt == "" && op.Completion == "" {
		if _, err := nativeoperation_model.SetTerminal(ctx, op.InstallationID, op.OperationID, nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonRecoveredNoEffect, Cancellation: nativeoperation_model.CancellationNone}, ""); err != nil {
			return RecoveryAssessment{}, err
		}
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectNotCommitted)
	}
	if !op.Admitted || op.EffectState != nativeoperation_model.EffectCommitted {
		return fenced(a, ReasonRecoveryUncertainEffect, "human reviewer request has no atomic committed receipt")
	}
	var receipt sdk.HumanReviewRequestReceipt
	if json.Unmarshal([]byte(op.Receipt), &receipt) != nil || !sdk.ValidHumanReviewRequestReceipt(receipt, q) {
		return fenced(a, ReasonRecoveryUncertainEffect, "human reviewer request receipt differs from intent")
	}
	raw, err := json.Marshal(receipt)
	if err != nil || string(raw) != op.Receipt {
		return fenced(a, ReasonRecoveryUncertainEffect, "human reviewer request exact receipt encoding differs")
	}
	issue, err := issues_model.GetIssueByID(ctx, receipt.IssueID)
	if err != nil || issue.RepoID != receipt.RepositoryID || issue.Index != receipt.Index || !issue.IsPull {
		return fenced(a, ReasonRecoveryMissingEvidence, "human reviewer request parent differs")
	}
	if err := issue.LoadPullRequest(ctx); err != nil || issue.PullRequest == nil || issue.PullRequest.ID != receipt.PullID || issue.PullRequest.BaseRepoID != receipt.RepositoryID {
		return fenced(a, ReasonRecoveryMissingEvidence, "human reviewer request pull identity differs")
	}
	if _, err := user_model.GetUserByID(ctx, receipt.ActorID); err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human reviewer request original actor is missing")
	}
	if receipt.ReviewerKind == "user" {
		_, err = user_model.GetUserByID(ctx, receipt.ReviewerID)
	} else {
		_, err = organization.GetTeamByID(ctx, receipt.ReviewerID)
	}
	if err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human reviewer request subject is missing")
	}
	// Offline attribution retains original principals; current review permission
	// and Git movement do not authorize any new work during recovery.
	evidence, err := ReadHumanReviewRequestEvidence(ctx, receipt.IssueID, receipt.ReviewerKind, receipt.ReviewerID)
	if err != nil || !reflect.DeepEqual(evidence, receipt.After) {
		return fenced(a, ReasonRecoveryMissingEvidence, "human reviewer request selected post-state differs")
	}
	rows, err := humanReviewRequestHistory(ctx, receipt.IssueID, receipt.ReviewerKind, receipt.ReviewerID)
	if err != nil {
		return fenced(a, ReasonRecoveryMissingEvidence, "human reviewer request review history is missing")
	}
	digest, err := humanReviewRequestDigest(rows)
	if err != nil || digest != receipt.ReviewsAfterDigest {
		return fenced(a, ReasonRecoveryMissingEvidence, "human reviewer request complete review history differs")
	}
	digest, err = humanReviewRequestHistoryDigest(ctx, receipt, q.Requested)
	if err != nil || digest != receipt.HistoryDigest {
		return fenced(a, ReasonRecoveryMissingEvidence, "human reviewer request native change history differs")
	}
	if op.Completion == nativeoperation_model.CompletionComplete || op.Completion == nativeoperation_model.CompletionNeedsIntervention {
		return s.releaseRecovered(ctx, a, r, nativeoperation_model.EffectCommitted)
	}
	if op.Completion != nativeoperation_model.CompletionPending && op.Completion != "" {
		return fenced(a, ReasonRecoveryUncertainEffect, "human reviewer request completion differs")
	}
	completion := nativeoperation_model.CompletionComplete
	if humanReviewRequestNeedsCompletion(receipt, q.Requested) {
		completion = nativeoperation_model.CompletionNeedsIntervention
	}
	if _, err := nativeoperation_model.SetCompletionAndRelease(ctx, op.InstallationID, op.OperationID, r.Owner, r.Generation, completion); err != nil {
		return RecoveryAssessment{}, err
	}
	a.Verdict, a.Effect = RecoveryReleased, nativeoperation_model.EffectCommitted
	a.Checks = append(a.Checks, "atomic reviewer receipt and complete request/review/history post-state match; no primary or notification work replayed")
	s.retireStaleCapabilities()
	return *a, nil
}

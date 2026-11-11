// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"fmt"

	issues_model "forgejo.org/models/issues"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	org_model "forgejo.org/models/organization"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
)

// EffectCollaborationConsistent releases an interrupted collaboration
// writer whose anchor entities are intact. Single-transaction collaboration
// changes apply atomically, so the observed state is always a valid end
// state; the exclusive held reservation proves no other writer
// interleaved. Multi-statement changes, creates with unknowable IDs and
// multi-entity batches cannot establish their effect this way and stay
// fenced for composed recovery.
const EffectCollaborationConsistent = "consistent"

// recoverCollaboration reconciles one held ordinary collaboration writer
// from its resource label's anchor entities. The label's operation class
// decides: single-transaction updates release when their issue, comment,
// pull request, review, label, milestone or history anchors are intact,
// with present-or-absent rows accepted for the audited single-transaction
// deletes; multi-statement changes, creates and batches stay fenced with
// stable reasons. A missing anchor means unaccounted interference and
// fences: a collaboration change never deletes its own context.
func (s *Service) recoverCollaboration(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	resource, err := execcontext.OrdinaryResource(reservation.Owner, FamilyCollaboration)
	if err != nil {
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("collaboration owner names no parseable resource: %v", err))
	}
	claim, err := execcontext.ParseCollabResource(resource)
	if err != nil {
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("collaboration resource is not attributable: %v", err))
	}
	if scope.RepositoryID > 0 {
		if _, err := repo_model.GetRepositoryByID(ctx, scope.RepositoryID); err != nil {
			if repo_model.IsErrRepoNotExist(err) {
				return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("repository %d for the held collaboration scope no longer exists", scope.RepositoryID))
			}
			return RecoveryAssessment{}, err
		}
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("repository %d exists", scope.RepositoryID))
	}
	switch claim.Kind {
	case "issue":
		return s.recoverCollabIssue(ctx, assessment, reservation, claim)
	case "comment":
		return s.recoverCollabComment(ctx, assessment, reservation, claim)
	case "dependency":
		return s.recoverCollabDependency(ctx, assessment, reservation, claim)
	case "pull":
		return s.recoverCollabPull(ctx, assessment, reservation, claim)
	case "review":
		return s.recoverCollabReview(ctx, assessment, reservation, claim)
	case "reaction":
		return s.recoverCollabReaction(ctx, assessment, reservation, claim)
	case "label":
		return s.recoverCollabLabel(ctx, assessment, reservation, scope, claim)
	case "milestone":
		return s.recoverCollabMilestone(ctx, assessment, reservation, scope, claim)
	case "history":
		return s.recoverCollabHistory(ctx, assessment, reservation, claim)
	case "attachment":
		return s.recoverCollabAttachment(ctx, assessment, reservation, claim)
	case "batch":
		return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("collaboration batch %q covers many entities; no single-entity reconciliation", claim.Op))
	default:
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("collaboration kind %q has no offline reconciliation", claim.Kind))
	}
}

// collabIssueAnchor loads the anchor issue for one collaboration claim. A
// missing issue means unaccounted interference and fences.
//
//nolint:unparam // Keep the common operation helper and callback signature.
func collabIssueAnchor(ctx context.Context, assessment *RecoveryAssessment, issueID int64) (*issues_model.Issue, bool, error) {
	issue, err := issues_model.GetIssueByID(ctx, issueID)
	if err != nil {
		if issues_model.IsErrIssueNotExist(err) {
			_, fenceErr := fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("issue %d for the held collaboration scope no longer exists", issueID))
			return nil, true, fenceErr
		}
		return nil, false, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("issue %d exists", issueID))
	return issue, false, nil
}

// recoverCollabIssue reconciles one issue-row writer. Single-transaction
// field updates release on the intact issue; creates, deletes, the
// multi-statement status change and pin moves (pin row and timeline
// comment commit separately) stay fenced.
func (s *Service) recoverCollabIssue(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	switch claim.Op {
	case "create", "delete", "status", "pin":
		return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("issue operation %q spans statements; no single-transaction reconciliation", claim.Op))
	}
	if _, fencedNow, err := collabIssueAnchor(ctx, assessment, claim.ID); err != nil || fencedNow {
		return *assessment, err
	}
	return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
}

// recoverCollabComment reconciles one comment writer. Conversation
// resolutions and attachment associations release on the intact comment
// and issue; creations, multi-statement deletes and content updates
// (history rows commit outside the comment transaction) stay fenced.
func (s *Service) recoverCollabComment(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	switch claim.Op {
	case "create", "delete", "update":
		return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("comment operation %q cannot establish its effect; no single-transaction reconciliation", claim.Op))
	}
	comment, err := issues_model.GetCommentByID(ctx, claim.ID)
	if err != nil {
		if issues_model.IsErrCommentNotExist(err) {
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("comment %d for the held collaboration scope no longer exists", claim.ID))
		}
		return RecoveryAssessment{}, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("comment %d exists", claim.ID))
	if _, fencedNow, err := collabIssueAnchor(ctx, assessment, comment.IssueID); err != nil || fencedNow {
		return *assessment, err
	}
	return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
}

// recoverCollabDependency reconciles one dependency edge writer. Edge
// insertion and removal are single-transaction, so a present or absent
// edge over two intact issues is always a valid end state.
func (s *Service) recoverCollabDependency(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	if _, fencedNow, err := collabIssueAnchor(ctx, assessment, claim.ID); err != nil || fencedNow {
		return *assessment, err
	}
	if _, fencedNow, err := collabIssueAnchor(ctx, assessment, claim.ID2); err != nil || fencedNow {
		return *assessment, err
	}
	return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
}

// recoverCollabPull reconciles one PR-metadata writer. Retargets, closes,
// branch updates, derived refreshes and manual merges span statements or
// Git effects and stay fenced; single-comment automerge scheduling and
// the single-statement maintainer-edits flag release on the intact pull
// request.
func (s *Service) recoverCollabPull(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	switch claim.Op {
	case "create", "retarget", "close", "update", "refresh", "manual":
		return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("pull operation %q spans statements; no single-transaction reconciliation", claim.Op))
	}
	pr, err := issues_model.GetPullRequestByID(ctx, claim.ID)
	if err != nil {
		if issues_model.IsErrPullRequestNotExist(err) {
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("pull request %d for the held collaboration scope no longer exists", claim.ID))
		}
		return RecoveryAssessment{}, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("pull request %d exists", claim.ID))
	if _, fencedNow, err := collabIssueAnchor(ctx, assessment, pr.IssueID); err != nil || fencedNow {
		return *assessment, err
	}
	return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
}

// recoverCollabReview reconciles one review writer. The single-transaction
// submission releases on the intact issue, and the single-transaction
// delete releases on the intact issue with a present or absent review
// row (both are valid atomic end states); dismissals release on the
// intact review; review requests release on the intact issue and the
// named reviewer or team.
func (s *Service) recoverCollabReview(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	switch claim.Op {
	case "request-user", "request-team":
		if _, fencedNow, err := collabIssueAnchor(ctx, assessment, claim.ID); err != nil || fencedNow {
			return *assessment, err
		}
		if claim.Op == "request-user" {
			if _, err := user_model.GetUserByID(ctx, claim.ID2); err != nil {
				if user_model.IsErrUserNotExist(err) {
					return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("user %d for the held collaboration scope no longer exists", claim.ID2))
				}
				return RecoveryAssessment{}, err
			}
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("user %d exists", claim.ID2))
		} else {
			if _, err := org_model.GetTeamByID(ctx, claim.ID2); err != nil {
				if org_model.IsErrTeamNotExist(err) {
					return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("team %d for the held collaboration scope no longer exists", claim.ID2))
				}
				return RecoveryAssessment{}, err
			}
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("team %d exists", claim.ID2))
		}
		return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
	case "delete":
		if _, fencedNow, err := collabIssueAnchor(ctx, assessment, claim.ID); err != nil || fencedNow {
			return *assessment, err
		}
		if _, err := issues_model.GetReviewByID(ctx, claim.ID2); err != nil {
			if !issues_model.IsErrReviewNotExist(err) {
				return RecoveryAssessment{}, err
			}
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("review %d absent", claim.ID2))
		} else {
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("review %d present", claim.ID2))
		}
		return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
	case "submit":
		if _, fencedNow, err := collabIssueAnchor(ctx, assessment, claim.ID); err != nil || fencedNow {
			return *assessment, err
		}
		return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
	}
	review, err := issues_model.GetReviewByID(ctx, claim.ID)
	if err != nil {
		if issues_model.IsErrReviewNotExist(err) {
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("review %d for the held collaboration scope no longer exists", claim.ID))
		}
		return RecoveryAssessment{}, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("review %d exists", claim.ID))
	if _, fencedNow, err := collabIssueAnchor(ctx, assessment, review.IssueID); err != nil || fencedNow {
		return *assessment, err
	}
	return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
}

// recoverCollabReaction reconciles one reaction writer. Reaction insertion
// and removal are single statements, so any reaction state over the
// intact anchors is a valid end state.
func (s *Service) recoverCollabReaction(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	if _, fencedNow, err := collabIssueAnchor(ctx, assessment, claim.ID); err != nil || fencedNow {
		return *assessment, err
	}
	if claim.ID2 > 0 {
		if _, err := issues_model.GetCommentByID(ctx, claim.ID2); err != nil {
			if issues_model.IsErrCommentNotExist(err) {
				return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("comment %d for the held collaboration scope no longer exists", claim.ID2))
			}
			return RecoveryAssessment{}, err
		}
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("comment %d exists", claim.ID2))
	}
	return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
}

// recoverCollabLabel reconciles one label-row writer. Single-statement
// label updates release on the intact label; creates name the repository
// and release on it; the single-transaction delete releases with a
// present or absent label row (both are valid atomic end states).
//
//nolint:unparam // Keep the common operation helper and callback signature.
func (s *Service) recoverCollabLabel(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	switch claim.Op {
	case "create", "delete":
		// Creates name the repository for repo labels and zero for org
		// labels; the repository anchor was verified above when set.
		// The single-statement insert and the single-transaction
		// delete leave no partial state behind.
		if claim.Op == "delete" {
			if _, err := issues_model.GetLabelByID(ctx, claim.ID); err != nil {
				if !issues_model.IsErrLabelNotExist(err) {
					return RecoveryAssessment{}, err
				}
				assessment.Checks = append(assessment.Checks, fmt.Sprintf("label %d absent", claim.ID))
			} else {
				assessment.Checks = append(assessment.Checks, fmt.Sprintf("label %d present", claim.ID))
			}
		}
		return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
	}
	if _, err := issues_model.GetLabelByID(ctx, claim.ID); err != nil {
		if issues_model.IsErrLabelNotExist(err) {
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("label %d for the held collaboration scope no longer exists", claim.ID))
		}
		return RecoveryAssessment{}, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("label %d exists", claim.ID))
	return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
}

// recoverCollabMilestone reconciles one milestone writer. Single-statement
// milestone updates and status flips release on the intact milestone;
// creates name the repository and release on it; the single-transaction
// delete releases with a present or absent milestone row (both are valid
// atomic end states).
func (s *Service) recoverCollabMilestone(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	switch claim.Op {
	case "create":
		if scope.RepositoryID <= 0 {
			return fenced(assessment, ReasonRecoveryUnknownFamily, "milestone create names no repository")
		}
		return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
	case "delete":
		if scope.RepositoryID <= 0 {
			return fenced(assessment, ReasonRecoveryUnknownFamily, "milestone delete names no repository")
		}
		if _, err := issues_model.GetMilestoneByRepoID(ctx, scope.RepositoryID, claim.ID); err != nil {
			if !issues_model.IsErrMilestoneNotExist(err) {
				return RecoveryAssessment{}, err
			}
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("milestone %d absent", claim.ID))
		} else {
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("milestone %d present", claim.ID))
		}
		return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
	}
	if scope.RepositoryID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "milestone scope names no repository")
	}
	if _, err := issues_model.GetMilestoneByRepoID(ctx, scope.RepositoryID, claim.ID); err != nil {
		if issues_model.IsErrMilestoneNotExist(err) {
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("milestone %d for the held collaboration scope no longer exists", claim.ID))
		}
		return RecoveryAssessment{}, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("milestone %d exists", claim.ID))
	return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
}

// recoverCollabHistory reconciles one content-history soft-delete. The
// delete is a single statement over an append-only history table, so any
// deletion state over the intact row is a valid end state; a missing row
// means unaccounted interference and fences.
func (s *Service) recoverCollabHistory(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	if _, err := issues_model.GetIssueContentHistoryByID(ctx, claim.ID); err != nil {
		var notExist issues_model.ErrIssueContentHistoryNotExist
		if errors.As(err, &notExist) {
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("content history %d for the held collaboration scope no longer exists", claim.ID))
		}
		return RecoveryAssessment{}, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("content history %d exists", claim.ID))
	return s.releaseRecovered(ctx, assessment, reservation, EffectCollaborationConsistent)
}

// recoverCollabAttachment reconciles one standalone attachment row
// operation. Uploads invent their storage UUID inside the write, so the
// created row is unidentifiable and fences. Deletes are attributable
// both ways: an absent row proves the committed end state (a leftover
// storage file is a harmless orphan the row no longer references), and
// a present row proves nothing applied. Updates fence: the applied
// values are unknowable.
func (s *Service) recoverCollabAttachment(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, claim execcontext.CollabClaim) (RecoveryAssessment, error) {
	switch claim.Op {
	case execcontext.CollabAttachmentCreate:
		assessment.Checks = append(assessment.Checks, "attachment upload invents its row identity")
		return fenced(assessment, ReasonRecoveryUncertainEffect, "attachment upload cannot be reconciled; its row is unidentifiable")
	case execcontext.CollabAttachmentUpdate:
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("attachment %d edit leaves no reconcilable trace", claim.ID))
		return fenced(assessment, ReasonRecoveryUncertainEffect, "attachment edit cannot be reconciled; restore the data set from backup")
	case execcontext.CollabAttachmentDelete:
		if _, err := repo_model.GetAttachmentByID(ctx, claim.ID); err != nil {
			if !repo_model.IsErrAttachmentNotExist(err) {
				return RecoveryAssessment{}, err
			}
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("attachment %d absent", claim.ID))
			return s.releaseRecovered(ctx, assessment, reservation, "deleted")
		}
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("attachment %d present", claim.ID))
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	default:
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("attachment operation %q has no offline reconciliation", claim.Op))
	}
}

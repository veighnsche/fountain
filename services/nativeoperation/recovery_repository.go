// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"fmt"
	"os"
	"strings"

	git_model "forgejo.org/models/git"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/git"
	execcontext "forgejo.org/modules/nativeoperation"
)

// recoveryRepo loads the repository named by a held scope. A missing row
// reports gone; any other failure is an error.
//
//nolint:nilnil // The optional lookup reports absence without an error.
func recoveryRepo(ctx context.Context, repositoryID int64) (repo *repo_model.Repository, gone bool, err error) {
	repo, err = repo_model.GetRepositoryByID(ctx, repositoryID)
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return repo, false, nil
}

// dirExists reports whether path names an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// recoverReceive reconciles one held Git receive (HTTP or SSH) from the
// ref tuples its transaction gate recorded before effects. A receive
// with no recorded tuples fences: its refs may have moved without a
// trace. Every recorded tuple must agree: all at their new OIDs release
// as received, all back at their old OIDs release as not committed, and
// any mix or third state fences as unaccounted. The resource names the
// push target ("<id>/main" or "<id>/wiki"); an unmarked owner against a
// repository with a wiki fences because the target is ambiguous.
func (s *Service) recoverReceive(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	if scope.ReceiveHasPushOptions {
		return fenced(assessment, ReasonRecoveryUncertainEffect, "receive push options may have changed repository settings; ref evidence cannot reconcile them")
	}
	if scope.RepositoryID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "receive scope names no repository")
	}
	if len(scope.Refs) == 0 {
		return fenced(assessment, ReasonRecoveryUncertainEffect, "receive scope records no proposed ref tuples")
	}
	_, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
	if !ok {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "receive owner names no resource")
	}
	repo, gone, err := recoveryRepo(ctx, scope.RepositoryID)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	if gone {
		return fenced(assessment, ReasonRecoveryMissingEvidence, "repository for the held scope no longer exists")
	}
	target, marked, err := execcontext.ReceiveRepositoryTarget(scope.RepositoryTarget, resource)
	if err != nil {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "receive target is contradictory or unknown")
	}
	if !marked && scope.RepositoryTarget == "" && repo.HasWiki() {
		return fenced(assessment, ReasonRecoveryUncertainEffect, "unmarked receive against a repository with a wiki names no push target")
	}
	repoPath := repo.RepoPath()
	if target == execcontext.TargetWiki {
		repoPath = repo.WikiPath()
	}
	landed, pending := 0, 0
	for _, tuple := range scope.Refs {
		tip, absent, fence, err := s.recoveryTip(ctx, assessment, repoPath, tuple.Ref)
		if err != nil || fence {
			return *assessment, err
		}
		wantAbsent := isZeroOID(tuple.NewOID)
		switch {
		case wantAbsent && absent:
			landed++
		case !wantAbsent && !absent && strings.EqualFold(tip, tuple.NewOID):
			landed++
		case !absent && tuple.OldOID != "" && !strings.EqualFold(tuple.OldOID, tuple.NewOID) && strings.EqualFold(tip, tuple.OldOID):
			pending++
		case wantAbsent && !absent && tuple.OldOID != "" && strings.EqualFold(tip, tuple.OldOID):
			pending++
		default:
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("ref %q absent=%t matches_new=%t matches_old=%t", tuple.Ref, absent, !absent && strings.EqualFold(tip, tuple.NewOID), !absent && tuple.OldOID != "" && strings.EqualFold(tip, tuple.OldOID)))
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("ref %q shows neither its proposed nor its previous tip", tuple.Ref))
		}
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("receive tuples landed=%d pending=%d", landed, pending))
	switch {
	case pending == 0:
		return s.releaseRecovered(ctx, assessment, reservation, "received")
	case landed == 0:
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	default:
		return fenced(assessment, ReasonRecoveryUnaccounted, "receive tuples are partially applied")
	}
}

// recoverRefWrite reconciles one held direct ref write from its recorded
// single-ref tuple or rename pair. A rename needs both sides to agree;
// a default-branch repair releases only with HEAD on the recorded
// branch, since the previous target is unrecorded.
func (s *Service) recoverRefWrite(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	if scope.RepositoryID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "ref-write scope names no repository")
	}
	_, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
	if !ok {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "ref-write owner names no resource")
	}
	parts := strings.SplitN(resource, "/", 3)
	if len(parts) != 3 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "ref-write resource names no ref-write kind")
	}
	kind := parts[1]
	repo, gone, err := recoveryRepo(ctx, scope.RepositoryID)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	if gone {
		return fenced(assessment, ReasonRecoveryMissingEvidence, "repository for the held scope no longer exists")
	}
	target, targetErr := execcontext.RefWriteRepositoryTarget(scope.RepositoryTarget, resource)
	if targetErr != nil {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "ref-write target is contradictory or unknown")
	}
	repoPath := repo.RepoPath()
	if target == execcontext.TargetWiki {
		repoPath = repo.WikiPath()
	}
	if kind == RefWriteRename {
		return s.recoverRefRename(ctx, assessment, reservation, scope, repoPath)
	}
	if kind == RefWriteDefaultBranch {
		return s.recoverDefaultBranch(ctx, assessment, reservation, scope, repoPath)
	}
	if scope.Ref == "" {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "ref-write scope names no ref")
	}
	tip, absent, fence, err := s.recoveryTip(ctx, assessment, repoPath, scope.Ref)
	if err != nil || fence {
		return *assessment, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("ref %q absent=%t matches_new=%t matches_old=%t", scope.Ref, absent, !absent && scope.NewOID != "" && strings.EqualFold(tip, scope.NewOID), !absent && scope.OldOID != "" && strings.EqualFold(tip, scope.OldOID)))
	switch {
	case scope.NewOID != "" && !absent && strings.EqualFold(tip, scope.NewOID):
		return s.releaseRecovered(ctx, assessment, reservation, "updated")
	case scope.OldOID != "" && !strings.EqualFold(scope.OldOID, scope.NewOID) && !absent && strings.EqualFold(tip, scope.OldOID):
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	default:
		if scope.NewOID == "" {
			return fenced(assessment, ReasonRecoveryUncertainEffect, "ref-write realized tip is unrecorded and the ref moved")
		}
		return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("ref %q shows neither its recorded nor its previous tip", scope.Ref))
	}
}

// recoverRefRename reconciles one held branch rename from its recorded
// from/to pair: the source ref must be gone and the destination at the
// renamed tip, or both sides must still show the pre-rename state.
func (s *Service) recoverRefRename(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, repoPath string) (RecoveryAssessment, error) {
	if len(scope.Refs) != 2 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "rename scope records no from/to pair")
	}
	from, to := scope.Refs[0], scope.Refs[1]
	fromTip, fromAbsent, fence, err := s.recoveryTip(ctx, assessment, repoPath, from.Ref)
	if err != nil || fence {
		return *assessment, err
	}
	toTip, toAbsent, fence, err := s.recoveryTip(ctx, assessment, repoPath, to.Ref)
	if err != nil || fence {
		return *assessment, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("rename from absent=%t at_old=%t to absent=%t at_new=%t",
		fromAbsent, !fromAbsent && from.OldOID != "" && strings.EqualFold(fromTip, from.OldOID),
		toAbsent, !toAbsent && to.NewOID != "" && strings.EqualFold(toTip, to.NewOID)))
	fromGone := fromAbsent
	fromKept := !fromAbsent && from.OldOID != "" && strings.EqualFold(fromTip, from.OldOID)
	toMoved := !toAbsent && to.NewOID != "" && strings.EqualFold(toTip, to.NewOID)
	switch {
	case fromGone && toMoved:
		return s.releaseRecovered(ctx, assessment, reservation, "renamed")
	case fromKept && toAbsent:
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	default:
		return fenced(assessment, ReasonRecoveryUnaccounted, "rename refs disagree with both the renamed and the pre-rename state")
	}
}

// recoverDefaultBranch reconciles one held default-branch repair: HEAD
// must point at the recorded branch. Any other HEAD state fences,
// because the previous target is unrecorded.
func (s *Service) recoverDefaultBranch(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, repoPath string) (RecoveryAssessment, error) {
	head, err := git.GetDefaultBranch(ctx, repoPath)
	pointsAtRecorded := err == nil && git.BranchPrefix+head == scope.Ref
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("default branch recorded=%q head_ok=%t", scope.Ref, pointsAtRecorded))
	if pointsAtRecorded {
		return s.releaseRecovered(ctx, assessment, reservation, "repaired")
	}
	return fenced(assessment, ReasonRecoveryUncertainEffect, "HEAD does not point at the recorded default branch")
}

// recoverRepoLifecycle reconciles one held repository lifecycle operation
// from its recorded owner/name identity. Creations need the row and its
// directory together; deletions need both gone; transfers, renames and
// conversions compare the row against the recorded pre-operation
// identity. Any partial state fences.
func (s *Service) recoverRepoLifecycle(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	_, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
	if !ok {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "lifecycle owner names no resource")
	}
	parts := strings.SplitN(resource, "/", 4)
	if len(parts) != 4 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "lifecycle resource names no operation")
	}
	op, ownerName, repoName := parts[1], parts[2], parts[3]
	switch op {
	case LifecycleCreate, LifecycleFork, LifecycleMigrate, LifecycleGenerate, LifecycleAdopt:
		return s.recoverLifecycleCreate(ctx, assessment, reservation, ownerName, repoName)
	case LifecycleDelete, LifecycleDeleteUnadopted:
		return s.recoverLifecycleDelete(ctx, assessment, reservation, scope, ownerName, repoName)
	case LifecycleTransfer, LifecycleRename:
		return s.recoverLifecycleMove(ctx, assessment, reservation, scope, op, ownerName, repoName)
	case LifecycleConvertMirror, LifecycleConvertFork:
		return s.recoverLifecycleConvert(ctx, assessment, reservation, scope, op)
	default:
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("lifecycle operation %q has no offline reconciliation", op))
	}
}

// recoverLifecycleCreate reconciles one held repository creation: the row
// and its directory must both exist, or both be absent.
func (s *Service) recoverLifecycleCreate(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, ownerName, repoName string) (RecoveryAssessment, error) {
	_, err := repo_model.GetRepositoryByOwnerAndName(ctx, ownerName, repoName)
	rowMissing := err != nil && repo_model.IsErrRepoNotExist(err)
	if err != nil && !rowMissing {
		return RecoveryAssessment{}, err
	}
	pathMissing := !dirExists(repo_model.RepoPath(ownerName, repoName))
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("lifecycle create row_missing=%t path_missing=%t", rowMissing, pathMissing))
	switch {
	case !rowMissing && !pathMissing:
		return s.releaseRecovered(ctx, assessment, reservation, "created")
	case rowMissing && pathMissing:
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	default:
		return fenced(assessment, ReasonRecoveryUnaccounted, "repository row and directory disagree after a held creation")
	}
}

// recoverLifecycleDelete reconciles one held repository deletion: the row
// and its directory must both be gone, or the row must still exist.
func (s *Service) recoverLifecycleDelete(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, ownerName, repoName string) (RecoveryAssessment, error) {
	_, gone, err := recoveryRepo(ctx, scope.RepositoryID)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	pathMissing := !dirExists(repo_model.RepoPath(ownerName, repoName))
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("lifecycle delete row_missing=%t path_missing=%t", gone, pathMissing))
	switch {
	case gone && pathMissing:
		return s.releaseRecovered(ctx, assessment, reservation, "deleted")
	case !gone:
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	default:
		return fenced(assessment, ReasonRecoveryUnaccounted, "repository row is gone but its directory remains after a held deletion")
	}
}

// recoverLifecycleMove reconciles one held transfer or rename by comparing
// the row's current identity against the recorded pre-operation identity:
// any change releases as moved, an unchanged row releases as not
// committed, and a missing row fences.
func (s *Service) recoverLifecycleMove(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, op, ownerName, repoName string) (RecoveryAssessment, error) {
	repo, gone, err := recoveryRepo(ctx, scope.RepositoryID)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	if gone {
		return fenced(assessment, ReasonRecoveryUnaccounted, "repository row is gone after a held move")
	}
	moved := repo.OwnerName != ownerName || repo.Name != repoName
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("lifecycle %s moved=%t", op, moved))
	if moved {
		return s.releaseRecovered(ctx, assessment, reservation, "moved")
	}
	return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
}

// recoverLifecycleConvert reconciles one held fork or mirror conversion
// from the row's current flags: a converted row releases, an unconverted
// row releases as not committed, and a missing row fences.
func (s *Service) recoverLifecycleConvert(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope, op string) (RecoveryAssessment, error) {
	repo, gone, err := recoveryRepo(ctx, scope.RepositoryID)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	if gone {
		return fenced(assessment, ReasonRecoveryUnaccounted, "repository row is gone after a held conversion")
	}
	converted := (op == LifecycleConvertMirror && !repo.IsMirror) || (op == LifecycleConvertFork && !repo.IsFork)
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("lifecycle %s converted=%t is_mirror=%t is_fork=%t", op, converted, repo.IsMirror, repo.IsFork))
	if converted {
		return s.releaseRecovered(ctx, assessment, reservation, "converted")
	}
	return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
}

// recoverRepoSettings always fences: a settings scope records its area
// but no before/after values, so the applied effect is unknowable. The
// check names the area for the operator to re-apply.
//
//nolint:unparam // Keep the common operation helper and callback signature.
func (s *Service) recoverRepoSettings(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	_, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
	if !ok {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "settings owner names no resource")
	}
	_, area, _ := strings.Cut(resource, "/")
	if area == "" {
		area = resource
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("settings area %q leaves no reconcilable trace", area))
	return fenced(assessment, ReasonRecoveryUncertainEffect, fmt.Sprintf("settings area %q cannot be reconciled; restore the data set from backup", area))
}

// recoverProtection reconciles one held protection rule change by rule
// presence. Creates and deletes are attributable both ways; an edit with
// the rule present fences because the applied field values are
// unknowable, and an edit with the rule absent fences as impossible
// under the held reservation.
func (s *Service) recoverProtection(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	if scope.RepositoryID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "protection scope names no repository")
	}
	_, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
	if !ok {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "protection owner names no resource")
	}
	parts := strings.SplitN(resource, "/", 4)
	if len(parts) != 4 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "protection resource names no rule operation")
	}
	kind, op, name := parts[1], parts[2], parts[3]
	present, err := protectionRulePresent(ctx, scope.RepositoryID, kind, name)
	if err != nil {
		if err == errProtectionUnknownKind {
			return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("protection kind %q has no offline reconciliation", kind))
		}
		return RecoveryAssessment{}, err
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("protection %s %s present=%t", kind, op, present))
	switch op {
	case ProtectionCreate:
		if present {
			return s.releaseRecovered(ctx, assessment, reservation, "protected")
		}
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	case ProtectionDelete:
		if !present {
			return s.releaseRecovered(ctx, assessment, reservation, "unprotected")
		}
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	case ProtectionEdit:
		if !present {
			return fenced(assessment, ReasonRecoveryUnaccounted, "edited protection rule is absent, which the held reservation forbids")
		}
		return fenced(assessment, ReasonRecoveryUncertainEffect, "edited protection rule is present but its applied values are unknowable")
	default:
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("protection operation %q has no offline reconciliation", op))
	}
}

// errProtectionUnknownKind marks a protection resource whose rule kind is
// not branch or tag.
var errProtectionUnknownKind = fmt.Errorf("unknown protection kind")

// protectionRulePresent reports whether the named branch or tag rule
// exists for the repository.
func protectionRulePresent(ctx context.Context, repoID int64, kind, name string) (bool, error) {
	switch kind {
	case ProtectionBranch:
		rule, err := git_model.GetProtectedBranchRuleByName(ctx, repoID, name)
		if err != nil {
			return false, err
		}
		return rule != nil, nil
	case ProtectionTag:
		rule, err := git_model.GetProtectedTagByNamePattern(ctx, repoID, name)
		if err != nil {
			return false, err
		}
		return rule != nil, nil
	default:
		return false, errProtectionUnknownKind
	}
}

// recoverMirrorSync reconciles one held mirror synchronization by
// verifying every recorded ref still shows its end state. Refs prefixed
// "wiki:" resolve in the wiki repository; the rest resolve in the main
// repository. A settings-only scope with no recorded refs fences. A sync
// that never fetched still matches its pre-state tuples and releases as
// consistently stale; the scheduler re-syncs it.
func (s *Service) recoverMirrorSync(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	if scope.RepositoryID <= 0 || len(scope.Refs) == 0 {
		return fenced(assessment, ReasonRecoveryUncertainEffect, "mirror-sync scope records no repository refs")
	}
	repo, gone, err := recoveryRepo(ctx, scope.RepositoryID)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	if gone {
		return fenced(assessment, ReasonRecoveryMissingEvidence, "repository for the held scope no longer exists")
	}
	for _, tuple := range scope.Refs {
		repoPath := repo.RepoPath()
		target, ref, targetErr := execcontext.ScopedRefRepositoryTarget(tuple)
		if targetErr != nil {
			return fenced(assessment, ReasonRecoveryUnknownFamily, "mirror ref target is contradictory or unknown")
		}
		if target == execcontext.TargetWiki {
			repoPath = repo.WikiPath()
		}
		tip, absent, fence, err := s.recoveryTip(ctx, assessment, repoPath, ref)
		if err != nil || fence {
			return *assessment, err
		}
		if isZeroOID(tuple.NewOID) {
			assessment.Checks = append(assessment.Checks, fmt.Sprintf("ref %q deleted, absent=%t", tuple.Ref, absent))
			if !absent {
				return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("ref %q should be deleted", tuple.Ref))
			}
			continue
		}
		match := !absent && strings.EqualFold(tip, tuple.NewOID)
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("ref %q absent=%t matches_expected=%t", tuple.Ref, absent, match))
		if !match {
			return fenced(assessment, ReasonRecoveryUnaccounted, fmt.Sprintf("ref %q no longer shows the sync end state", tuple.Ref))
		}
	}
	return s.releaseRecovered(ctx, assessment, reservation, "consistent")
}

// recoverRefSync reconciles one held branch synchronization by comparing
// each recorded branch row against its expected commit. Rows at their
// expected commits release as synced; rows anywhere else count as
// pending, since only this owner could have moved them. A mix fences as
// a partial sync. A scope with no recorded refs fences. The
// deleted-branches purge is one atomic delete over an unknowable set, so
// any observed state is a valid end state and releases.
func (s *Service) recoverRefSync(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	if _, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner); ok && resource == "0/deleted-branches" {
		assessment.Checks = append(assessment.Checks, "ref-sync purge is one atomic delete; any observed state is valid")
		return s.releaseRecovered(ctx, assessment, reservation, "consistent")
	}
	if scope.RepositoryID <= 0 || len(scope.Refs) == 0 {
		return fenced(assessment, ReasonRecoveryUncertainEffect, "ref-sync scope records no expected branch commits")
	}
	landed, pending := 0, 0
	for _, tuple := range scope.Refs {
		branchName := strings.TrimPrefix(tuple.Ref, git.BranchPrefix)
		branch, err := git_model.GetBranch(ctx, scope.RepositoryID, branchName)
		if err != nil {
			if git_model.IsErrBranchNotExist(err) {
				pending++
				continue
			}
			return RecoveryAssessment{}, err
		}
		if branch.IsDeleted || !strings.EqualFold(branch.CommitID, tuple.NewOID) {
			pending++
			continue
		}
		landed++
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("ref-sync branches landed=%d pending=%d", landed, pending))
	switch {
	case pending == 0:
		return s.releaseRecovered(ctx, assessment, reservation, "synced")
	case landed == 0:
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	default:
		return fenced(assessment, ReasonRecoveryUnaccounted, "branch rows are partially synchronized")
	}
}

// maintenanceBatchOps names the instance-wide repair batches. Every one
// is a single atomic statement (or a read plus one statement) over rows
// its predicate selects, so any observed state is a valid end state and
// releases; the operator reruns the repair to complete it. The "orphan-"
// prefix covers every generic orphan sweep.
var maintenanceBatchOps = map[string]bool{
	"orphan-attachments": true, "null-archived": true,
	"runner-owner": true, "runner-repo": true,
	"topics": true, "oauth2-apps": true, "owner-teams": true, "user-type": true,
	"team-8312": true,
}

// recoverMaintenance reconciles one held maintenance operation. A reinit
// releases by directory presence. Garbage collection releases once the
// repository opens and its default branch resolves: git gc never
// rewrites reachable data (it copies reachable objects into new packs
// before deleting the old ones and prunes only unreachable objects), so
// an intact head commit proves no observable loss; the pruned set stays
// unknowable and the next scheduled run finishes pruning. The LFS
// collection, the empty-flag repair and the atomic repair batches
// release the same way with rerun notes.
func (s *Service) recoverMaintenance(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, scope Scope) (RecoveryAssessment, error) {
	_, resource, ok := execcontext.SplitOrdinaryResource(reservation.Owner)
	if !ok {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "maintenance owner names no resource")
	}
	_, op, _ := strings.Cut(resource, "/")
	if maintenanceBatchOps[op] || strings.HasPrefix(op, "orphan-") {
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("maintenance batch %q is atomic; rerun the repair to complete it", op))
		return s.releaseRecovered(ctx, assessment, reservation, "consistent")
	}
	if scope.RepositoryID <= 0 {
		return fenced(assessment, ReasonRecoveryUnknownFamily, "maintenance scope names no repository")
	}
	repo, gone, err := recoveryRepo(ctx, scope.RepositoryID)
	if err != nil {
		return RecoveryAssessment{}, err
	}
	if gone {
		return fenced(assessment, ReasonRecoveryMissingEvidence, "repository for the held scope no longer exists")
	}
	switch op {
	case "reinit":
		exists := dirExists(repo.RepoPath())
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("maintenance reinit directory_present=%t", exists))
		if exists {
			return s.releaseRecovered(ctx, assessment, reservation, "reinitialized")
		}
		return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
	case "gc":
		return s.recoverMaintenanceGC(ctx, assessment, reservation, repo)
	case "lfs-gc":
		assessment.Checks = append(assessment.Checks, "maintenance lfs-gc collects independent objects; the next scheduled run completes it")
		return s.releaseRecovered(ctx, assessment, reservation, "consistent")
	case "is-empty":
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("maintenance is-empty flag=%t", repo.IsEmpty))
		if repo.IsEmpty {
			return s.releaseRecovered(ctx, assessment, reservation, nativeoperation_model.EffectNotCommitted)
		}
		return s.releaseRecovered(ctx, assessment, reservation, "repaired")
	case "unit-16961":
		assessment.Checks = append(assessment.Checks, "maintenance unit repair is one atomic update; rerun the repair to complete it")
		return s.releaseRecovered(ctx, assessment, reservation, "consistent")
	default:
		return fenced(assessment, ReasonRecoveryUnknownFamily, fmt.Sprintf("maintenance operation %q has no offline reconciliation", op))
	}
}

// recoverMaintenanceGC reconciles one held garbage collection by
// verifying the repository still opens and its default branch resolves
// to a commit. Either failure fences for fsck and repair, since the
// object store may be damaged.
func (s *Service) recoverMaintenanceGC(ctx context.Context, assessment *RecoveryAssessment, reservation *nativeoperation_model.Reservation, repo *repo_model.Repository) (RecoveryAssessment, error) {
	if repo.IsEmpty {
		assessment.Checks = append(assessment.Checks, "maintenance gc empty repository needs no collection")
		return s.releaseRecovered(ctx, assessment, reservation, "consistent")
	}
	head, err := git.GetDefaultBranch(ctx, repo.RepoPath())
	if err != nil {
		assessment.Checks = append(assessment.Checks, "maintenance gc HEAD cannot be read")
		return fenced(assessment, ReasonRecoveryUnaccounted, "repository HEAD is unreadable after a held garbage collection; run fsck")
	}
	_, absent, fence, err := s.recoveryTip(ctx, assessment, repo.RepoPath(), git.BranchPrefix+head)
	if err != nil || fence {
		return *assessment, err
	}
	if absent {
		assessment.Checks = append(assessment.Checks, fmt.Sprintf("maintenance gc default branch %q has no tip", head))
		return fenced(assessment, ReasonRecoveryUnaccounted, "default branch has no tip after a held garbage collection; run fsck")
	}
	assessment.Checks = append(assessment.Checks, fmt.Sprintf("maintenance gc default branch %q resolves", head))
	return s.releaseRecovered(ctx, assessment, reservation, "consistent")
}

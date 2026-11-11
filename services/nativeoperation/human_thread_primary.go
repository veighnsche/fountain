// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"reflect"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	access_model "forgejo.org/models/perm/access"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
)

// PreparedHumanThreadResult is native primary evidence, not a journal receipt.
// Resolving an already resolved root preserves its existing resolver. No-op
// transitions retain exact evidence; unknown original context stays unknown.
type PreparedHumanThreadResult struct {
	Before  sdk.HumanThreadEvidence
	After   sdk.HumanThreadEvidence
	Changed bool
}

// ApplyPreparedHumanThread joins the caller's transaction and requires the exact
// native owner of the captured revision. ActorID is the caller's trusted current
// human, never selected from request JSON. Admit must recheck that same human,
// request/installation and lifetime. This primary performs fresh Git/policy and
// complete conversation proof, then native root-only MarkConversation SQL.
// Caller-owned journal/receipt and primary can commit or roll back together.
// It claims no owner, dispatches no notification and implements no replay.
func ApplyPreparedHumanThread(ctx context.Context, actorID int64, want sdk.RepositoryThreadSnapshot, resolved bool, admit HumanReviewAdmission) (*PreparedHumanThreadResult, error) {
	p := want.Review
	refuse := func(code issues_model.ExactReviewRefusalCode, reason string) (*PreparedHumanThreadResult, error) {
		return nil, issues_model.NewExactReviewRefusal(code, reason)
	}
	if actorID <= 0 || actorID != nativeCommentID(p.ActorID) || admit == nil || !sdk.ValidRepositoryThreadSnapshot(&want, p.RepositoryID, p.ActorID, sdk.RepositoryThreadSnapshotRequest{Index: p.Index, ThreadID: want.Thread.RootCommentID}) {
		return refuse(reviewRefusalInvalidHumanThreadTarget, "invalid human thread target")
	}
	action := want.UnresolveAction
	if resolved {
		action = want.ResolveAction
	}
	if action.State != "allowed" {
		return refuse(reviewRefusalHumanThreadActionWasNotAdmitted, "human thread action was not admitted")
	}
	if err := requireHumanPrimaryOwner(ctx, p.NativeRevision); err != nil {
		return nil, err
	}
	ctx, committer, err := db.TxContext(ctx)
	if err != nil {
		return nil, err
	}
	defer committer.Close()
	if err := admitHumanPrimary(ctx, admit); err != nil {
		return nil, err
	}
	actor, issue, err := loadHumanThreadPrimary(ctx, actorID, p)
	if err != nil {
		return nil, err
	}
	before, err := ReadHumanThreadEvidence(ctx, issue, actor, nativeCommentID(want.Thread.RootCommentID))
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before, want.Thread) {
		return refuse(reviewRefusalHumanThreadConversationChanged, "human thread conversation changed")
	}
	nextResolver := int64(0)
	if resolved {
		nextResolver = nativeCommentID(before.ResolverID)
		if !before.Resolved {
			nextResolver = actorID
		}
	}
	predicted, err := readHumanThreadEvidence(ctx, issue, actor, nativeCommentID(want.Thread.RootCommentID), &nextResolver)
	if err != nil {
		return nil, err
	}
	if err := admitHumanPrimary(ctx, admit); err != nil {
		return nil, err
	}
	// Admission reads can span I/O. Repeat current policy/Git after them, before
	// the native effect; native ownership orders every participating writer.
	actor, issue, err = loadHumanThreadPrimary(ctx, actorID, p)
	if err != nil {
		return nil, err
	}
	fresh, err := ReadHumanThreadEvidence(ctx, issue, actor, nativeCommentID(want.Thread.RootCommentID))
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(fresh, before) {
		return refuse(reviewRefusalHumanThreadConversationChanged, "human thread conversation changed")
	}
	if err := requireHumanPrimaryOwner(ctx, p.NativeRevision); err != nil {
		return nil, err
	}
	root, err := issues_model.GetCommentByID(ctx, nativeCommentID(want.Thread.RootCommentID))
	if err != nil {
		return nil, err
	}
	if err := issues_model.MarkConversation(ctx, root, actor, resolved); err != nil {
		return nil, err
	}
	after, err := ReadHumanThreadEvidence(ctx, issue, actor, root.ID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(after, predicted) {
		return refuse(reviewRefusalHumanThreadPrimaryEvidenceDiffers, "human thread primary evidence differs")
	}
	if err := admitHumanPrimary(ctx, admit); err != nil {
		return nil, err
	}
	if err := requireHumanPrimaryOwner(ctx, p.NativeRevision); err != nil {
		return nil, err
	}
	if err := committer.Commit(); err != nil {
		return nil, err
	}
	return &PreparedHumanThreadResult{Before: before, After: after, Changed: before.ResolverID != after.ResolverID}, nil
}

// Current native SQL and Git facts are observed under held ownership. Resolution
// follows CanMarkConversation; it does not inherit review-event/self-review or
// pending-draft adoption rules unrelated to changing a thread's resolution.
func loadHumanThreadPrimary(ctx context.Context, actorID int64, p sdk.RepositoryReviewSnapshot) (*user_model.User, *issues_model.Issue, error) {
	refuse := func(code issues_model.ExactReviewRefusalCode, reason string) (*user_model.User, *issues_model.Issue, error) {
		return nil, nil, issues_model.NewExactReviewRefusal(code, reason)
	}
	actor, err := user_model.GetUserByID(ctx, actorID)
	if err != nil {
		return nil, nil, err
	}
	if !actor.IsAccessAllowed(ctx) {
		return refuse(reviewRefusalThreadActorIsInactive, "thread actor is inactive")
	}
	issue, err := issues_model.GetIssueByID(ctx, nativeCommentID(p.IssueID))
	if err != nil {
		return nil, nil, err
	}
	if issue.RepoID != nativeCommentID(p.RepositoryID) || issue.Index != nativeCommentID(p.Index) || !issue.IsPull || issue.IsClosed != p.IsClosed || issue.IsPoster(actorID) != p.IsAuthor {
		return refuse(reviewRefusalHumanThreadParentChanged, "human thread parent changed")
	}
	if err := issue.LoadPullRequest(ctx); err != nil {
		return nil, nil, err
	}
	pr := issue.PullRequest
	if pr == nil || pr.ID != nativeCommentID(p.PullID) || pr.IssueID != issue.ID || pr.BaseRepoID != issue.RepoID || pr.Index != issue.Index || pr.BaseBranch != p.BaseBranch || pr.HasMerged != p.HasMerged {
		return refuse(reviewRefusalHumanThreadPullChanged, "human thread pull changed")
	}
	if err := issue.LoadRepo(ctx); err != nil {
		return nil, nil, err
	}
	repo := issue.Repo
	if repo.IsArchived || !repo.AllowsPulls(ctx) {
		return refuse(reviewRefusalThreadWorkflowUnavailable, "thread workflow unavailable")
	}
	permission, err := access_model.GetUserRepoPermission(ctx, repo, actor)
	if err != nil {
		return nil, nil, err
	}
	for _, kind := range []unit.Type{unit.TypeCode, unit.TypePullRequests} {
		if kind.UnitGlobalDisabled() || !repo.UnitEnabled(ctx, kind) || !permission.CanRead(kind) {
			return refuse(reviewRefusalThreadActorCannotReadPullRequest, "thread actor cannot read pull request")
		}
	}
	allowed, err := issues_model.CanMarkConversation(ctx, issue, actor)
	if err != nil {
		return nil, nil, err
	}
	if !allowed {
		return refuse(reviewRefusalHumanThreadPermissionLost, "human thread permission lost")
	}
	native, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		return nil, nil, err
	}
	defer native.Close()
	head, err := native.GetRefCommitID(pr.GetGitRefName())
	if err != nil || head != p.HeadOID {
		return refuse(issues_model.ReviewRefusalHeadChanged, "head changed")
	}
	base, err := native.GetRefCommitID(git.BranchPrefix + p.BaseBranch)
	if err != nil || base != p.BaseOID {
		return refuse(issues_model.ReviewRefusalBaseChanged, "base changed")
	}
	mergeBase, err := native.GetMergeBaseSimple(base, head)
	if err != nil || mergeBase != p.MergeBaseOID {
		return refuse(issues_model.ReviewRefusalMergeBaseChanged, "merge base changed")
	}
	return actor, issue, nil
}

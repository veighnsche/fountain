// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	user_model "forgejo.org/models/user"
)

var (
	ErrHumanThreadMissing  = errors.New("human thread is not visible")
	ErrHumanThreadTarget   = errors.New("human thread requires an exact valid root")
	ErrHumanThreadChanged  = errors.New("human thread changed during preparation")
	ErrHumanThreadTooLarge = errors.New("complete human thread exceeds preparation bounds")
)

// ReadHumanThreadEvidence observes a complete bounded conversation through the
// defining native grouping. Callers own current read/mutation admission and Git
// proofs. It starts no transaction, claims no owner and changes no native rows.
// Conditional writers may repeat this same proof under their held owner.
func ReadHumanThreadEvidence(ctx context.Context, issue *issues_model.Issue, user *user_model.User, rootID int64) (sdk.HumanThreadEvidence, error) {
	return readHumanThreadEvidence(ctx, issue, user, rootID, nil)
}

// The internal resolver override predicts exactly one root-only SQL transition
// from complete live evidence, without changing any native row.
func readHumanThreadEvidence(ctx context.Context, issue *issues_model.Issue, user *user_model.User, rootID int64, resolverOverride *int64) (sdk.HumanThreadEvidence, error) {
	var empty sdk.HumanThreadEvidence
	if issue == nil || !issue.IsPull || user == nil || rootID <= 0 {
		return empty, ErrHumanThreadTarget
	}
	var rows issues_model.CommentList
	err := db.GetEngine(ctx).Where("issue_id=? AND type=?", issue.ID, issues_model.CommentTypeCode).
		Cols("id", "type", "issue_id", "review_id", "poster_id", "tree_path", "line", "created_unix", "updated_unix", "content_version", "resolve_doer_id", "invalidated", "commit_sha", "original_anchor").
		Asc("created_unix").Asc("id").Limit(10001).Find(&rows)
	if err != nil {
		return empty, err
	}
	if len(rows) > 10000 {
		return empty, ErrHumanThreadTooLarge
	}
	thread := issues_model.FindCodeThread(issues_model.GroupCodeThreads(rows), rootID)
	if len(thread) == 0 {
		return empty, ErrHumanThreadMissing
	}
	root := thread.Root()
	// Check visibility before reporting whether the supplied comment was a reply.
	review, err := issues_model.GetReviewByID(ctx, root.ReviewID)
	if err != nil {
		return empty, err
	}
	if review.IssueID != issue.ID || review.Type == issues_model.ReviewTypePending && review.ReviewerID != user.ID {
		return empty, ErrHumanThreadMissing
	}
	if root.ID != rootID {
		return empty, ErrHumanThreadTarget
	}
	if len(thread) > sdk.MaxThreadSnapshotMembers {
		return empty, ErrHumanThreadTooLarge
	}
	if root.TreePath == "" || len(root.TreePath) > 4096 || !utf8.ValidString(root.TreePath) || strings.ContainsRune(root.TreePath, 0) || root.Line < -9007199254740991 || root.Line > 9007199254740991 {
		return empty, ErrHumanThreadTarget
	}
	side, line := "", int64(0)
	if root.Line < 0 {
		side, line = "old", -root.Line
	} else if root.Line > 0 {
		side, line = "new", root.Line
	}
	location := sdk.RenderThreadLocation(root.TreePath, side, line)
	// Exact metadata complements the shared conversation digest, which encodes
	// boolean resolver state rather than the native resolver's identity.
	type evidenceMember struct {
		ID, IssueID, ReviewID, PosterID, Type, Line, Version, Resolver, Created, Updated, OriginalAuthorID int64
		Path, Commit, PatchDigest, OriginalAuthorDigest                                                    string
		Invalidated                                                                                        bool
		OriginalAnchor                                                                                     *sdk.RepositoryInlineAnchor
	}
	facts := make([]evidenceMember, 0, len(thread))
	lines := make([]string, 0, len(thread))
	total := 0
	for _, meta := range thread {
		c, err := issues_model.GetCommentByID(ctx, meta.ID)
		if err != nil {
			return empty, ErrHumanThreadChanged
		}
		if c.ID != meta.ID || c.Type != meta.Type || c.IssueID != meta.IssueID || c.ReviewID != meta.ReviewID || c.PosterID != meta.PosterID || c.TreePath != meta.TreePath || c.Line != meta.Line || c.CreatedUnix != meta.CreatedUnix || c.UpdatedUnix != meta.UpdatedUnix || c.ContentVersion != meta.ContentVersion || c.ResolveDoerID != meta.ResolveDoerID || c.Invalidated != meta.Invalidated || c.CommitSHA != meta.CommitSHA || !c.OriginalAnchor.Equal(meta.OriginalAnchor) {
			return empty, ErrHumanThreadChanged
		}
		if c.OriginalAnchor != nil && c.OriginalAnchor.Validate() != nil || !utf8.ValidString(c.Content) || strings.ContainsRune(c.Content, 0) {
			return empty, ErrHumanThreadTarget
		}
		total += len(c.Content) + len(c.Patch)
		if total > sdk.MaxThreadSnapshotBodyBytes {
			return empty, ErrHumanThreadTooLarge
		}
		if c.ID == rootID && resolverOverride != nil {
			c.ResolveDoerID = *resolverOverride
		}
		anchor := threadOriginalAnchor(c.OriginalAnchor)
		lines = append(lines, sdk.ResolveConversationLine(sdk.ResolveConversationMember{OriginalAnchor: anchor, ID: c.ID, Body: c.Content, ContentVer: int64(c.ContentVersion), Location: location, ResolverState: sdk.ThreadResolverState(c.ResolveDoerID)}))
		facts = append(facts, evidenceMember{ID: c.ID, IssueID: c.IssueID, ReviewID: c.ReviewID, PosterID: c.PosterID, Type: int64(c.Type), Line: c.Line, Version: int64(c.ContentVersion), Resolver: c.ResolveDoerID, Created: int64(c.CreatedUnix), Updated: int64(c.UpdatedUnix), Path: c.TreePath, Commit: c.CommitSHA, Invalidated: c.Invalidated, OriginalAnchor: anchor, OriginalAuthorID: c.OriginalAuthorID, PatchDigest: sdk.HumanReviewBodyDigest(c.Patch), OriginalAuthorDigest: sdk.HumanReviewBodyDigest(c.OriginalAuthor)})
	}
	digest, err := sdk.ResolveConversationDigest(lines)
	if err != nil {
		return empty, err
	}
	raw, err := json.Marshal(struct {
		ReviewID, ReviewerID, ReviewerTeamID, OriginalAuthorID, ReviewType, Created, Updated int64
		ReviewDigest, OriginalAuthorDigest, Commit                                           string
		Official, Stale, Dismissed                                                           bool
		Members                                                                              []evidenceMember
	}{review.ID, review.ReviewerID, review.ReviewerTeamID, review.OriginalAuthorID, int64(review.Type), int64(review.CreatedUnix), int64(review.UpdatedUnix), sdk.HumanReviewBodyDigest(review.Content), sdk.HumanReviewBodyDigest(review.OriginalAuthor), review.CommitID, review.Official, review.Stale, review.Dismissed, facts})
	if err != nil {
		return empty, err
	}
	resolverID := root.ResolveDoerID
	if resolverOverride != nil {
		resolverID = *resolverOverride
	}
	resolver := ""
	if resolverID != 0 {
		resolver = strconv.FormatInt(resolverID, 10)
	}
	return sdk.HumanThreadEvidence{RootCommentID: strconv.FormatInt(root.ID, 10), ReviewID: strconv.FormatInt(root.ReviewID, 10), OriginalAnchor: threadOriginalAnchor(root.OriginalAnchor), ConversationDigest: digest, EvidenceDigest: sdk.HumanReviewBodyDigest(string(raw)), MemberCount: len(thread), ResolverID: resolver, Resolved: resolver != ""}, nil
}

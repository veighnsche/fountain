// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"context"
	"errors"
	"sort"

	"forgejo.org/models/db"
)

// CodeThread is one native code-review conversation: code comments sharing
// one immutable anchor within a (review, path, signed line) bucket in native (created_unix, id)
// order with the anchored root first. Replies are members, never
// resolution targets: the root carries the conversation identity and its
// resolve-doer carries the resolution.
type CodeThread []*Comment

// Root reports the anchored conversation root: the first comment in
// native (created_unix, id) order. It reports nil for an empty thread.
func (t CodeThread) Root() *Comment {
	if len(t) == 0 {
		return nil
	}
	return t[0]
}

// FetchThreadComments returns every code comment on the issue in native
// (created_unix, id) order with the owning reviews joined, including
// outdated (invalidated) comments and pending-review comments the reader
// may not see. Callers decide visibility per member: a member behind a
// pending review authored by another actor stays hidden. Reads perform no
// filtering so snapshot digests and resolve bindings observe the same
// complete conversation the host resolves.
func FetchThreadComments(ctx context.Context, issue *Issue) (CommentList, map[int64]*Review, error) {
	conds := FindCommentsOptions{Type: CommentTypeCode, IssueID: issue.ID}.ToConds()
	var comments CommentList
	if err := db.GetEngine(ctx).Where(conds).
		Asc("comment.created_unix").
		Asc("comment.id").
		Find(&comments); err != nil {
		return nil, nil, err
	}
	reviews, err := loadThreadCommentRelations(ctx, issue, comments)
	if err != nil {
		return nil, nil, err
	}
	return comments, reviews, nil
}

// ThreadCommentRead carries a complete issue context or an explicit overflow.
// Incomplete reads expose no prefix: later anchored comments can change how a
// retained anchorless comment belongs to a conversation.
type ThreadCommentRead struct {
	Comments CommentList
	Reviews  map[int64]*Review
	Complete bool
}

// FetchThreadCommentsBounded reads at most limit+1 rows, using the last row only
// as an overflow sentinel. Relations are hydrated only for a complete context.
// Exact native writers retain FetchThreadComments for their full-context read.
func FetchThreadCommentsBounded(ctx context.Context, issue *Issue, limit int) (ThreadCommentRead, error) {
	if issue == nil || issue.ID <= 0 || limit <= 0 {
		return ThreadCommentRead{}, errors.New("invalid thread comment bound")
	}
	conds := FindCommentsOptions{Type: CommentTypeCode, IssueID: issue.ID}.ToConds()
	var comments CommentList
	if err := db.GetEngine(ctx).Where(conds).
		Asc("comment.created_unix").Asc("comment.id").Limit(limit + 1).
		Find(&comments); err != nil {
		return ThreadCommentRead{}, err
	}
	if len(comments) > limit {
		return ThreadCommentRead{Complete: false}, nil
	}
	reviews, err := loadThreadCommentRelations(ctx, issue, comments)
	if err != nil {
		return ThreadCommentRead{}, err
	}
	return ThreadCommentRead{Comments: comments, Reviews: reviews, Complete: true}, nil
}

func loadThreadCommentRelations(ctx context.Context, issue *Issue, comments CommentList) (map[int64]*Review, error) {
	if err := issue.LoadRepo(ctx); err != nil {
		return nil, err
	}
	if err := comments.LoadPosters(ctx); err != nil {
		return nil, err
	}
	if err := comments.LoadResolveDoers(ctx); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(comments))
	for _, comment := range comments {
		if comment.ReviewID != 0 {
			ids = append(ids, comment.ReviewID)
		}
	}
	reviews := make(map[int64]*Review)
	if err := db.GetEngine(ctx).In("id", ids).Find(&reviews); err != nil {
		return nil, err
	}
	for _, comment := range comments {
		if re, ok := reviews[comment.ReviewID]; ok && re != nil {
			comment.Review = re
		}
	}
	return reviews, nil
}

// CodeThreadIdentity describes a legacy location-only selector. A complete
// empty location has RootID zero; ambiguous or incomplete contexts never name
// a root. Callers must refuse those selectors rather than guessing a thread.
type CodeThreadIdentity struct {
	RootID    int64
	Complete  bool
	Ambiguous bool
}

// ReadCodeThreadIdentity resolves a native location against its entire bounded
// context. The sentinel is checked before grouping so a prefix cannot make an
// anchorless comment appear to belong to a sole recorded immutable context.
func ReadCodeThreadIdentity(ctx context.Context, issueID, reviewID int64, treePath string, line int64, limit int) (CodeThreadIdentity, error) {
	if limit <= 0 {
		return CodeThreadIdentity{}, errors.New("invalid thread identity selector")
	}
	var rows CommentList
	if err := db.GetEngine(ctx).Where("issue_id = ? AND review_id = ? AND tree_path = ? AND line = ? AND type = ?",
		issueID, reviewID, treePath, line, CommentTypeCode).
		Cols("id", "type", "issue_id", "review_id", "tree_path", "line", "created_unix", "original_anchor").
		Asc("created_unix").Asc("id").Limit(limit + 1).Find(&rows); err != nil {
		return CodeThreadIdentity{}, err
	}
	if len(rows) > limit {
		return CodeThreadIdentity{Complete: false}, nil
	}
	threads := GroupCodeThreads(rows)
	if len(threads) > 1 {
		return CodeThreadIdentity{Complete: true, Ambiguous: true}, nil
	}
	identity := CodeThreadIdentity{Complete: true}
	if len(threads) == 1 {
		identity.RootID = threads[0].Root().ID
	}
	return identity, nil
}

// threadGroupKey anchors one conversation: its review, tree path and
// signed line. The sign carries the diff side: negative lines anchor the
// previous (merge-base) tree, positive lines the proposed (head) tree.
type threadGroupKey struct {
	issueID  int64
	reviewID int64
	treePath string
	line     int64
}

// GroupCodeThreads partitions native location buckets by the entire recorded
// immutable anchor, including file side, range and original trees. A retained
// nil-context reply joins a sole valid context only when that is unambiguous;
// otherwise unknown context forms its own conversation. No context is invented
// on the retained row. Malformed recorded anchors never absorb unknown replies.
// Roots and members remain ordered by (created_unix, id).
// The caller supplies the full available context; a partial read must not use
// this function to infer anchorless identity.
func GroupCodeThreads(comments CommentList) []CodeThread {
	groups := make(map[threadGroupKey][]*Comment)
	order := make([]threadGroupKey, 0, len(comments))
	for _, comment := range comments {
		if comment == nil || comment.Type != CommentTypeCode || comment.ReviewID == 0 {
			continue
		}
		key := threadGroupKey{issueID: comment.IssueID, reviewID: comment.ReviewID, treePath: comment.TreePath, line: comment.Line}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], comment)
	}
	threads := make([]CodeThread, 0, len(order))
	for _, key := range order {
		// Presence is separate from the zero value: a malformed recorded
		// anchor must not become indistinguishable from an unknown one.
		type contextKey struct {
			anchor CodeCommentAnchor
			known  bool
		}
		contexts := make(map[contextKey]CodeThread)
		var sole contextKey
		for _, c := range groups[key] {
			if c.OriginalAnchor != nil {
				sole = contextKey{anchor: *c.OriginalAnchor, known: true}
				contexts[sole] = nil
			}
		}
		unambiguous := len(contexts) == 1 && sole.anchor.Validate() == nil
		for _, c := range groups[key] {
			var context contextKey
			if c.OriginalAnchor != nil {
				context = contextKey{anchor: *c.OriginalAnchor, known: true}
			} else if unambiguous {
				context = sole
			}
			contexts[context] = append(contexts[context], c)
		}
		for _, members := range contexts {
			sort.Slice(members, func(i, j int) bool {
				if members[i].CreatedUnix != members[j].CreatedUnix {
					return members[i].CreatedUnix < members[j].CreatedUnix
				}
				return members[i].ID < members[j].ID
			})
			threads = append(threads, members)
		}
	}
	sort.Slice(threads, func(i, j int) bool {
		a, b := threads[i].Root(), threads[j].Root()
		if a.CreatedUnix != b.CreatedUnix {
			return a.CreatedUnix < b.CreatedUnix
		}
		return a.ID < b.ID
	})
	return threads
}

// FindCodeThread returns the conversation holding the named comment ID,
// or nil when no grouped thread contains it.
func FindCodeThread(threads []CodeThread, commentID int64) CodeThread {
	for _, thread := range threads {
		for _, member := range thread {
			if member != nil && member.ID == commentID {
				return thread
			}
		}
	}
	return nil
}

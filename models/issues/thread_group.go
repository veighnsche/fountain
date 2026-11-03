// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues

import (
	"context"
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
	if err := issue.LoadRepo(ctx); err != nil {
		return nil, nil, err
	}
	if err := comments.LoadPosters(ctx); err != nil {
		return nil, nil, err
	}
	if err := comments.LoadResolveDoers(ctx); err != nil {
		return nil, nil, err
	}
	ids := make([]int64, 0, len(comments))
	for _, comment := range comments {
		if comment.ReviewID != 0 {
			ids = append(ids, comment.ReviewID)
		}
	}
	reviews := make(map[int64]*Review)
	if err := db.GetEngine(ctx).In("id", ids).Find(&reviews); err != nil {
		return nil, nil, err
	}
	for _, comment := range comments {
		if re, ok := reviews[comment.ReviewID]; ok && re != nil {
			comment.Review = re
		}
	}
	return comments, reviews, nil
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

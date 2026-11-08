// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"fmt"
	"strconv"
	"strings"
)

// FamilyCollaboration identifies ordinary collaboration writers.
const FamilyCollaboration = "collaboration"

// Collaboration resource labels. Each label names the operation's anchor
// entities with positive integer IDs; the Scope carries the repository.
// Labels with unknowable IDs (creates), multi-entity batches and
// cross-family remaps still order before their effects, but offline
// recovery leaves them fenced.
const (
	// CollabOpCreate marks a create whose row ID is unknowable pre-insert.
	CollabOpCreate = "create"
)

// IssueResource names one issue-row writer: title, content, ref, status,
// deadline, delete, assignee, milestone, labels, pin, lock, project or
// attachment association on the named issue.
func IssueResource(issueID int64, op string) string {
	return fmt.Sprintf("issue/%d/%s", issueID, op)
}

// IssueCreateResource names one issue creation in the named repository.
// The issue ID is unknowable pre-insert, so recovery fences it.
func IssueCreateResource(repoID int64) string {
	return fmt.Sprintf("issue/new/%d", repoID)
}

// CommentResource names one comment writer: update, delete, conversation
// or attachment on the named comment.
func CommentResource(commentID int64, op string) string {
	return fmt.Sprintf("comment/%d/%s", commentID, op)
}

// CommentCreateResource names one comment creation on the named issue.
// The comment ID is unknowable pre-insert, so recovery fences it.
func CommentCreateResource(issueID int64) string {
	return fmt.Sprintf("comment/new/%d", issueID)
}

// DependencyResource names one dependency edge writer between the two
// named issues. The edge may be present or absent; both are valid
// single-transaction end states.
func DependencyResource(issueID, depID int64) string {
	return fmt.Sprintf("dependency/%d/%d", issueID, depID)
}

// PullResource names one PR-metadata writer: retarget, close, update,
// refresh, automerge, manual or edits on the named pull request.
func PullResource(prID int64, op string) string {
	return fmt.Sprintf("pull/%d/%s", prID, op)
}

// PullCreateResource names one PR creation on the named issue. The PR ID
// is unknowable pre-insert, so recovery fences it.
func PullCreateResource(issueID int64) string {
	return fmt.Sprintf("pull/new/%d", issueID)
}

// ReviewResource names one review writer: dismiss on the named review.
func ReviewResource(reviewID int64, op string) string {
	return fmt.Sprintf("review/%d/%s", reviewID, op)
}

// ReviewRequestResource names one user review-request add or remove on
// the named issue for the named reviewer. Requests address reviewers by
// identity, not by review row, so the label carries both IDs.
func ReviewRequestResource(issueID, reviewerID int64) string {
	return fmt.Sprintf("review/%d/request-user/%d", issueID, reviewerID)
}

// ReviewTeamRequestResource names one team review-request add or remove
// on the named issue for the named team.
func ReviewTeamRequestResource(issueID, teamID int64) string {
	return fmt.Sprintf("review/%d/request-team/%d", issueID, teamID)
}

// ReviewSubmitResource names one review submission on the named issue.
// The review ID is unknowable pre-insert, so recovery fences it.
func ReviewSubmitResource(issueID int64) string {
	return fmt.Sprintf("review/new/%d", issueID)
}

// ReviewDeleteResource names one single-transaction review deletion on
// the named issue. A present or absent review row over the intact issue
// are both valid atomic end states.
func ReviewDeleteResource(issueID, reviewID int64) string {
	return fmt.Sprintf("review/%d/%d/delete", issueID, reviewID)
}

// ReactionResource names one reaction writer on the named issue, with
// commentID 0 for an issue-level reaction.
func ReactionResource(issueID, commentID, doerID int64) string {
	return fmt.Sprintf("reaction/%d/%d/%d", issueID, commentID, doerID)
}

// LabelResource names one label-row writer: update on the named label,
// or create on the named repository.
func LabelResource(id int64, op string) string {
	return fmt.Sprintf("label/%d/%s", id, op)
}

// MilestoneResource names one milestone writer: update or status on the
// named milestone, or create/delete on the named repository.
func MilestoneResource(id int64, op string) string {
	return fmt.Sprintf("milestone/%d/%s", id, op)
}

// Attachment operations carried in collaboration resource labels. Issue
// and comment attachments are part of native accepted-input records;
// release assets ride the same attachment row writer, so standalone
// asset operations claim here too (assets attached inside a release
// flow nest under that flow's owner instead).
const (
	// CollabAttachmentCreate uploads one attachment row. The storage
	// UUID is invented inside the write, so offline recovery fences it.
	CollabAttachmentCreate = "create"
	// CollabAttachmentDelete removes one attachment row by ID. The row
	// is attributable both ways.
	CollabAttachmentDelete = "delete"
	// CollabAttachmentUpdate edits one attachment row by ID. The applied
	// values are unknowable, so offline recovery fences it.
	CollabAttachmentUpdate = "update"
)

// AttachmentCreateResource names one attachment upload on the named
// repository. The Scope carries the repository.
func AttachmentCreateResource(repoID int64) string {
	return fmt.Sprintf("attachment/new/%d", repoID)
}

// AttachmentResource names one attachment row update by ID. The Scope
// carries the repository.
func AttachmentResource(attachID int64, op string) string {
	return fmt.Sprintf("attachment/%d/%s", attachID, op)
}

// HistoryResource names one content-history soft-delete on the named
// history row.
func HistoryResource(historyID int64) string {
	return fmt.Sprintf("history/%d/delete", historyID)
}

// CollabBatchResource names one multi-entity collaboration batch:
// orphan-fix, external-remap, push-commit or pr-sweep. Batches order
// before their effects, but recovery fences them: no single-entity
// reconciliation.
func CollabBatchResource(op string) string {
	return "batch/" + op
}

// CollabClaim is one parsed collaboration resource label.
type CollabClaim struct {
	Kind string // issue, comment, dependency, pull, review, reaction, label, milestone, history, attachment or batch
	ID   int64  // anchor entity ID (issue, comment, PR, review, label, milestone or history row)
	ID2  int64  // second entity ID (dependency target, review delete pair)
	Op   string // operation within the kind
}

// ParseCollabResource strictly parses one collaboration resource label.
// Any deviation fences: recovery must never guess which update an owner
// ran. Writers and recovery use these same labels.
func ParseCollabResource(resource string) (CollabClaim, error) {
	var claim CollabClaim
	parts := strings.Split(resource, "/")
	if len(parts) < 2 {
		return claim, fmt.Errorf("unparseable collaboration resource %q", resource)
	}
	positive := func(s string) (int64, bool) {
		id, err := strconv.ParseInt(s, 10, 64)
		return id, err == nil && id > 0
	}
	switch parts[0] {
	case "issue":
		if len(parts) == 3 && parts[1] == "new" {
			id, ok := positive(parts[2])
			if !ok {
				break
			}
			return CollabClaim{Kind: "issue", ID: id, Op: "create"}, nil
		}
		if len(parts) != 3 {
			break
		}
		id, ok := positive(parts[1])
		if !ok {
			break
		}
		switch parts[2] {
		case "title", "content", "ref", "status", "deadline",
			"delete", "assignee", "milestone", "labels", "pin", "lock",
			"project", "attachment":
			claim = CollabClaim{Kind: "issue", ID: id, Op: parts[2]}
			return claim, nil
		}
	case "comment":
		if len(parts) == 3 && parts[1] == "new" {
			id, ok := positive(parts[2])
			if !ok {
				break
			}
			return CollabClaim{Kind: "comment", ID: id, Op: "create"}, nil
		}
		if len(parts) != 3 {
			break
		}
		id, ok := positive(parts[1])
		if !ok {
			break
		}
		switch parts[2] {
		case "update", "delete", "conversation", "attachment":
			return CollabClaim{Kind: "comment", ID: id, Op: parts[2]}, nil
		}
	case "dependency":
		if len(parts) != 3 {
			break
		}
		id, ok := positive(parts[1])
		id2, ok2 := positive(parts[2])
		if !ok || !ok2 {
			break
		}
		return CollabClaim{Kind: "dependency", ID: id, ID2: id2}, nil
	case "pull":
		if len(parts) == 3 && parts[1] == "new" {
			id, ok := positive(parts[2])
			if !ok {
				break
			}
			return CollabClaim{Kind: "pull", ID: id, Op: "create"}, nil
		}
		if len(parts) != 3 {
			break
		}
		id, ok := positive(parts[1])
		if !ok {
			break
		}
		switch parts[2] {
		case "retarget", "close", "update", "refresh", "automerge", "manual", "edits":
			return CollabClaim{Kind: "pull", ID: id, Op: parts[2]}, nil
		}
	case "review":
		if len(parts) == 3 && parts[1] == "new" {
			id, ok := positive(parts[2])
			if !ok {
				break
			}
			return CollabClaim{Kind: "review", ID: id, Op: "submit"}, nil
		}
		if len(parts) == 4 && parts[3] == "delete" {
			id, ok := positive(parts[1])
			id2, ok2 := positive(parts[2])
			if !ok || !ok2 {
				break
			}
			return CollabClaim{Kind: "review", ID: id, ID2: id2, Op: "delete"}, nil
		}
		if len(parts) == 4 && (parts[2] == "request-user" || parts[2] == "request-team") {
			id, ok := positive(parts[1])
			id2, ok2 := positive(parts[3])
			if !ok || !ok2 {
				break
			}
			return CollabClaim{Kind: "review", ID: id, ID2: id2, Op: parts[2]}, nil
		}
		if len(parts) != 3 {
			break
		}
		id, ok := positive(parts[1])
		if !ok {
			break
		}
		if parts[2] == "dismiss" {
			return CollabClaim{Kind: "review", ID: id, Op: parts[2]}, nil
		}
	case "reaction":
		if len(parts) != 4 {
			break
		}
		id, ok := positive(parts[1])
		commentID, err := strconv.ParseInt(parts[2], 10, 64)
		_, ok2 := positive(parts[3])
		if !ok || err != nil || commentID < 0 || !ok2 {
			break
		}
		// The validated doer ID rides in op; recovery reconciles
		// reactions from the issue/comment anchors only.
		return CollabClaim{Kind: "reaction", ID: id, ID2: commentID, Op: parts[3]}, nil
	case "label":
		if len(parts) != 3 {
			break
		}
		id, ok := positive(parts[1])
		if !ok {
			break
		}
		switch parts[2] {
		case "create", "update", "delete":
			return CollabClaim{Kind: "label", ID: id, Op: parts[2]}, nil
		}
	case "milestone":
		if len(parts) != 3 {
			break
		}
		id, ok := positive(parts[1])
		if !ok {
			break
		}
		switch parts[2] {
		case "create", "update", "status", "delete":
			return CollabClaim{Kind: "milestone", ID: id, Op: parts[2]}, nil
		}
	case "history":
		if len(parts) != 3 || parts[2] != "delete" {
			break
		}
		id, ok := positive(parts[1])
		if !ok {
			break
		}
		return CollabClaim{Kind: "history", ID: id, Op: "delete"}, nil
	case "attachment":
		if len(parts) == 3 && parts[1] == "new" {
			id, ok := positive(parts[2])
			if !ok {
				break
			}
			return CollabClaim{Kind: "attachment", ID: id, Op: CollabAttachmentCreate}, nil
		}
		if len(parts) != 3 {
			break
		}
		id, ok := positive(parts[1])
		if !ok {
			break
		}
		switch parts[2] {
		case CollabAttachmentDelete, CollabAttachmentUpdate:
			return CollabClaim{Kind: "attachment", ID: id, Op: parts[2]}, nil
		}
	case "batch":
		if len(parts) != 2 {
			break
		}
		switch parts[1] {
		case "orphan-fix", "external-remap", "push-commit", "pr-sweep":
			return CollabClaim{Kind: "batch", Op: parts[1]}, nil
		}
	}
	return claim, fmt.Errorf("unparseable collaboration resource %q", resource)
}

// SplitOrdinaryResource splits an ordinary owner into its family and
// resource label. Owners read "ord:family/resource/nonce"; the resource
// may itself contain slashes, so only the first and last segments split.
func SplitOrdinaryResource(owner string) (family, resource string, ok bool) {
	rest, found := strings.CutPrefix(owner, "ord:")
	if !found {
		return "", "", false
	}
	family, middle, found := strings.Cut(rest, "/")
	if !found || family == "" {
		return "", "", false
	}
	slash := strings.LastIndex(middle, "/")
	if slash < 0 {
		return "", "", false
	}
	return family, middle[:slash], true
}

// OrdinaryResource extracts the resource label from one ordinary owner of
// the given family. Ordinary owners have the form
// "ord:<family>/<resource>/<16-hex-nonce>"; the resource itself may
// contain slashes, so the nonce is the last segment. A format drift
// fences rather than guessing.
func OrdinaryResource(owner, family string) (string, error) {
	rest, ok := strings.CutPrefix(owner, "ord:"+family+"/")
	if !ok {
		return "", fmt.Errorf("owner %q is not an %s owner", owner, family)
	}
	idx := strings.LastIndex(rest, "/")
	if idx <= 0 {
		return "", fmt.Errorf("owner %q names no parseable resource", owner)
	}
	nonce := rest[idx+1:]
	if len(nonce) != 16 {
		return "", fmt.Errorf("owner %q names no parseable resource", owner)
	}
	for _, c := range nonce {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("owner %q names no parseable resource", owner)
		}
	}
	return rest[:idx], nil
}

// RefWriteResource builds the owner resource string for one direct ref
// write: repository, kind and affected ref.
func RefWriteResource(repoID int64, kind, ref string) string {
	return fmt.Sprintf("%d/%s/%s", repoID, kind, ref)
}

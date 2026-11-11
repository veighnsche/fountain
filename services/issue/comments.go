// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issue

import (
	"context"
	"errors"
	"fmt"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/timeutil"
	notify_service "forgejo.org/services/notify"
)

// CreateRefComment creates a commit reference comment to issue.
func CreateRefComment(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, issue *issues_model.Issue, content, commitSHA string) error {
	// One collaboration writer owns the comment creation before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale.
	return withCollabOwnership(ctx, execcontext.CommentCreateResource(issue.ID), repo.ID, func(ctx context.Context) error {
		return doCreateRefComment(ctx, doer, repo, issue, content, commitSHA)
	})
}

func doCreateRefComment(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, issue *issues_model.Issue, content, commitSHA string) error {
	if len(commitSHA) == 0 {
		return errors.New("cannot create reference with empty commit SHA")
	}

	// Check if same reference from same commit has already existed.
	has, err := db.GetEngine(ctx).Get(&issues_model.Comment{
		Type:      issues_model.CommentTypeCommitRef,
		IssueID:   issue.ID,
		CommitSHA: commitSHA,
	})
	if err != nil {
		return fmt.Errorf("check reference comment: %w", err)
	} else if has {
		return nil
	}

	_, err = issues_model.CreateComment(ctx, &issues_model.CreateCommentOptions{
		Type:      issues_model.CommentTypeCommitRef,
		Doer:      doer,
		Repo:      repo,
		Issue:     issue,
		CommitSHA: commitSHA,
		Content:   content,
	})
	return err
}

// CreateIssueComment creates a plain issue comment.
func CreateIssueComment(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, issue *issues_model.Issue, content string, attachments []string) (comment *issues_model.Comment, err error) {
	// One collaboration writer owns the comment creation before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale.
	err = withCollabOwnership(ctx, execcontext.CommentCreateResource(issue.ID), repo.ID, func(ctx context.Context) error {
		comment, err = doCreateIssueComment(ctx, doer, repo, issue, content, attachments)
		return err
	})
	return comment, err
}

func doCreateIssueComment(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, issue *issues_model.Issue, content string, attachments []string) (*issues_model.Comment, error) {
	// Check if doer is blocked by the poster of the issue or by the owner of the repository.
	if user_model.IsBlockedMultiple(ctx, []int64{issue.PosterID, repo.OwnerID}, doer.ID) {
		return nil, user_model.ErrBlockedByUser
	}

	comment, err := issues_model.CreateComment(ctx, &issues_model.CreateCommentOptions{
		Type:        issues_model.CommentTypeComment,
		Doer:        doer,
		Repo:        repo,
		Issue:       issue,
		Content:     content,
		Attachments: attachments,
	})
	if err != nil {
		return nil, err
	}

	mentions, err := issues_model.FindAndUpdateIssueMentions(ctx, issue, doer, comment.Content)
	if err != nil {
		return nil, err
	}

	notify_service.CreateIssueComment(ctx, doer, repo, issue, comment, mentions)

	return comment, nil
}

// UpdateComment updates information of comment.
func UpdateComment(ctx context.Context, c *issues_model.Comment, contentVersion int, doer *user_model.User, oldContent string) error {
	// One collaboration writer owns the comment update before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale. The comment carries no repository ID, so
	// the scope names none; the comment and issue anchors reconcile it.
	return withCollabOwnership(ctx, execcontext.CommentResource(c.ID, "update"), 0, func(ctx context.Context) error {
		return doUpdateComment(ctx, c, contentVersion, doer, oldContent)
	})
}

func doUpdateComment(ctx context.Context, c *issues_model.Comment, contentVersion int, doer *user_model.User, oldContent string) error {
	if err := c.LoadReview(ctx); err != nil {
		return err
	}
	isPartOfPendingReview := c.Review != nil && c.Review.Type == issues_model.ReviewTypePending

	needsContentHistory := c.Content != oldContent && c.Type.HasContentSupport() && !isPartOfPendingReview
	if needsContentHistory {
		hasContentHistory, err := issues_model.HasIssueContentHistory(ctx, c.IssueID, c.ID)
		if err != nil {
			return err
		}
		if !hasContentHistory {
			if err = issues_model.SaveIssueContentHistory(ctx, c.PosterID, c.IssueID, c.ID,
				c.CreatedUnix, oldContent, true); err != nil {
				return err
			}
		}
	}

	if err := issues_model.UpdateComment(ctx, c, contentVersion, doer); err != nil {
		return err
	}

	if needsContentHistory {
		historyDate := timeutil.TimeStampNow()
		if c.Issue.NoAutoTime {
			historyDate = c.Issue.UpdatedUnix
		}
		err := issues_model.SaveIssueContentHistory(ctx, doer.ID, c.IssueID, c.ID, historyDate, c.Content, false)
		if err != nil {
			return err
		}
	}

	if !isPartOfPendingReview {
		notify_service.UpdateComment(ctx, doer, c, oldContent)
	}

	return nil
}

// DeleteComment deletes the comment
func DeleteComment(ctx context.Context, doer *user_model.User, comment *issues_model.Comment) error {
	// One collaboration writer owns the comment delete before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale. The comment carries no repository ID, so
	// the scope names none; the comment and issue anchors reconcile it.
	return withCollabOwnership(ctx, execcontext.CommentResource(comment.ID, "delete"), 0, func(ctx context.Context) error {
		return doDeleteComment(ctx, doer, comment)
	})
}

func doDeleteComment(ctx context.Context, doer *user_model.User, comment *issues_model.Comment) error {
	err := db.WithTx(ctx, func(ctx context.Context) error {
		reviewID := comment.ReviewID

		err := issues_model.DeleteComment(ctx, comment)
		if err != nil {
			return err
		}

		if comment.Review != nil {
			reviewType := comment.Review.Type
			if reviewType == issues_model.ReviewTypePending {
				found, err := db.GetEngine(ctx).Table("comment").Where("review_id = ?", reviewID).Exist()
				if err != nil {
					return err
				} else if !found {
					_, err := db.GetEngine(ctx).Table("review").Where("id = ?", reviewID).Delete()
					if err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	if err := comment.LoadReview(ctx); err != nil {
		return err
	}
	if comment.Review == nil || comment.Review.Type != issues_model.ReviewTypePending {
		notify_service.DeleteComment(ctx, doer, comment)
	}

	return nil
}

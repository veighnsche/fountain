// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	stdCtx "context"
	"net/http"

	execcontext "forgejo.org/modules/nativeoperation"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	"forgejo.org/models/organization"
	"forgejo.org/modules/base"
	"forgejo.org/modules/label"
	"forgejo.org/modules/log"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/web"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"
	issue_service "forgejo.org/services/issue"
	nativeoperation_service "forgejo.org/services/nativeoperation"
)

const (
	tplLabels base.TplName = "repo/issue/labels"
)

// Labels render issue's labels page
func Labels(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.labels")
	ctx.Data["PageIsIssueList"] = true
	ctx.Data["PageIsLabels"] = true
	ctx.Data["LabelTemplateFiles"] = repo_module.LabelTemplateFiles
	ctx.HTML(http.StatusOK, tplLabels)
}

// InitializeLabels init labels for a repository
func InitializeLabels(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.InitializeLabelsForm)
	if ctx.HasError() {
		ctx.Redirect(ctx.Repo.RepoLink + "/labels")
		return
	}

	// One collaboration writer owns the label initialization before
	// its effects, advancing the native revision so old accepted-input
	// observations go stale.
	repoID := ctx.Repo.Repository.ID
	if err := nativeoperation_service.WithCollaborationOwnership(ctx, execcontext.LabelResource(repoID, "create"), repoID, func(ctx stdCtx.Context) error {
		return repo_module.InitializeLabels(ctx, repoID, form.TemplateName, false)
	}); err != nil {
		if label.IsErrTemplateLoad(err) {
			originalErr := err.(label.ErrTemplateLoad).OriginalError
			ctx.Flash.Error(ctx.Tr("repo.issues.label_templates.fail_to_load_file", form.TemplateName, originalErr))
			ctx.Redirect(ctx.Repo.RepoLink + "/labels")
			return
		}
		ctx.ServerError("InitializeLabels", err)
		return
	}
	ctx.Redirect(ctx.Repo.RepoLink + "/labels")
}

// RetrieveLabels find all the labels of a repository and organization
func RetrieveLabels(ctx *context.Context) {
	labels, err := issues_model.GetLabelsByRepoID(ctx, ctx.Repo.Repository.ID, ctx.FormString("sort"), db.ListOptions{})
	if err != nil {
		ctx.ServerError("RetrieveLabels.GetLabels", err)
		return
	}

	for _, l := range labels {
		l.CalOpenIssues()
	}

	ctx.Data["Labels"] = labels

	if ctx.Repo.Owner.IsOrganization() {
		orgLabels, err := issues_model.GetLabelsByOrgID(ctx, ctx.Repo.Owner.ID, ctx.FormString("sort"), db.ListOptions{})
		if err != nil {
			ctx.ServerError("GetLabelsByOrgID", err)
			return
		}
		for _, l := range orgLabels {
			l.CalOpenOrgIssues(ctx, ctx.Repo.Repository.ID, l.ID)
		}
		ctx.Data["OrgLabels"] = orgLabels

		org, err := organization.GetOrgByName(ctx, ctx.Repo.Owner.LowerName)
		if err != nil {
			ctx.ServerError("GetOrgByName", err)
			return
		}
		if ctx.Doer != nil {
			ctx.Org.IsOwner, err = org.IsOwnedBy(ctx, ctx.Doer.ID)
			if err != nil {
				ctx.ServerError("org.IsOwnedBy", err)
				return
			}
			ctx.Org.OrgLink = org.AsUser().OrganisationLink()
			ctx.Data["IsOrganizationOwner"] = ctx.Org.IsOwner
			ctx.Data["OrganizationLink"] = ctx.Org.OrgLink
		}
	}
	ctx.Data["NumLabels"] = len(labels)
	ctx.Data["SortType"] = ctx.FormString("sort")
}

// NewLabel create new label for repository
func NewLabel(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.CreateLabelForm)
	ctx.Data["Title"] = ctx.Tr("repo.labels")
	ctx.Data["PageIsLabels"] = true

	if ctx.HasError() {
		ctx.Flash.Error(ctx.Data["ErrorMsg"].(string))
		ctx.Redirect(ctx.Repo.RepoLink + "/labels")
		return
	}

	l := &issues_model.Label{
		RepoID:      ctx.Repo.Repository.ID,
		Name:        form.Title,
		Exclusive:   form.Exclusive,
		Description: form.Description,
		Color:       form.Color,
	}
	// One collaboration writer owns the label creation before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale.
	repoID := ctx.Repo.Repository.ID
	if err := nativeoperation_service.WithCollaborationOwnership(ctx, execcontext.LabelResource(repoID, "create"), repoID, func(ctx stdCtx.Context) error {
		return issues_model.NewLabel(ctx, l)
	}); err != nil {
		ctx.ServerError("NewLabel", err)
		return
	}
	ctx.Redirect(ctx.Repo.RepoLink + "/labels")
}

// UpdateLabel update a label's name and color
func UpdateLabel(ctx *context.Context) {
	form := web.GetForm(ctx).(*forms.CreateLabelForm)
	l, err := issues_model.GetLabelInRepoByID(ctx, ctx.Repo.Repository.ID, form.ID)
	if err != nil {
		switch {
		case issues_model.IsErrRepoLabelNotExist(err):
			ctx.Error(http.StatusNotFound)
		default:
			ctx.ServerError("UpdateLabel", err)
		}
		return
	}
	l.Name = form.Title
	l.Exclusive = form.Exclusive
	l.Description = form.Description
	l.Color = form.Color

	l.SetArchived(form.IsArchived)
	// One collaboration writer owns the label change before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale.
	if err := nativeoperation_service.WithCollaborationOwnership(ctx, execcontext.LabelResource(l.ID, "update"), l.RepoID, func(ctx stdCtx.Context) error {
		return issues_model.UpdateLabel(ctx, l)
	}); err != nil {
		ctx.ServerError("UpdateLabel", err)
		return
	}
	ctx.Redirect(ctx.Repo.RepoLink + "/labels")
}

// DeleteLabel delete a label
func DeleteLabel(ctx *context.Context) {
	// One collaboration writer owns the label delete before its
	// effects, advancing the native revision so old accepted-input
	// observations go stale.
	repoID := ctx.Repo.Repository.ID
	labelID := ctx.FormInt64("id")
	if err := nativeoperation_service.WithCollaborationOwnership(ctx, execcontext.LabelResource(labelID, "delete"), repoID, func(ctx stdCtx.Context) error {
		return issues_model.DeleteLabelInRepo(ctx, repoID, labelID)
	}); err != nil {
		if nativeoperation_service.IsBusy(err) {
			ctx.Flash.Error("A native operation is in progress; retry shortly.")
		} else {
			ctx.Flash.Error("DeleteLabel: " + err.Error())
		}
	} else {
		ctx.Flash.Success(ctx.Tr("repo.issues.label_deletion_success"))
	}

	ctx.JSONRedirect(ctx.Repo.RepoLink + "/labels")
}

// UpdateIssueLabel change issue's labels
func UpdateIssueLabel(ctx *context.Context) {
	issues := getActionIssues(ctx)
	if ctx.Written() {
		return
	}

	switch action := ctx.FormString("action"); action {
	case "clear":
		for _, issue := range issues {
			if err := issue_service.ClearLabels(ctx, issue, ctx.Doer); err != nil {
				ctx.ServerError("ClearLabels", err)
				return
			}
		}
	case "attach", "detach", "toggle", "toggle-alt":
		label, err := issues_model.GetLabelByID(ctx, ctx.FormInt64("id"))
		if err != nil {
			if issues_model.IsErrRepoLabelNotExist(err) {
				ctx.Error(http.StatusNotFound, "GetLabelByID")
			} else {
				ctx.ServerError("GetLabelByID", err)
			}
			return
		}

		if action == "toggle" {
			// detach if any issues already have label, otherwise attach
			action = "attach"
			if label.ExclusiveScope() == "" {
				for _, issue := range issues {
					if issues_model.HasIssueLabel(ctx, issue.ID, label.ID) {
						action = "detach"
						break
					}
				}
			}
		} else if action == "toggle-alt" {
			// always detach with alt key pressed, to be able to remove
			// scoped labels
			action = "detach"
		}

		if action == "attach" {
			for _, issue := range issues {
				if err = issue_service.AddLabel(ctx, issue, ctx.Doer, label); err != nil {
					ctx.ServerError("AddLabel", err)
					return
				}
			}
		} else {
			for _, issue := range issues {
				if err = issue_service.RemoveLabel(ctx, issue, ctx.Doer, label); err != nil {
					ctx.ServerError("RemoveLabel", err)
					return
				}
			}
		}
	default:
		log.Warn("Unrecognized action: %s", action)
		ctx.Error(http.StatusInternalServerError)
		return
	}

	ctx.JSONOK()
}

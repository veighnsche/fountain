// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package attachment

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/storage"
	"forgejo.org/modules/util"
	"forgejo.org/modules/validation"
	"forgejo.org/services/context/upload"
	nativeoperation_service "forgejo.org/services/nativeoperation"

	"github.com/google/uuid"
)

// NewAttachment creates a new attachment object, but do not verify.
func NewAttachment(ctx context.Context, attach *repo_model.Attachment, file io.Reader, size int64) (*repo_model.Attachment, error) {
	if attach.RepoID == 0 {
		return nil, fmt.Errorf("attachment %s should belong to a repository", attach.Name)
	}
	if attach.UploaderID == 0 {
		return nil, fmt.Errorf("attachment %s should have a uploader", attach.Name)
	}

	// One attachment upload owns the storage write and row insert
	// before its effects. Callers inside an enclosing owner (release
	// flows attach under their own owner) reuse that execution.
	err := nativeoperation_service.WithCollaborationOwnership(ctx, execcontext.AttachmentCreateResource(attach.RepoID), attach.RepoID, func(ctx context.Context) error {
		return db.WithTx(ctx, func(ctx context.Context) error {
			attach.UUID = uuid.New().String()
			size, err := storage.Attachments.Save(attach.RelativePath(), file, size)
			if err != nil {
				return fmt.Errorf("Create: %w", err)
			}
			attach.Size = size

			eng := db.GetEngine(ctx)
			if attach.NoAutoTime {
				eng.NoAutoTime()
			}
			_, err = eng.Insert(attach)
			return err
		})
	})

	return attach, err
}

func NewExternalAttachment(ctx context.Context, attach *repo_model.Attachment) (*repo_model.Attachment, error) {
	if attach.RepoID == 0 {
		return nil, fmt.Errorf("attachment %s should belong to a repository", attach.Name)
	}
	if attach.UploaderID == 0 {
		return nil, fmt.Errorf("attachment %s should have a uploader", attach.Name)
	}
	if attach.ExternalURL == "" {
		return nil, fmt.Errorf("attachment %s should have a external url", attach.Name)
	}
	if !validation.IsValidReleaseAssetURL(attach.ExternalURL) {
		return nil, repo_model.ErrInvalidExternalURL{ExternalURL: attach.ExternalURL}
	}

	// One attachment upload owns the row insert before its effects.
	// Callers inside an enclosing owner (release flows attach under
	// their own owner) reuse that execution.
	err := nativeoperation_service.WithCollaborationOwnership(ctx, execcontext.AttachmentCreateResource(attach.RepoID), attach.RepoID, func(ctx context.Context) error {
		attach.UUID = uuid.New().String()

		eng := db.GetEngine(ctx)
		if attach.NoAutoTime {
			eng.NoAutoTime()
		}
		_, err := eng.Insert(attach)
		return err
	})

	return attach, err
}

// UploadAttachment upload new attachment into storage and update database
func UploadAttachment(ctx context.Context, file io.Reader, allowedTypes string, fileSize int64, attach *repo_model.Attachment) (*repo_model.Attachment, error) {
	buf := make([]byte, 1024)
	n, _ := util.ReadAtMost(file, buf)
	buf = buf[:n]

	if err := upload.Verify(buf, attach.Name, allowedTypes); err != nil {
		return nil, err
	}

	return NewAttachment(ctx, attach, io.MultiReader(bytes.NewReader(buf), file), fileSize)
}

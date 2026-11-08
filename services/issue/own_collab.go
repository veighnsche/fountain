// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package issue

import (
	"context"
	"encoding/json" //nolint:depguard // Native protocol and persisted JSON require standard-library semantics.
	"path/filepath"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"
	"forgejo.org/services/nativeownership"
)

const familyCollaboration = execcontext.FamilyCollaboration

// collabScope mirrors the service Scope JSON fields the collaboration
// writers record. Unknown fields in either direction are ignored by
// encoding/json, so service-side additions never break this projection.
type collabScope struct {
	Kind         string `json:"kind"`
	Family       string `json:"family,omitempty"`
	RepositoryID int64  `json:"repository_id"`
}

// withCollabOwnership claims the idle reservation for one issue-service
// collaboration writer, advancing the revision before its effects, and
// releases it after the writer returns. A busy reservation refuses before
// any effect. Nested calls reuse the enclosing ownership instead of
// claiming again, so writers driven under an existing execution
// (conditional operations, bound hook callbacks, enclosing service
// claims) never claim twice.
func withCollabOwnership(ctx context.Context, resource string, repositoryID int64, fn func(ctx context.Context) error) error {
	encoded, err := json.Marshal(collabScope{
		Kind:         nativeoperation_model.OwnerOrdinary,
		Family:       familyCollaboration,
		RepositoryID: repositoryID,
	})
	if err != nil {
		return err
	}
	return nativeownership.WithOrdinary(ctx, filepath.Join(setting.AppDataPath, "nativeop-exec"), familyCollaboration, resource, string(encoded), fn)
}

// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"

	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"
)

func requireHumanPrimaryOwner(ctx context.Context, revision int64) error {
	held, err := nativeoperation_model.ReadReservation(ctx)
	if err != nil {
		return err
	}
	execution := execcontext.FromContext(ctx)
	if execution == nil || held.Owner == "" || execution.Owner != held.Owner || execution.Generation != held.Generation {
		return nativeoperation_model.ErrBusy
	}
	if held.Revision < 1 || revision != held.Revision-1 {
		return nativeoperation_model.ErrStaleRevision
	}
	return nil
}

func admitHumanPrimary(ctx context.Context, admit HumanReviewAdmission) error {
	if ctx.Err() != nil {
		return ErrAuthorityLost
	}
	if err := admit(ctx); err != nil {
		if errors.Is(err, nativeoperation_model.ErrOperationExpired) {
			return err
		}
		return ErrAuthorityLost
	}
	return nil
}

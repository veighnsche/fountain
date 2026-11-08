// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package desktopgrant

import (
	"context"
	"errors"

	"forgejo.org/models/db"
	"forgejo.org/modules/timeutil"
)

var (
	ErrNotFound = errors.New("desktop grant not found")
	ErrConflict = errors.New("desktop enrollment identity already used")
)

// Grant binds a public OAuth authorization to one repository/runtime. Withdrawal
// is retained as a tombstone; replay cannot reactivate an earlier enrollment.
type Grant struct {
	ID           string             `xorm:"pk VARCHAR(32)"`
	ActorID      int64              `xorm:"NOT NULL INDEX"`
	OAuthGrantID int64              `xorm:"'oauth_grant_id' NOT NULL INDEX"`
	RepositoryID int64              `xorm:"NOT NULL INDEX"`
	RuntimeID    string             `xorm:"VARCHAR(32) NOT NULL"`
	ProofSHA256  string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	Revoked      bool               `xorm:"NOT NULL DEFAULT FALSE"`
	CreatedUnix  timeutil.TimeStamp `xorm:"created"`
}

func (*Grant) TableName() string { return "native_desktop_grant" }

func init() { db.RegisterModel(new(Grant)) }

func Get(ctx context.Context, id string) (*Grant, error) {
	grant := &Grant{ID: id}
	found, err := db.GetEngine(ctx).Get(grant)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	return grant, nil
}

func sameScope(left, right *Grant) bool {
	return left.ActorID == right.ActorID && left.OAuthGrantID == right.OAuthGrantID && left.RepositoryID == right.RepositoryID && left.RuntimeID == right.RuntimeID && left.ProofSHA256 == right.ProofSHA256
}

// Enroll retains the original command identity, including after withdrawal.
func Enroll(ctx context.Context, requested *Grant) (*Grant, bool, error) {
	prior, err := Get(ctx, requested.ID)
	if err == nil {
		if !sameScope(prior, requested) {
			return nil, false, ErrConflict
		}
		return prior, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	if err := db.Insert(ctx, requested); err != nil {
		// A concurrent identical command may have committed first.
		prior, readErr := Get(ctx, requested.ID)
		if readErr == nil {
			if !sameScope(prior, requested) {
				return nil, false, ErrConflict
			}
			return prior, false, nil
		}
		return nil, false, err
	}
	return requested, true, nil
}

func Revoke(ctx context.Context, grant *Grant) error {
	_, err := db.GetEngine(ctx).ID(grant.ID).Where("actor_id=? AND oauth_grant_id=? AND repository_id=?", grant.ActorID, grant.OAuthGrantID, grant.RepositoryID).
		Cols("revoked").Update(&Grant{Revoked: true})
	return err
}

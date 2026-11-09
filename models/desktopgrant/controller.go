// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package desktopgrant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	sdk "forgejo.org/extension-sdk"
	"forgejo.org/models/db"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
)

const (
	ControllerDesktop   = "desktop"
	ControllerAppliance = "appliance"
	ControllerActive    = "active"
	ControllerRetiring  = "retiring"
	ControllerInactive  = "inactive"
)

var ErrController = errors.New("runtime controller is unavailable, unsettled or changed")

// Placement is the central repository controller, independently of the global
// short-lived native mutation reservation. Native operation receipts remain in
// the existing operation journal; this row is only the current selection.
type Placement struct {
	RepositoryID int64              `xorm:"pk"`
	RuntimeID    string             `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"`
	OwnerKind    string             `xorm:"VARCHAR(16) NOT NULL DEFAULT ''"`
	OwnerID      string             `xorm:"VARCHAR(36) NOT NULL DEFAULT ''"`
	Generation   int64              `xorm:"NOT NULL DEFAULT 0"`
	Revision     string             `xorm:"VARCHAR(32) NOT NULL DEFAULT ''"`
	State        string             `xorm:"VARCHAR(16) NOT NULL DEFAULT 'inactive'"`
	SelectedBy   int64              `xorm:"NOT NULL DEFAULT 0"`
	UpdatedUnix  timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

func (*Placement) TableName() string { return "native_runtime_placement" }
func init()                          { db.RegisterModel(new(Placement)) }

func (p *Placement) Projection() sdk.RuntimePlacement {
	return sdk.RuntimePlacement{RepositoryID: fmt.Sprint(p.RepositoryID), RuntimeID: p.RuntimeID, OwnerKind: p.OwnerKind,
		OwnerID: p.OwnerID, Generation: p.Generation, Revision: p.Revision, State: p.State}
}

func ReadPlacement(ctx context.Context, repository int64) (*Placement, error) {
	p := new(Placement)
	found, err := db.GetEngine(ctx).ID(repository).NoAutoCondition().Get(p)
	if err != nil {
		return nil, err
	}
	if !found {
		return &Placement{RepositoryID: repository, State: ControllerInactive}, nil
	}
	return p, nil
}

// WithPlacement serializes controller changes, dispatch claims, grant revocation
// and native commit admission on this repository alone. The no-change UPDATE
// holds its row lock through an enclosing native admission transaction too.
func WithPlacement(ctx context.Context, repository int64, use func(context.Context, *Placement) error) error {
	if repository <= 0 {
		return ErrController
	}
	return db.WithTx(ctx, func(ctx context.Context) error {
		conflict := " ON CONFLICT (repository_id) DO NOTHING"
		if setting.Database.Type.IsMySQL() {
			conflict = " ON DUPLICATE KEY UPDATE repository_id=repository_id"
		}
		query := fmt.Sprintf("INSERT INTO `%s` (repository_id,runtime_id,owner_kind,owner_id,generation,revision,state,selected_by,updated_unix) VALUES (?,'','','',0,'','inactive',0,?)%s", db.TableName(new(Placement)), conflict)
		if _, err := db.GetEngine(ctx).Exec(query, repository, timeutil.TimeStampNow()); err != nil {
			return err
		}
		if _, err := db.GetEngine(ctx).Exec(fmt.Sprintf("UPDATE `%s` SET generation=generation WHERE repository_id=?", db.TableName(new(Placement))), repository); err != nil {
			return err
		}
		p, err := ReadPlacement(ctx, repository)
		if err != nil {
			return err
		}
		return use(ctx, p)
	})
}

// SavePlacement is called only inside WithPlacement after native authority,
// previous revision and retiring-work checks. A selection never authorizes spend.
func SavePlacement(ctx context.Context, p *Placement) error {
	if p.RepositoryID <= 0 || p.Generation <= 0 || p.SelectedBy <= 0 || len(p.RuntimeID) != 32 ||
		(p.OwnerKind != ControllerDesktop && p.OwnerKind != ControllerAppliance) || p.OwnerID == "" ||
		(p.State != ControllerActive && p.State != ControllerRetiring && p.State != ControllerInactive) {
		return ErrController
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	p.Revision = hex.EncodeToString(random[:])
	p.UpdatedUnix = timeutil.TimeStampNow()
	_, err := db.GetEngine(ctx).ID(p.RepositoryID).NoAutoCondition().AllCols().Update(p)
	return err
}

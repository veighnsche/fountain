// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"forgejo.org/models/db"
	execcontext "forgejo.org/modules/nativeoperation"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
)

// Reservation owner kinds.
const (
	OwnerConditional = execcontext.OwnerConditional
	OwnerOrdinary    = execcontext.OwnerOrdinary
)

// Reservation is the single exclusive native-mutation reservation (row id 1)
// with the native-state revision. Owner is empty when idle; otherwise it
// identifies the holding operation or ordinary writer. There is no expiring
// lease, automatic takeover or replay: only the recorded completion path or
// the offline operator recovery (a later task) releases a held owner.
type Reservation struct {
	ID          int64              `xorm:"pk"`
	Revision    int64              `xorm:"NOT NULL"`
	Owner       string             `xorm:"TEXT NOT NULL"`
	Generation  int64              `xorm:"NOT NULL DEFAULT 0"`
	OwnerKind   string             `xorm:"VARCHAR(16) NOT NULL DEFAULT ''"`
	Verifier    string             `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	Scope       string             `xorm:"TEXT NOT NULL"`
	UpdatedUnix timeutil.TimeStamp `xorm:"updated NOT NULL"`
}

var (
	// ErrBusy is returned when the reservation is held by another owner.
	ErrBusy = execcontext.ErrBusy
	// ErrStaleRevision is returned when a conditional claim's expected
	// revision no longer equals the current idle revision.
	ErrStaleRevision = errors.New("stale native revision")
	// ErrAdmissionLost is returned when an admission attempt finds the
	// operation revoked, already admitted or no longer pending.
	ErrAdmissionLost = errors.New("operation admission lost")
	// ErrWrongOwner is returned when a release or admission names an owner
	// that does not hold the reservation.
	ErrWrongOwner = errors.New("wrong reservation owner")
	// ErrDuplicateOperation is returned when an insert meets an existing
	// operation row; the caller must reread and reconcile it.
	ErrDuplicateOperation = errors.New("operation already recorded")
	// ErrInhibited is returned when the offline operator recovery holds the
	// writer domain: new ownership claims refuse until the operator lifts
	// inhibition after reconciliation. It is retryable like ErrBusy and
	// never releases or transfers an existing owner.
	ErrInhibited = execcontext.ErrInhibited
	// ErrOperationExpired is returned when a waiting conditional claim finds
	// the operation past its admission deadline. The caller records the
	// terminal refusal; nothing is claimed.
	ErrOperationExpired = errors.New("operation expired before admission")
)

// OfflineMarkerPath is the host-private file whose presence inhibits new
// ownership claims during offline operator recovery. The deployment's stop
// procedure creates it after every native writer has stopped; the operator
// removes it after reconciliation before restarting writers. It is a
// deployment control, not a lease: it grants no ownership and releases none.
func OfflineMarkerPath() string {
	return filepath.Join(setting.AppDataPath, "nativeop-offline")
}

// OfflineInhibited reports whether offline recovery currently inhibits claims.
func OfflineInhibited() bool {
	info, err := os.Stat(OfflineMarkerPath())
	return err == nil && !info.IsDir()
}

// RequireHeldOwnership fences nested participating writers while another
// owner holds the reservation: with no enclosing execution, or one naming a
// different owner/generation, it returns ErrBusy before any effect. An idle
// reservation allows the call so unintegrated writers keep working until
// their owning task claims outer ownership; that transitional allowance
// closes as writer coverage completes and must not be mistaken for approval
// of a new unowned path.
func RequireHeldOwnership(ctx context.Context) error {
	reservation, err := ReadReservation(ctx)
	if err != nil {
		return err
	}
	if reservation.Owner == "" {
		return nil
	}
	exec := execcontext.FromContext(ctx)
	if exec == nil || exec.Owner != reservation.Owner || exec.Generation != reservation.Generation {
		return ErrBusy
	}
	return nil
}

// ReadReservation returns the current revision and owner, creating the idle
// row on first use.
func ReadReservation(ctx context.Context) (*Reservation, error) {
	reservation := new(Reservation)
	has, err := db.GetEngine(ctx).ID(1).NoAutoCondition().Get(reservation)
	if err != nil {
		return nil, err
	}
	if has {
		return reservation, nil
	}
	// A duplicate INSERT aborts a PostgreSQL caller transaction, so catching
	// its error and rereading cannot recover a concurrent first read. Preserve
	// the winning row without changing revision, ownership or generation.
	conflict := " ON CONFLICT (id) DO NOTHING"
	if setting.Database.Type.IsMySQL() {
		conflict = " ON DUPLICATE KEY UPDATE id=id"
	}
	query := fmt.Sprintf("INSERT INTO `%s` (id,revision,owner,generation,owner_kind,verifier,scope,updated_unix) VALUES (1,1,'',0,'','','',?)%s", db.TableName(new(Reservation)), conflict)
	if _, err := db.GetEngine(ctx).Exec(query, timeutil.TimeStampNow()); err != nil {
		return nil, err
	}
	reservation = new(Reservation)
	has, err = db.GetEngine(ctx).ID(1).NoAutoCondition().Get(reservation)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, errors.New("native reservation initialization was not observed")
	}
	return reservation, nil
}

// LookupOperation returns the recorded operation, or nil when not observed.
//
//nolint:nilnil // The optional lookup reports absence without an error.
func LookupOperation(ctx context.Context, installationID, operationID string) (*Operation, error) {
	op := new(Operation)
	has, err := db.GetEngine(ctx).Where("installation_id=? AND operation_id=?", installationID, operationID).NoAutoCondition().Get(op)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, nil
	}
	return op, nil
}

// InsertTombstone persists a cancellation for an operation whose submission
// has not arrived. A delayed submission under the same ID cannot recreate
// authority. When the operation is already recorded, the existing row wins
// and is returned with ErrDuplicateOperation.
func InsertTombstone(ctx context.Context, installationID, operationID string) (*Operation, error) {
	op := &Operation{
		InstallationID: installationID,
		OperationID:    operationID,
		Revoked:        true,
		EffectState:    EffectNotCommitted,
		Reason:         ReasonCancelledBeforeSubmit,
		Cancellation:   CancellationCancelled,
	}
	if err := db.Insert(ctx, op); err != nil {
		existing, getErr := LookupOperation(ctx, installationID, operationID)
		if getErr != nil {
			return nil, getErr
		}
		if existing != nil {
			return existing, ErrDuplicateOperation
		}
		return nil, err
	}
	return op, nil
}

// RevokeOperation marks a recorded operation revoked. Terminal rows keep
// their outcome; the caller interprets committed as too_late.
func RevokeOperation(ctx context.Context, installationID, operationID string) (*Operation, error) {
	if _, err := db.GetEngine(ctx).Where("installation_id=? AND operation_id=?", installationID, operationID).Cols("revoked").Update(&Operation{Revoked: true}); err != nil {
		return nil, err
	}
	return LookupOperation(ctx, installationID, operationID)
}

// claimIdleReservation atomically moves the idle reservation at
// expectedRevision to owner, advancing revision and generation. The UPDATE
// predicate is the linearization point: under PostgreSQL READ COMMITTED a
// concurrent winner's commit makes a loser's UPDATE match zero rows, so
// exactly one claimant wins. On success it returns the claimed row reread in
// the same transaction. On a lost race it reports the current state: ErrBusy
// when held, ErrStaleRevision when idle past the expected revision; ordinary
// claims report ErrBusy instead of stale so the caller retries at the new
// revision. The reservation row must already exist; claims never insert it
// inside the transaction, so a duplicate key can never abort the transaction
// on PostgreSQL.
func claimIdleReservation(ctx context.Context, expectedRevision int64, owner, ownerKind, scopeJSON, verifier string, ordinary bool) (*Reservation, error) {
	affected, err := db.GetEngine(ctx).Where("id=1 AND owner=? AND revision=?", "", expectedRevision).
		Incr("revision").Incr("generation").
		Cols("owner", "owner_kind", "verifier", "scope").
		Update(&Reservation{Owner: owner, OwnerKind: ownerKind, Verifier: verifier, Scope: scopeJSON})
	if err != nil {
		return nil, err
	}
	claimed := new(Reservation)
	has, err := db.GetEngine(ctx).ID(1).NoAutoCondition().Get(claimed)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, errors.New("native mutation reservation is missing")
	}
	if affected == 0 {
		if claimed.Owner != "" {
			return nil, ErrBusy
		}
		if !ordinary && claimed.Revision != expectedRevision {
			return nil, ErrStaleRevision
		}
		return nil, ErrBusy
	}
	return claimed, nil
}

// readClaimReservation reads the reservation row for a claim transaction.
// The row is created outside the transaction by ReadReservation, so a
// missing row here is unexpected and fails the claim.
func readClaimReservation(ctx context.Context) (*Reservation, error) {
	reservation := new(Reservation)
	has, err := db.GetEngine(ctx).ID(1).NoAutoCondition().Get(reservation)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, errors.New("native mutation reservation is missing")
	}
	return reservation, nil
}

// ClaimConditional durably records a pending conditional operation and claims
// the idle reservation for it in one transaction, advancing the revision.
// owner identifies the holding operation, scopeJSON its permitted effect and
// verifier its execution proof. Busy, inhibited and stale claims record nothing.
func ClaimConditional(ctx context.Context, op *Operation, owner, scopeJSON, verifier string) (*Reservation, error) {
	if OfflineInhibited() {
		return nil, ErrInhibited
	}
	if _, err := ReadReservation(ctx); err != nil {
		return nil, err
	}
	var claimed *Reservation
	err := db.WithTx(ctx, func(ctx context.Context) error {
		reservation, err := readClaimReservation(ctx)
		if err != nil {
			return err
		}
		if reservation.Owner != "" {
			return ErrBusy
		}
		if reservation.Revision != op.ExpectedNativeRevision {
			return ErrStaleRevision
		}
		op.Submitted = true
		op.EffectState = EffectPending
		if err := db.Insert(ctx, op); err != nil {
			return err
		}
		claimedRow, err := claimIdleReservation(ctx, op.ExpectedNativeRevision, owner, OwnerConditional, scopeJSON, verifier, false)
		if err != nil {
			return err
		}
		claimed = claimedRow
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// InsertWaitingOperation durably registers a pending conditional operation
// that launches its native writer later, such as a publish registration
// awaiting its receive. Registration occupies no reservation and performs no
// revision check; the later claim enforces revision, cancellation and
// deadline atomically. When the ID is already recorded, the existing row
// wins and is returned with ErrDuplicateOperation.
func InsertWaitingOperation(ctx context.Context, op *Operation) (*Operation, error) {
	op.Submitted = true
	op.EffectState = EffectPending
	if err := db.Insert(ctx, op); err != nil {
		existing, getErr := LookupOperation(ctx, op.InstallationID, op.OperationID)
		if getErr != nil {
			return nil, getErr
		}
		if existing != nil {
			return existing, ErrDuplicateOperation
		}
		return nil, err
	}
	return op, nil
}

// ClaimPublishReceive atomically moves one waiting conditional operation to
// its started execution: the operation must still be pending, unrevoked and
// unexpired, and the reservation must be idle at the operation's expected
// revision. owner identifies the holding operation, scopeJSON its permitted
// effect and verifier its execution proof. Busy, inhibited, stale, revoked
// and expired claims record nothing; the caller maps them to a terminal
// refusal or a retryable error without launching a receiver.
func ClaimPublishReceive(ctx context.Context, installationID, operationID, owner, scopeJSON, verifier string, nowUnix int64) (*Operation, *Reservation, error) {
	if OfflineInhibited() {
		return nil, nil, ErrInhibited
	}
	if _, err := ReadReservation(ctx); err != nil {
		return nil, nil, err
	}
	var claimedOp *Operation
	var claimed *Reservation
	err := db.WithTx(ctx, func(ctx context.Context) error {
		op, err := LookupOperation(ctx, installationID, operationID)
		if err != nil {
			return err
		}
		if op == nil || !op.Submitted || op.EffectState != EffectPending {
			return ErrAdmissionLost
		}
		if op.Revoked || op.Admitted {
			return ErrAdmissionLost
		}
		if op.NotAfter <= nowUnix {
			return ErrOperationExpired
		}
		reservation, err := readClaimReservation(ctx)
		if err != nil {
			return err
		}
		if reservation.Owner != "" {
			return ErrBusy
		}
		if reservation.Revision != op.ExpectedNativeRevision {
			return ErrStaleRevision
		}
		claimedRow, err := claimIdleReservation(ctx, op.ExpectedNativeRevision, owner, OwnerConditional, scopeJSON, verifier, false)
		if err != nil {
			return err
		}
		claimedOp = op
		claimed = claimedRow
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return claimedOp, claimed, nil
}

// InsertRefusedOperation records a terminally refused submission that took no
// ownership, such as a stale revision. When the ID is already recorded, the
// existing row wins and is returned with ErrDuplicateOperation.
func InsertRefusedOperation(ctx context.Context, op *Operation) (*Operation, error) {
	op.Submitted = true
	if err := db.Insert(ctx, op); err != nil {
		existing, getErr := LookupOperation(ctx, op.InstallationID, op.OperationID)
		if getErr != nil {
			return nil, getErr
		}
		if existing != nil {
			return existing, ErrDuplicateOperation
		}
		return nil, err
	}
	return op, nil
}

// RecordAdmissionAttempt orders one prepared-commit admission against
// cancellation in a single transaction. When admit is true the operation must
// still be pending, unrevoked and unadmitted under the given owner. A lost
// attempt commits the ordering truth (cancelled before admission, or
// duplicate admission) and reports admitted=false; only database failures,
// a missing or non-pending operation, or a wrong owner return an error.
// The transaction never returns an error after writing: WithTx rolls back
// on error, which would discard the recorded ordering.
func RecordAdmissionAttempt(ctx context.Context, installationID, operationID, owner string, admit bool, reason string) (recorded *Operation, admitted bool, err error) {
	err = db.WithTx(ctx, func(ctx context.Context) error {
		op, err := LookupOperation(ctx, installationID, operationID)
		if err != nil {
			return err
		}
		if op == nil || !op.Submitted || op.EffectState != EffectPending {
			return ErrAdmissionLost
		}
		reservation, err := ReadReservation(ctx)
		if err != nil {
			return err
		}
		if reservation.Owner != owner {
			return ErrWrongOwner
		}
		if admit && !op.Revoked && !op.Admitted {
			// The transition, rather than the earlier read, decides which
			// transaction wins against cancellation and other admissions.
			candidate := &Operation{Admitted: true}
			affected, err := db.GetEngine(ctx).
				Where("id=? AND submitted=? AND effect_state=? AND revoked=? AND admitted=?", op.ID, true, EffectPending, false, false).
				Cols("admitted", "reason").Update(candidate)
			if err != nil {
				return err
			}
			if affected == 1 {
				op.Admitted, op.Reason = true, ""
				recorded, admitted = op, true
				return nil
			}
			op, err = LookupOperation(ctx, installationID, operationID)
			if err != nil {
				return err
			}
			if op == nil || !op.Submitted || op.EffectState != EffectPending {
				return ErrAdmissionLost
			}
		}
		if op.Revoked || op.Admitted {
			// The lost attempt persists the ordering truth: a revoked
			// operation was cancelled before admission and an admitted
			// one cannot admit twice.
			derived := reason
			if op.Admitted {
				derived = ReasonDuplicateAdmission
			} else if op.Revoked {
				derived = ReasonCancelledBeforeAdmission
			}
			if op.Reason == "" && derived != "" {
				op.Reason = derived
				if _, err := db.GetEngine(ctx).
					Where("id=? AND effect_state=? AND reason=?", op.ID, EffectPending, "").
					Cols("reason").Update(op); err != nil {
					return err
				}
				op, err = LookupOperation(ctx, installationID, operationID)
				if err != nil {
					return err
				}
			}
			recorded = op
			return nil
		}
		if !admit {
			op.Reason = reason
			if _, err := db.GetEngine(ctx).ID(op.ID).Cols("reason").Update(op); err != nil {
				return err
			}
			recorded = op
			return nil
		}
		return ErrAdmissionLost
	})
	if err != nil {
		return nil, false, err
	}
	return recorded, admitted, nil
}

// TerminalOutcome carries the reconciled result of one operation.
type TerminalOutcome struct {
	EffectState  string
	Reason       string
	Cancellation string
	Completion   string
	Receipt      string
}

// SetTerminal records the reconciled outcome and, when releaseOwner names the
// current owner, releases the reservation in the same transaction. An empty
// releaseOwner retains the fence for an unresolved effect.
func SetTerminal(ctx context.Context, installationID, operationID string, outcome TerminalOutcome, releaseOwner string) (*Operation, error) {
	var recorded *Operation
	err := db.WithTx(ctx, func(ctx context.Context) error {
		op, err := LookupOperation(ctx, installationID, operationID)
		if err != nil {
			return err
		}
		if op == nil {
			return errors.New("operation is not recorded")
		}
		if op.IsTerminal() {
			recorded = op
			return nil
		}
		op.EffectState = outcome.EffectState
		op.Reason = outcome.Reason
		op.Cancellation = outcome.Cancellation
		op.Completion = outcome.Completion
		op.Receipt = outcome.Receipt
		if _, err := db.GetEngine(ctx).ID(op.ID).Cols("effect_state", "reason", "cancellation", "completion", "receipt").Update(op); err != nil {
			return err
		}
		if releaseOwner != "" {
			affected, err := db.GetEngine(ctx).Where("id=1 AND owner=?", releaseOwner).Cols("owner", "owner_kind", "verifier", "scope").Update(&Reservation{})
			if err != nil {
				return err
			}
			if affected == 0 {
				return ErrWrongOwner
			}
		}
		recorded = op
		return nil
	})
	if err != nil {
		return recorded, err
	}
	return recorded, nil
}

// ClaimOrdinary claims the idle reservation for one ordinary native writer,
// advancing the revision before its effects. Ordinary writers carry no
// expected revision; they serialize and invalidate conditional intents bound
// to the old revision.
func ClaimOrdinary(ctx context.Context, owner, scopeJSON, verifier string) (*Reservation, error) {
	if OfflineInhibited() {
		return nil, ErrInhibited
	}
	if _, err := ReadReservation(ctx); err != nil {
		return nil, err
	}
	var claimed *Reservation
	err := db.WithTx(ctx, func(ctx context.Context) error {
		reservation, err := readClaimReservation(ctx)
		if err != nil {
			return err
		}
		if reservation.Owner != "" {
			return ErrBusy
		}
		claimedRow, err := claimIdleReservation(ctx, reservation.Revision, owner, OwnerOrdinary, scopeJSON, verifier, true)
		if err != nil {
			return err
		}
		claimed = claimedRow
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// ReleaseOwner releases the reservation held by owner. Only the exact owner
// can release; anything else is ErrWrongOwner.
func ReleaseOwner(ctx context.Context, owner string) error {
	affected, err := db.GetEngine(ctx).Where("id=1 AND owner=?", owner).Cols("owner", "owner_kind", "verifier", "scope").Update(&Reservation{})
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrWrongOwner
	}
	return nil
}

// ReleaseExactOwner releases the reservation only when both the owner string
// and its fencing generation match the current holder. Offline recovery uses
// it after establishing the known effect under whole-domain quiescence; a
// wrong owner or a stale generation refuses with ErrWrongOwner and the fence
// stays held. The native revision is never reset.
func ReleaseExactOwner(ctx context.Context, owner string, generation int64) error {
	if owner == "" || generation <= 0 {
		return ErrWrongOwner
	}
	affected, err := db.GetEngine(ctx).Where("id=1 AND owner=? AND generation=?", owner, generation).Cols("owner", "owner_kind", "verifier", "scope").Update(&Reservation{})
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrWrongOwner
	}
	return nil
}

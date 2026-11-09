// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package nativeoperation_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forgejo.org/models/db"
	nativeoperation_model "forgejo.org/models/nativeoperation"
	execcontext "forgejo.org/modules/nativeoperation"

	"code.forgejo.org/xorm/xorm"
	"code.forgejo.org/xorm/xorm/names"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for the disposable PostgreSQL fixture
)

// useDisposablePostgres connects the real claim code to a disposable
// PostgreSQL and swaps it in as the default engine for one test, restoring
// the shared unit-test engine afterwards. It skips without
// FORGEJO_TEST_PG_DSN, so ordinary runs stay on SQLite.
func useDisposablePostgres(t *testing.T) *xorm.Engine {
	t.Helper()
	dsn := os.Getenv("FORGEJO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("FORGEJO_TEST_PG_DSN is not set; needs a disposable PostgreSQL")
	}
	engine, err := xorm.NewEngine("pgx", dsn)
	require.NoError(t, err)
	engine.SetMapper(names.GonicMapper{})
	t.Cleanup(func() { _ = engine.Close() })
	require.NoError(t, engine.Sync2(new(nativeoperation_model.Operation), new(nativeoperation_model.Reservation)))
	_, err = engine.Exec("DELETE FROM `operation`")
	require.NoError(t, err)
	_, err = engine.Exec("DELETE FROM `reservation`")
	require.NoError(t, err)

	previous, ok := db.DefaultContext.(db.Engined)
	require.True(t, ok, "default context carries the unit-test engine")
	db.SetDefaultEngine(context.Background(), engine)
	t.Cleanup(func() { db.SetDefaultEngine(context.Background(), previous.Engine()) })
	return engine
}

// The existing real PostgreSQL ordering roots can exercise the human merge's
// stricter generation/scope lock without replacing their cancellation races.
// Native SDK/Git/policy binding is qualified in the actual source integration.
func postgresAdmission(t *testing.T, ctx context.Context, id string) (string, func(context.Context) (*nativeoperation_model.Operation, bool, error)) {
	t.Helper()
	start, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	owner := "cond:" + testInstallation + "/" + id
	op := testOperation(id, start.Revision)
	human := os.Getenv("FORGEJO_TEST_PG_HUMAN_MERGE") == "1"
	if human {
		op.Kind, op.TokenID, op.CredentialFingerprint = nativeoperation_model.KindHumanMerge, 0, ""
		op.AuthRevision, op.NotAfter = "native-human-request", time.Now().Unix()+60
	}
	held, err := nativeoperation_model.ClaimConditional(ctx, op, owner, `{}`, "v")
	require.NoError(t, err)
	if !human {
		return owner, func(ctx context.Context) (*nativeoperation_model.Operation, bool, error) {
			return nativeoperation_model.RecordAdmissionAttempt(ctx, testInstallation, id, owner, true, "")
		}
	}
	const scope = `{"prepared_scope":"native-human-merge"}` // Opaque at this model boundary.
	// A concurrent second binder must lose the original-scope CAS, even when it
	// names the same computed scope. It cannot replace the winning generation.
	gate := make(chan struct{})
	var bindWG sync.WaitGroup
	bindErrors := make([]error, 2)
	for i := range bindErrors {
		bindWG.Go(func() {
			<-gate
			bindErrors[i] = nativeoperation_model.BindHumanMergeScope(ctx, op, owner, held.Generation, `{}`, scope, time.Now().Unix())
		})
	}
	close(gate)
	bindWG.Wait()
	winners := 0
	for _, err := range bindErrors {
		if err == nil {
			winners++
		} else {
			require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
		}
	}
	require.Equal(t, 1, winners)
	_, yes, err := nativeoperation_model.RecordHumanMergeAdmission(ctx, op, owner, held.Generation+1, scope, time.Now().Unix(), true, "")
	require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
	require.False(t, yes)
	return owner, func(ctx context.Context) (*nativeoperation_model.Operation, bool, error) {
		return nativeoperation_model.RecordHumanMergeAdmission(ctx, op, owner, held.Generation, scope, time.Now().Unix(), true, "")
	}
}

func TestCancellationWinsBlockedAdmissionOnPostgres(t *testing.T) {
	engine := useDisposablePostgres(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	_, admit := postgresAdmission(t, ctx, "op-pg-cancel-admission")

	// Keep cancellation uncommitted while admission reads the older pending
	// row. Its UPDATE then waits on this transaction's row lock. Committing
	// cancellation first must make the admission transition lose.
	cancelCtx, commit, err := db.TxContext(ctx)
	require.NoError(t, err)
	defer commit.Close()
	revoked, err := nativeoperation_model.RevokeOperation(cancelCtx, testInstallation, "op-pg-cancel-admission")
	require.NoError(t, err)
	require.True(t, revoked.Revoked)
	type result struct {
		op       *nativeoperation_model.Operation
		admitted bool
		err      error
	}
	finished := make(chan result, 1)
	go func() {
		op, admitted, err := admit(ctx)
		finished <- result{op, admitted, err}
	}()
	require.Eventually(t, func() bool {
		var waiting struct{ Count int64 }
		has, err := engine.SQL(`SELECT count(*) AS count FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid()
			AND wait_event_type = 'Lock' AND query ILIKE '%UPDATE%operation%admitted%'`).Get(&waiting)
		return err == nil && has && waiting.Count == 1
	}, 10*time.Second, 10*time.Millisecond, "admission must reach the locked UPDATE after its read")
	require.NoError(t, commit.Commit())
	select {
	case got := <-finished:
		require.NoError(t, got.err)
		require.False(t, got.admitted)
		require.True(t, got.op.Revoked)
		require.False(t, got.op.Admitted)
		require.Equal(t, nativeoperation_model.ReasonCancelledBeforeAdmission, got.op.Reason)
	case <-ctx.Done():
		t.Fatal("admission did not settle after cancellation committed")
	}
	stored, err := nativeoperation_model.LookupOperation(ctx, testInstallation, "op-pg-cancel-admission")
	require.NoError(t, err)
	require.True(t, stored.Revoked)
	require.False(t, stored.Admitted)
	if os.Getenv("FORGEJO_TEST_PG_HUMAN_MERGE_EFFECT") == "1" {
		first, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, first.Owner))
		owner, _ := postgresAdmission(t, ctx, "op-pg-effect-cancel-order")
		want, err := nativeoperation_model.LookupOperation(ctx, testInstallation, "op-pg-effect-cancel-order")
		require.NoError(t, err)
		held, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		cancelled, cancelCommit, err := db.TxContext(ctx)
		require.NoError(t, err)
		defer cancelCommit.Close()
		_, err = nativeoperation_model.RevokeOperation(cancelled, testInstallation, want.OperationID)
		require.NoError(t, err)
		type effectResult struct {
			op  *nativeoperation_model.Operation
			err error
		}
		effects := make(chan effectResult, 1)
		go func() {
			op, err := nativeoperation_model.RecordHumanMergeEffect(ctx, want, owner, held.Generation, held.Scope, func(_ context.Context, current *nativeoperation_model.Operation) (nativeoperation_model.TerminalOutcome, error) {
				if !current.Revoked || current.Admitted {
					return nativeoperation_model.TerminalOutcome{}, fmt.Errorf("effect observed stale cancellation")
				}
				return nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted, Reason: nativeoperation_model.ReasonCancelledBeforeAdmission, Cancellation: nativeoperation_model.CancellationCancelled}, nil
			})
			effects <- effectResult{op, err}
		}()
		require.Eventually(t, func() bool {
			var waiting struct{ Count int64 }
			has, err := engine.SQL(`SELECT count(*) AS count FROM pg_stat_activity
				WHERE datname = current_database() AND pid <> pg_backend_pid()
				AND wait_event_type = 'Lock' AND query ILIKE '%UPDATE%operation%reason%'`).Get(&waiting)
			return err == nil && has && waiting.Count == 1
		}, 10*time.Second, 10*time.Millisecond, "effect must wait on the actual cancellation row lock")
		require.NoError(t, cancelCommit.Commit())
		select {
		case got := <-effects:
			require.NoError(t, got.err)
			require.Equal(t, nativeoperation_model.EffectNotCommitted, got.op.EffectState)
			require.Equal(t, nativeoperation_model.CancellationCancelled, got.op.Cancellation)
			require.Empty(t, got.op.Receipt)
		case <-ctx.Done():
			t.Fatal("effect did not settle after cancellation committed")
		}
		stillHeld, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		require.Equal(t, owner, stillHeld.Owner)
		if os.Getenv("FORGEJO_TEST_PG_HUMAN_MERGE_NONCOMMIT") == "1" {
			// Opaque model evidence: the actual native source root proves the
			// closed writer and unchanged Git ref. This root owns SQL ordering.
			verify := func(context.Context, *nativeoperation_model.Operation) error { return nil }
			_, err := nativeoperation_model.ReleaseHumanMergeNotCommitted(ctx, want, owner, held.Generation, held.Scope, nil)
			require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
			_, err = nativeoperation_model.ReleaseHumanMergeNotCommitted(ctx, want, owner, held.Generation+1, held.Scope, verify)
			require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
			_, err = nativeoperation_model.ReleaseHumanMergeNotCommitted(ctx, want, owner, held.Generation, held.Scope+"changed", verify)
			require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
			_, err = nativeoperation_model.ReleaseHumanMergeNotCommitted(ctx, want, owner, held.Generation, held.Scope, func(context.Context, *nativeoperation_model.Operation) error {
				return fmt.Errorf("fixture native evidence refused")
			})
			require.Error(t, err)
			for _, row := range []*nativeoperation_model.Operation{
				{EffectState: nativeoperation_model.EffectPending},
				{EffectState: nativeoperation_model.EffectIndeterminate},
				{EffectState: nativeoperation_model.EffectCommitted},
				{EffectState: nativeoperation_model.EffectNotCommitted, Admitted: true},
				{EffectState: nativeoperation_model.EffectNotCommitted, Receipt: "unexpected"},
				{EffectState: nativeoperation_model.EffectNotCommitted, Completion: nativeoperation_model.CompletionPending},
			} {
				_, err := db.GetEngine(ctx).ID(want.ID).Cols("effect_state", "admitted", "receipt", "completion").Update(row)
				require.NoError(t, err)
				_, err = nativeoperation_model.ReleaseHumanMergeNotCommitted(ctx, want, owner, held.Generation, held.Scope, verify)
				require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
			}
			_, err = db.GetEngine(ctx).ID(want.ID).Cols("effect_state", "admitted", "receipt", "completion").Update(&nativeoperation_model.Operation{EffectState: nativeoperation_model.EffectNotCommitted})
			require.NoError(t, err)
			before, err := nativeoperation_model.LookupOperation(ctx, testInstallation, want.OperationID)
			require.NoError(t, err)
			_, err = db.GetEngine(ctx).Exec("CREATE FUNCTION refuse_noncommit_release() RETURNS TRIGGER AS $$ BEGIN IF NEW.owner='' THEN RAISE EXCEPTION 'fixture noncommit release failure'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql")
			require.NoError(t, err)
			_, err = db.GetEngine(ctx).Exec("CREATE TRIGGER fail_noncommit_release BEFORE UPDATE OF owner ON reservation FOR EACH ROW EXECUTE FUNCTION refuse_noncommit_release()")
			require.NoError(t, err)
			_, err = nativeoperation_model.ReleaseHumanMergeNotCommitted(ctx, want, owner, held.Generation, held.Scope, verify)
			require.Error(t, err)
			retained, err := nativeoperation_model.ReadReservation(ctx)
			require.NoError(t, err)
			require.Equal(t, owner, retained.Owner)
			require.Equal(t, held.Scope, retained.Scope)
			_, err = db.GetEngine(ctx).Exec("DROP TRIGGER fail_noncommit_release ON reservation")
			require.NoError(t, err)
			_, err = db.GetEngine(ctx).Exec("DROP FUNCTION refuse_noncommit_release()")
			require.NoError(t, err)
			gate := make(chan struct{})
			results := make([]*nativeoperation_model.Operation, 8)
			failures := make([]error, len(results))
			var wg sync.WaitGroup
			for i := range results {
				wg.Go(func() {
					<-gate
					results[i], failures[i] = nativeoperation_model.ReleaseHumanMergeNotCommitted(ctx, want, owner, held.Generation, held.Scope, verify)
				})
			}
			close(gate)
			wg.Wait()
			winners := 0
			for i := range results {
				if failures[i] == nil {
					winners++
					require.Equal(t, before, results[i])
				} else {
					require.ErrorIs(t, failures[i], nativeoperation_model.ErrWrongOwner)
				}
			}
			require.Equal(t, 1, winners)
			idle, err := nativeoperation_model.ReadReservation(ctx)
			require.NoError(t, err)
			require.Empty(t, idle.Owner)
			require.Empty(t, idle.Scope)
			require.Empty(t, idle.Verifier)
			require.Equal(t, held.Revision, idle.Revision)
			require.Equal(t, held.Generation, idle.Generation)
			after, err := nativeoperation_model.LookupOperation(ctx, testInstallation, want.OperationID)
			require.NoError(t, err)
			require.Equal(t, before, after)
		}
	}
}

func TestCompetingAdmissionsOnPostgres(t *testing.T) {
	useDisposablePostgres(t)
	ctx := t.Context()
	_, admit := postgresAdmission(t, ctx, "op-pg-admission-race")
	gate := make(chan struct{})
	var wg sync.WaitGroup
	admitted := make([]bool, 8)
	errors := make([]error, len(admitted))
	for i := range admitted {
		wg.Go(func() {
			<-gate
			_, admitted[i], errors[i] = admit(ctx)
		})
	}
	close(gate)
	wg.Wait()
	winners := 0
	for i := range admitted {
		require.NoError(t, errors[i])
		if admitted[i] {
			winners++
		}
	}
	require.Equal(t, 1, winners)
	stored, err := nativeoperation_model.LookupOperation(ctx, testInstallation, "op-pg-admission-race")
	require.NoError(t, err)
	require.True(t, stored.Admitted)
	if os.Getenv("FORGEJO_TEST_PG_HUMAN_MERGE_EFFECT") == "1" {
		held, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		var observed atomic.Int64
		results := make([]*nativeoperation_model.Operation, 8)
		errors := make([]error, len(results))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range results {
			wg.Go(func() {
				<-start
				results[i], errors[i] = nativeoperation_model.RecordHumanMergeEffect(ctx, stored, held.Owner, held.Generation, held.Scope, func(context.Context, *nativeoperation_model.Operation) (nativeoperation_model.TerminalOutcome, error) {
					observed.Add(1)
					// The model owns locking/monotonicity; the SDK/Git owner validates
					// actual receipt facts in the native source integration.
					return nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectCommitted, Completion: nativeoperation_model.CompletionPending, Cancellation: nativeoperation_model.CancellationNone, Receipt: `{"model_fixture":"committed"}`}, nil
				})
			})
		}
		close(start)
		wg.Wait()
		require.EqualValues(t, 1, observed.Load())
		for i, r := range results {
			require.NoError(t, errors[i])
			require.Equal(t, results[0].Receipt, r.Receipt)
			require.Equal(t, nativeoperation_model.EffectCommitted, r.EffectState)
			require.Equal(t, nativeoperation_model.CompletionPending, r.Completion)
		}
		_, err = nativeoperation_model.RecordHumanMergeEffect(ctx, stored, held.Owner, held.Generation+1, held.Scope, func(context.Context, *nativeoperation_model.Operation) (nativeoperation_model.TerminalOutcome, error) {
			return nativeoperation_model.TerminalOutcome{}, fmt.Errorf("wrong generation reached observer")
		})
		require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
		stillHeld, err := nativeoperation_model.ReadReservation(ctx)
		require.NoError(t, err)
		require.Equal(t, held.Owner, stillHeld.Owner)
		if os.Getenv("FORGEJO_TEST_PG_HUMAN_MERGE_METADATA") == "1" {
			// Opaque domain fixture: native PR/issue/history correctness is
			// qualified in the real source root. This checks transaction/locking.
			_, err := db.GetEngine(ctx).Exec("CREATE TABLE human_merge_metadata_fixture (id BIGINT PRIMARY KEY, applied BIGINT NOT NULL)")
			require.NoError(t, err)
			_, err = db.GetEngine(ctx).Exec("INSERT INTO human_merge_metadata_fixture (id, applied) VALUES (1, 0)")
			require.NoError(t, err)
			receipt := results[0].Receipt
			apply := func(tx context.Context, _ *nativeoperation_model.Operation) (bool, error) {
				r, err := db.GetEngine(tx).Exec("UPDATE human_merge_metadata_fixture SET applied=1 WHERE id=1 AND applied=0")
				if err != nil {
					return false, err
				}
				n, err := r.RowsAffected()
				return n == 1, err
			}
			start := make(chan struct{})
			changed := make([]bool, 8)
			errors := make([]error, len(changed))
			var wg sync.WaitGroup
			for i := range changed {
				wg.Go(func() {
					<-start
					changed[i], errors[i] = nativeoperation_model.ApplyHumanMergeMetadata(ctx, stored, held.Owner, held.Generation, held.Scope, receipt, apply)
				})
			}
			close(start)
			wg.Wait()
			winners := 0
			for i := range changed {
				require.NoError(t, errors[i])
				if changed[i] {
					winners++
				}
			}
			require.Equal(t, 1, winners)
			didChange, err := nativeoperation_model.ApplyHumanMergeMetadata(ctx, stored, held.Owner, held.Generation+1, held.Scope, receipt, apply)
			require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
			require.False(t, didChange)
			didChange, err = nativeoperation_model.ApplyHumanMergeMetadata(ctx, stored, held.Owner, held.Generation, held.Scope, receipt+"changed", apply)
			require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
			require.False(t, didChange)
			didChange, err = nativeoperation_model.ApplyHumanMergeMetadata(ctx, stored, held.Owner, held.Generation, held.Scope, receipt, func(tx context.Context, _ *nativeoperation_model.Operation) (bool, error) {
				if _, err := db.GetEngine(tx).Exec("UPDATE human_merge_metadata_fixture SET applied=2 WHERE id=1"); err != nil {
					return false, err
				}
				return true, fmt.Errorf("fixture metadata rollback")
			})
			require.Error(t, err)
			require.False(t, didChange)
			var state struct{ Applied int64 }
			exists, err := db.GetEngine(ctx).Table("human_merge_metadata_fixture").Where("id=1").Get(&state)
			require.NoError(t, err)
			require.True(t, exists)
			require.EqualValues(t, 1, state.Applied)
			fresh, err := nativeoperation_model.LookupOperation(ctx, stored.InstallationID, stored.OperationID)
			require.NoError(t, err)
			require.Equal(t, receipt, fresh.Receipt)
			require.Equal(t, nativeoperation_model.CompletionPending, fresh.Completion)
			stillHeld, err := nativeoperation_model.ReadReservation(ctx)
			require.NoError(t, err)
			require.Equal(t, held.Owner, stillHeld.Owner)
			if os.Getenv("FORGEJO_TEST_PG_HUMAN_MERGE_FINISH") == "1" {
				const startedScope = `{"model_finish":"started"}`
				const doneScope = `{"model_finish":"done"}`
				gate := make(chan struct{})
				errors := make([]error, 8)
				var wg sync.WaitGroup
				for i := range errors {
					wg.Go(func() {
						<-gate
						errors[i] = nativeoperation_model.ClaimHumanMergeDispatch(ctx, stored, held.Owner, held.Generation, held.Scope, startedScope, receipt)
					})
				}
				close(gate)
				wg.Wait()
				winners := 0
				for _, err := range errors {
					if err == nil {
						winners++
					} else {
						require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
					}
				}
				require.Equal(t, 1, winners)
				_, err = nativeoperation_model.RecordHumanMergeDispatch(ctx, stored, held.Owner, held.Generation+1, startedScope, doneScope, receipt, true)
				require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
				_, err = nativeoperation_model.RecordHumanMergeDispatch(ctx, stored, held.Owner, held.Generation, startedScope, doneScope, receipt+"changed", true)
				require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
				finished, err := nativeoperation_model.RecordHumanMergeDispatch(ctx, stored, held.Owner, held.Generation, startedScope, doneScope, receipt, true)
				require.NoError(t, err)
				require.Equal(t, nativeoperation_model.CompletionPending, finished.Completion)
				require.Equal(t, receipt, finished.Receipt)
				after, err := nativeoperation_model.ReadReservation(ctx)
				require.NoError(t, err)
				require.Equal(t, held.Owner, after.Owner)
				require.Equal(t, doneScope, after.Scope)
				require.ErrorIs(t, nativeoperation_model.ClaimHumanMergeDispatch(ctx, stored, held.Owner, held.Generation, held.Scope, startedScope, receipt), nativeoperation_model.ErrWrongOwner)
				released := false
				if os.Getenv("FORGEJO_TEST_PG_HUMAN_MERGE_COMPLETE") == "1" {
					// The service proves the closed stage and native phase. This
					// opaque model fixture proves the joined SQL release is atomic.
					_, err := db.GetEngine(ctx).Exec("CREATE FUNCTION refuse_human_merge_release() RETURNS TRIGGER AS $$ BEGIN IF NEW.owner='' THEN RAISE EXCEPTION 'fixture human merge release failure'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql")
					require.NoError(t, err)
					_, err = db.GetEngine(ctx).Exec("CREATE TRIGGER fail_human_merge_release BEFORE UPDATE OF owner ON reservation FOR EACH ROW EXECUTE FUNCTION refuse_human_merge_release()")
					require.NoError(t, err)
					_, err = nativeoperation_model.CompleteHumanMerge(ctx, stored, held.Owner, held.Generation, doneScope, receipt)
					require.Error(t, err)
					pending, err := nativeoperation_model.LookupOperation(ctx, stored.InstallationID, stored.OperationID)
					require.NoError(t, err)
					require.Equal(t, nativeoperation_model.CompletionPending, pending.Completion)
					require.Equal(t, receipt, pending.Receipt)
					retained, err := nativeoperation_model.ReadReservation(ctx)
					require.NoError(t, err)
					require.Equal(t, held.Owner, retained.Owner)
					require.Equal(t, held.Generation, retained.Generation)
					require.Equal(t, doneScope, retained.Scope)
					_, err = db.GetEngine(ctx).Exec("DROP TRIGGER fail_human_merge_release ON reservation")
					require.NoError(t, err)
					_, err = db.GetEngine(ctx).Exec("DROP FUNCTION refuse_human_merge_release()")
					require.NoError(t, err)
					_, err = nativeoperation_model.CompleteHumanMerge(ctx, stored, held.Owner, held.Generation+1, doneScope, receipt)
					require.ErrorIs(t, err, nativeoperation_model.ErrWrongOwner)
					_, err = nativeoperation_model.CompleteHumanMerge(ctx, stored, held.Owner, held.Generation, doneScope, receipt+"changed")
					require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
					gate := make(chan struct{})
					results := make([]*nativeoperation_model.Operation, 8)
					failures := make([]error, len(results))
					var wg sync.WaitGroup
					for i := range results {
						wg.Go(func() {
							<-gate
							results[i], failures[i] = nativeoperation_model.CompleteHumanMerge(ctx, stored, held.Owner, held.Generation, doneScope, receipt)
						})
					}
					close(gate)
					wg.Wait()
					winners := 0
					for i := range results {
						if failures[i] == nil {
							winners++
							require.Equal(t, nativeoperation_model.CompletionComplete, results[i].Completion)
							require.Equal(t, receipt, results[i].Receipt)
						} else {
							require.ErrorIs(t, failures[i], nativeoperation_model.ErrWrongOwner)
						}
					}
					require.Equal(t, 1, winners)
					idle, err := nativeoperation_model.ReadReservation(ctx)
					require.NoError(t, err)
					require.Empty(t, idle.Owner)
					require.Empty(t, idle.OwnerKind)
					require.Empty(t, idle.Scope)
					require.Empty(t, idle.Verifier)
					require.Equal(t, held.Revision, idle.Revision)
					require.Equal(t, held.Generation, idle.Generation)
					complete, err := nativeoperation_model.LookupOperation(ctx, stored.InstallationID, stored.OperationID)
					require.NoError(t, err)
					require.Equal(t, nativeoperation_model.CompletionComplete, complete.Completion)
					require.Equal(t, receipt, complete.Receipt)
					released = true
				}
				// A second isolated operation proves failure is durably claimed,
				// needs intervention and cannot be overwritten by a later success.
				if !released {
					require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, held.Owner))
				}
				owner, admit := postgresAdmission(t, ctx, "op-pg-finish-failure")
				_, yes, err := admit(ctx)
				require.NoError(t, err)
				require.True(t, yes)
				want, err := nativeoperation_model.LookupOperation(ctx, testInstallation, "op-pg-finish-failure")
				require.NoError(t, err)
				owned, err := nativeoperation_model.ReadReservation(ctx)
				require.NoError(t, err)
				_, err = nativeoperation_model.RecordHumanMergeEffect(ctx, want, owner, owned.Generation, owned.Scope, func(context.Context, *nativeoperation_model.Operation) (nativeoperation_model.TerminalOutcome, error) {
					return nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectCommitted, Completion: nativeoperation_model.CompletionPending, Cancellation: nativeoperation_model.CancellationNone, Receipt: receipt}, nil
				})
				require.NoError(t, err)
				require.NoError(t, nativeoperation_model.ClaimHumanMergeDispatch(ctx, want, owner, owned.Generation, owned.Scope, startedScope, receipt))
				failed, err := nativeoperation_model.RecordHumanMergeDispatch(ctx, want, owner, owned.Generation, startedScope, doneScope, receipt, false)
				require.NoError(t, err)
				require.Equal(t, nativeoperation_model.CompletionNeedsIntervention, failed.Completion)
				require.Equal(t, receipt, failed.Receipt)
				_, err = nativeoperation_model.RecordHumanMergeDispatch(ctx, want, owner, owned.Generation, startedScope, doneScope, receipt, true)
				require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
				retained, err := nativeoperation_model.ReadReservation(ctx)
				require.NoError(t, err)
				require.Equal(t, owner, retained.Owner)
				require.Equal(t, startedScope, retained.Scope)
				if os.Getenv("FORGEJO_TEST_PG_HUMAN_MERGE_COMPLETE") == "1" {
					_, err := nativeoperation_model.CompleteHumanMerge(ctx, want, owner, owned.Generation, startedScope, receipt)
					require.ErrorIs(t, err, nativeoperation_model.ErrAdmissionLost)
				}
			}
		}
	}
}

func TestAdmissionBeforeCancellationOnPostgres(t *testing.T) {
	useDisposablePostgres(t)
	ctx := t.Context()
	owner, admit := postgresAdmission(t, ctx, "op-pg-admission-first")
	_, admitted, err := admit(ctx)
	require.NoError(t, err)
	require.True(t, admitted)
	revoked, err := nativeoperation_model.RevokeOperation(ctx, testInstallation, "op-pg-admission-first")
	require.NoError(t, err)
	require.True(t, revoked.Admitted)
	require.True(t, revoked.Revoked)
	require.Equal(t, nativeoperation_model.EffectPending, revoked.EffectState)
	held, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, owner, held.Owner, "a cancellation request cannot release an admitted effect")
	terminal, err := nativeoperation_model.SetTerminal(ctx, testInstallation, "op-pg-admission-first", nativeoperation_model.TerminalOutcome{
		EffectState: nativeoperation_model.EffectCommitted, Cancellation: nativeoperation_model.CancellationTooLate,
		Completion: nativeoperation_model.CompletionComplete, Receipt: `{}`,
	}, owner)
	require.NoError(t, err)
	require.Equal(t, nativeoperation_model.CancellationTooLate, terminal.Cancellation)
	idle, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Empty(t, idle.Owner)
	stable, err := nativeoperation_model.SetTerminal(ctx, testInstallation, "op-pg-admission-first", nativeoperation_model.TerminalOutcome{EffectState: nativeoperation_model.EffectNotCommitted}, "")
	require.NoError(t, err)
	require.Equal(t, nativeoperation_model.EffectCommitted, stable.EffectState)
	require.Equal(t, nativeoperation_model.CancellationTooLate, stable.Cancellation)
}

type raceResult struct {
	owner string
	won   bool
	err   error
}

// runClaimRace fires racers claimants at the idle reservation through
// separate sessions, the way concurrent processes reach PostgreSQL.
func runClaimRace(racers int, claim func(ctx context.Context, i int) (owner string, err error)) []raceResult {
	gate := make(chan struct{})
	results := make([]raceResult, racers)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			<-gate
			owner, err := claim(context.Background(), i)
			results[i] = raceResult{owner: owner, won: err == nil, err: err}
		})
	}
	close(gate)
	wg.Wait()
	return results
}

func winnersOf(results []raceResult) []raceResult {
	var winners []raceResult
	for _, r := range results {
		if r.won {
			winners = append(winners, r)
		}
	}
	return winners
}

func TestConcurrentOrdinaryClaimOnPostgres(t *testing.T) {
	engine := useDisposablePostgres(t)
	ctx := context.Background()
	// Hold both first INSERTs after their empty reads. A duplicate must not
	// abort either caller's surrounding transaction or change the idle row.
	blocked, barrier, err := db.TxContext(ctx)
	require.NoError(t, err)
	defer barrier.Close()
	_, err = db.GetEngine(blocked).Exec("LOCK TABLE `reservation` IN SHARE MODE")
	require.NoError(t, err)
	type firstRead struct {
		reservation *nativeoperation_model.Reservation
		err         error
	}
	reads := make(chan firstRead, 2)
	for range 2 {
		go func() {
			transaction, commit, err := db.TxContext(ctx)
			if err != nil {
				reads <- firstRead{err: err}
				return
			}
			defer commit.Close()
			reservation, err := nativeoperation_model.ReadReservation(transaction)
			if err == nil {
				_, err = db.GetEngine(transaction).Exec("SELECT 1")
			}
			if err == nil {
				err = commit.Commit()
			}
			reads <- firstRead{reservation, err}
		}()
	}
	require.Eventually(t, func() bool {
		var count int64
		_, err := engine.SQL("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'INSERT INTO %reservation%'").Get(&count)
		return err == nil && count == 2
	}, 10*time.Second, 10*time.Millisecond)
	require.NoError(t, barrier.Commit())
	for range 2 {
		got := <-reads
		require.NoError(t, got.err)
		require.EqualValues(t, 1, got.reservation.Revision)
		require.Empty(t, got.reservation.Owner)
		require.Zero(t, got.reservation.Generation)
	}

	start, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Empty(t, start.Owner)

	results := runClaimRace(8, func(ctx context.Context, i int) (string, error) {
		owner := fmt.Sprintf("ord:race/pg/%d", i)
		_, err := nativeoperation_model.ClaimOrdinary(ctx, owner, `{"kind":"ordinary"}`, "v")
		return owner, err
	})
	for _, r := range results {
		if !r.won {
			require.ErrorIs(t, r.err, nativeoperation_model.ErrBusy, "loser %s", r.owner)
		}
	}
	winners := winnersOf(results)
	require.Len(t, winners, 1, "exactly one claimant must gain the reservation")

	final, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, winners[0].owner, final.Owner)
	require.Equal(t, start.Revision+1, final.Revision)
	require.Equal(t, start.Generation+1, final.Generation)

	// Every loser stays fenced out of effects under the winner's hold.
	for _, r := range results {
		if r.won {
			continue
		}
		require.ErrorIs(t, nativeoperation_model.ReleaseOwner(ctx, r.owner), nativeoperation_model.ErrWrongOwner)
		loserCtx := execcontext.NewContext(ctx, &execcontext.Execution{Owner: r.owner, Generation: final.Generation})
		require.ErrorIs(t, nativeoperation_model.RequireHeldOwnership(loserCtx), nativeoperation_model.ErrBusy, "loser %s", r.owner)
	}
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, winners[0].owner))
}

func TestConcurrentConditionalClaimOnPostgres(t *testing.T) {
	useDisposablePostgres(t)
	ctx := context.Background()

	start, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Empty(t, start.Owner)

	results := runClaimRace(8, func(ctx context.Context, i int) (string, error) {
		id := fmt.Sprintf("op-pg-race-%d", i)
		owner := "cond:" + testInstallation + "/" + id
		_, err := nativeoperation_model.ClaimConditional(ctx, testOperation(id, start.Revision), owner, `{"kind":"conditional"}`, "v")
		return owner, err
	})
	for _, r := range results {
		if !r.won {
			require.True(t, r.err == nativeoperation_model.ErrBusy || r.err == nativeoperation_model.ErrStaleRevision, "loser %s: %v", r.owner, r.err)
		}
	}
	winners := winnersOf(results)
	require.Len(t, winners, 1, "exactly one claimant must gain the revision")

	final, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, winners[0].owner, final.Owner)
	require.Equal(t, start.Revision+1, final.Revision)

	// Losing claims record no operation and cannot admit under their name.
	for i, r := range results {
		id := fmt.Sprintf("op-pg-race-%d", i)
		stored, err := nativeoperation_model.LookupOperation(ctx, testInstallation, id)
		require.NoError(t, err)
		if r.won {
			require.NotNil(t, stored, "winner records its operation")
			continue
		}
		require.Nil(t, stored, "loser %s records nothing", r.owner)
		_, _, err = nativeoperation_model.RecordAdmissionAttempt(ctx, testInstallation, id, r.owner, true, "")
		require.Error(t, err, "loser %s cannot admit", r.owner)
	}
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, winners[0].owner))
}

func TestConcurrentPublishReceiveClaimOnPostgres(t *testing.T) {
	useDisposablePostgres(t)
	ctx := context.Background()

	start, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Empty(t, start.Owner)
	_, err = nativeoperation_model.InsertWaitingOperation(ctx, testOperation("op-pg-receive", start.Revision))
	require.NoError(t, err)

	results := runClaimRace(8, func(ctx context.Context, i int) (string, error) {
		owner := fmt.Sprintf("cond:%s/op-pg-receive#%d", testInstallation, i)
		_, _, err := nativeoperation_model.ClaimPublishReceive(ctx, testInstallation, "op-pg-receive", owner, `{"kind":"conditional"}`, "v", 1000000000)
		return owner, err
	})
	for _, r := range results {
		if !r.won {
			require.True(t, r.err == nativeoperation_model.ErrBusy || r.err == nativeoperation_model.ErrStaleRevision || r.err == nativeoperation_model.ErrAdmissionLost, "loser %s: %v", r.owner, r.err)
		}
	}
	winners := winnersOf(results)
	require.Len(t, winners, 1, "exactly one receiver must gain the reservation")

	final, err := nativeoperation_model.ReadReservation(ctx)
	require.NoError(t, err)
	require.Equal(t, winners[0].owner, final.Owner)
	require.Equal(t, start.Revision+1, final.Revision)
	require.NoError(t, nativeoperation_model.ReleaseOwner(ctx, winners[0].owner))
}

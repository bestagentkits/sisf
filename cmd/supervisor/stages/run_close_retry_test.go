package stages

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// flakyCloser fails the first closes it is asked for, as a store that is
// briefly unavailable would, then closes through the real budget gate.
type flakyCloser struct {
	dispatcher.ReservationCloser
	failures atomic.Int32
	closes   atomic.Int32
}

func (f *flakyCloser) Release(ctx context.Context, ledger core.BudgetLedger, id string) error {
	f.closes.Add(1)
	if f.failures.Add(-1) >= 0 {
		return core.NewError(core.CodeUnavailable, "the store is busy")
	}
	return f.ReservationCloser.Release(ctx, ledger, id)
}

// A run QA passed or held has its budget reservation closed. When that
// close fails, it is tried again on the next passes of the QA stage until
// it succeeds, and then never again: a single failure does not leave the
// reservation held for good.
func TestAFailedCloseOfARunsReservationIsTriedAgainUntilItSucceeds(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		agentZone, control := db.Open(t, core.ZoneAgent), db.Open(t, core.ZoneControl)
		gate := budget.NewGate(budget.Config{Clock: db.Clock,
			Confirmations: dispatcher.ConfirmationFunc(func(context.Context, core.BudgetLedger) (dispatcher.BudgetConfirmation, error) {
				return dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Minute)}, nil
			})})
		issue, run := core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, core.RunID("run-7")
		var reservation core.Reservation
		if err := agentZone.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			var err error
			reservation, err = gate.Reserve(ctx, tx, core.ReservationRequest{FactoryID: sqlstoretest.Factory, IssueID: issue, RunID: run, IdempotencyKey: "claim-7"})
			if err != nil {
				return err
			}
			raw, _ := json.Marshal(dispatcher.RunRecord{Issue: issue, RunID: run, ReservationID: reservation.ID, Status: dispatcher.RunSucceeded})
			_, err = tx.PutKV(ctx, state.KVEntry{Namespace: handoff.Namespace, Key: "run/" + string(run), Value: raw})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		closer := &flakyCloser{ReservationCloser: gate}
		closer.failures.Store(2)
		s := &QAStage{FactoryID: sqlstoretest.Factory, Owner: "supervisor-qa", State: control, Budget: closer, Clock: db.Clock}
		// What a passed or held verdict records with itself.
		if err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error { return putCloseDue(ctx, tx, run) }); err != nil {
			t.Fatal(err)
		}
		status := func() core.ReservationStatus {
			var r core.Reservation
			if err := control.View(ctx, func(ctx context.Context, tx state.Tx) error {
				var err error
				r, err = tx.GetReservation(ctx, reservation.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return r.Status
		}
		for pass := 1; pass <= 2; pass++ {
			if res := s.Pass(ctx); len(res.Errors) != 1 || status() != core.ReservationHeld {
				t.Fatalf("pass %d: errors %v, reservation %s", pass, res.Errors, status())
			}
		}
		if res := s.Pass(ctx); len(res.Errors) != 0 || status() != core.ReservationReleased {
			t.Fatalf("the third pass: errors %v, reservation %s", res.Errors, status())
		}
		closes := closer.closes.Load()
		if res := s.Pass(ctx); len(res.Errors) != 0 || closer.closes.Load() != closes {
			t.Fatalf("a closed run was closed again: errors %v, %d closes", res.Errors, closer.closes.Load())
		}
	})
}

package stages

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The record of a run QA passed or held is the agent zone's, which can
// rewrite it. One naming another run's reservation neither closes that
// reservation nor keeps the run's own held: the close derives the run's
// reservation from its ID.
func TestARunRecordNamingAnotherRunsReservationClosesOnlyItsOwn(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		ctx := context.Background()
		agentZone, control := db.Open(t, core.ZoneAgent), db.Open(t, core.ZoneControl)
		gate := budget.NewGate(budget.Config{Clock: db.Clock,
			Confirmations: dispatcher.ConfirmationFunc(func(context.Context, core.BudgetLedger) (dispatcher.BudgetConfirmation, error) {
				return dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Minute)}, nil
			})})
		run, other := core.RunID("run-7"), core.RunID("run-8")
		reserved := map[core.RunID]string{}
		if err := agentZone.Update(ctx, func(ctx context.Context, tx state.Tx) error {
			for n, r := range []core.RunID{run, other} {
				res, err := gate.Reserve(ctx, tx, core.ReservationRequest{FactoryID: sqlstoretest.Factory,
					IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7 + n}, RunID: r, IdempotencyKey: dispatcher.ClaimIdempotencyKey(r)})
				if err != nil {
					return err
				}
				reserved[r] = res.ID
			}
			raw, _ := json.Marshal(dispatcher.RunRecord{Issue: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, RunID: run,
				ReservationID: reserved[other], Status: dispatcher.RunSucceeded})
			_, err := tx.PutKV(ctx, state.KVEntry{Namespace: handoff.Namespace, Key: "run/" + string(run), Value: raw})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		s := &QAStage{FactoryID: sqlstoretest.Factory, Owner: "supervisor-qa", State: control, Budget: gate, Clock: db.Clock}
		if err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error { return putCloseDue(ctx, tx, run) }); err != nil {
			t.Fatal(err)
		}
		if res := s.Pass(ctx); len(res.Errors) != 0 {
			t.Fatalf("the close failed: %v", res.Errors)
		}
		status := func(id string) core.ReservationStatus {
			var r core.Reservation
			if err := control.View(ctx, func(ctx context.Context, tx state.Tx) error {
				var err error
				r, err = tx.GetReservation(ctx, id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return r.Status
		}
		if got := status(reserved[other]); got != core.ReservationHeld {
			t.Fatalf("a record naming another run's reservation closed it: %s", got)
		}
		if got := status(reserved[run]); got != core.ReservationReleased {
			t.Fatalf("the run's own reservation is %s, want released", got)
		}
	})
}

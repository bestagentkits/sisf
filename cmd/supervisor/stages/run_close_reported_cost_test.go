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

// The cost a run's reservation is closed with is read from the run's rows
// in the ledger. The dispatcher's record is the agent zone's: a record that
// claims a cost far above what was booked, and hides that the usage stated
// none, does not release the reservation, and a run with no record at all
// is closed from the ledger alone.
func TestARunIsClosedWithTheCostTheLedgerShows(t *testing.T) {
	t.Parallel()
	const estimate = core.Micros(1_000_000)
	for name, c := range map[string]struct {
		record     *dispatcher.RunRecord
		priced     bool
		wantStatus core.ReservationStatus
		wantSpent  core.Micros
	}{
		"inflated record, unpriced usage": {&dispatcher.RunRecord{Status: dispatcher.RunSucceeded,
			Spend: dispatcher.RunSpend{ReportedCost: 50 * estimate}}, false, core.ReservationSettled, estimate},
		"no record, priced usage":   {nil, true, core.ReservationReleased, 200_000},
		"no record, unpriced usage": {nil, false, core.ReservationSettled, estimate},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
				ctx := context.Background()
				agentZone, control := db.Open(t, core.ZoneAgent), db.Open(t, core.ZoneControl)
				gate := budget.NewGate(budget.Config{Clock: db.Clock,
					Confirmations: dispatcher.ConfirmationFunc(func(context.Context, core.BudgetLedger) (dispatcher.BudgetConfirmation, error) {
						return dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now().Add(-time.Minute)}, nil
					})})
				run, issue := core.RunID("run-7"), core.IssueRef{Owner: "acme", Repo: "app", Number: 7}
				var reservation string
				if err := agentZone.Update(ctx, func(ctx context.Context, tx state.Tx) error {
					res, err := gate.Reserve(ctx, tx, core.ReservationRequest{FactoryID: sqlstoretest.Factory, IssueID: issue, RunID: run,
						Estimate: estimate, IdempotencyKey: dispatcher.ClaimIdempotencyKey(run)})
					if err != nil {
						return err
					}
					reservation = res.ID
					u := core.Usage{IssueID: issue, RunID: run, ReservationID: res.ID, Step: core.StepCoding, InputTokens: 100, OutputTokens: 20}
					if c.priced {
						u.Cost = 200_000
					}
					if err := tx.AppendUsage(ctx, u); err != nil || c.record == nil {
						return err
					}
					record := *c.record
					record.Issue, record.RunID, record.ReservationID = issue, run, res.ID
					raw, _ := json.Marshal(record)
					_, err = tx.PutKV(ctx, state.KVEntry{Namespace: handoff.Namespace, Key: "run/" + string(run), Value: raw})
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
				var (
					r      core.Reservation
					totals core.BudgetTotals
				)
				if err := control.View(ctx, func(ctx context.Context, tx state.Tx) (err error) {
					if r, err = tx.GetReservation(ctx, reservation); err != nil {
						return err
					}
					totals, err = tx.BudgetTotals(ctx, time.Time{})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if r.Status != c.wantStatus {
					t.Errorf("the reservation is %s, want %s", r.Status, c.wantStatus)
				}
				if totals.SpentUnverified != c.wantSpent || totals.SpentVerified != 0 {
					t.Errorf("the ledger shows %+v, want %d unverified", totals, c.wantSpent)
				}
			})
		})
	}
}

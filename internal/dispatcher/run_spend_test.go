package dispatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// reportingAgent is the scripted agent with the usage a CLI adapter reports
// for every run: tokens, and a cost when the tool states one.
type reportingAgent struct {
	*fake.Backend
	cost core.Micros
}

func (a reportingAgent) Usage(_ context.Context, ref agent.RunRef) ([]core.Usage, error) {
	return []core.Usage{{RunID: ref.RunID, InputTokens: 100, OutputTokens: 20, Cost: a.cost}}, nil
}

// Every coding attempt, failed or not, books what its agent reported against
// the run's reservation as unverified spend, so a run that goes on to QA
// has already shown in the ledger what it cost.
func TestEachCodingAttemptBooksWhatItsAgentReported(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Fail(time.Minute, "tests fail"), fake.Patch(time.Minute, "")); err != nil {
			t.Fatal(err)
		}
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) { c.Agent = reportingAgent{Backend: f.agent, cost: 10_000} })
		claimed := tick(t, d)
		f.tickUntil(d, 10, func(r dispatcher.TickResult) bool { return len(r.Succeeded) == 1 })

		run := runRecord(t, d, f.record(claimed.Claimed[0]).RunID)
		if run.Spend.ReportedCost != 20_000 || run.Spend.Unpriced {
			t.Fatalf("the run's tally is %+v, want 20000 reported and every attempt priced", run.Spend)
		}
		if totals := f.totals(); totals.SpentUnverified != 20_000 || totals.SpentVerified != 0 {
			t.Fatalf("the ledger shows %+v, want 20000 unverified for two attempts", totals)
		}
	})
}

// An attempt whose agent reports no cost, or nothing at all, leaves the
// run unpriced: its tokens are booked, and the Owner's estimate stands for
// it when the run ends.
func TestAnAttemptThatReportsNoCostLeavesTheRunUnpriced(t *testing.T) {
	t.Parallel()
	for name, reporting := range map[string]bool{"tokens only": true, "no usage at all": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
				f := newFixture(t, db)
				f.confirm()
				issue := f.readyIssue(1)
				if err := f.agent.Script(agent.RoleCoder, issue, fake.Patch(time.Minute, "")); err != nil {
					t.Fatal(err)
				}
				d := f.dispatcher("worker-a", func(c *dispatcher.Config) {
					if reporting {
						c.Agent = reportingAgent{Backend: f.agent}
					}
				})
				claimed := tick(t, d)
				f.tickUntil(d, 10, func(r dispatcher.TickResult) bool { return len(r.Succeeded) == 1 })
				if run := runRecord(t, d, f.record(claimed.Claimed[0]).RunID); !run.Spend.Unpriced || run.Spend.ReportedCost != 0 {
					t.Fatalf("the run's tally is %+v, want it unpriced", run.Spend)
				}
			})
		})
	}
}

// A run that will start no more coding attempts gives its reservation up.
// When all of its work was priced the reservation is released and the
// booked usage stands; when some was not, the part of the Owner's estimate
// the reported cost does not cover is booked as an unverified estimate and
// the reservation is settled. A capped reservation is never pushed over its
// amount, so closing never suspends it. A reservation already closed is
// left as it is.
func TestClosingARunsReservation(t *testing.T) {
	t.Parallel()
	const amount = core.Micros(1_000_000)
	for name, c := range map[string]struct {
		status     core.ReservationStatus
		reported   core.Micros
		unpriced   bool
		wantStatus core.ReservationStatus
		wantBooked core.Micros
	}{
		"every attempt priced":           {core.ReservationHeld, 300_000, false, core.ReservationReleased, 0},
		"some work unpriced":             {core.ReservationHeld, 300_000, true, core.ReservationSettled, 700_000},
		"unpriced but reported in full":  {core.ReservationHeld, 1_200_000, true, core.ReservationReleased, 0},
		"already settled":                {core.ReservationSettled, 0, true, core.ReservationSettled, 0},
		"already released, still priced": {core.ReservationReleased, 0, false, core.ReservationReleased, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
				f := newFixture(t, db)
				issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 1}
				gate := budget.NewGate(budget.Config{Clock: f.clock})
				ctx := context.Background()
				err := f.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
					if err := tx.PutReservation(ctx, core.Reservation{ID: "res-1", FactoryID: sqlstoretest.Factory, IssueID: issue, RunID: "run-1",
						Mode: core.BudgetCapped, Amount: amount, Status: c.status, IdempotencyKey: "claim/run-1", CreatedAt: f.clock.Now()}); err != nil {
						return err
					}
					return dispatcher.CloseReservationOfRun(ctx, gate, tx, "run-1", "res-1", dispatcher.RunSpend{ReportedCost: c.reported, Unpriced: c.unpriced})
				})
				if err != nil {
					t.Fatal(err)
				}
				if got := f.reservation("res-1").Status; got != c.wantStatus {
					t.Errorf("the reservation is %s, want %s", got, c.wantStatus)
				}
				if totals := f.totals(); totals.SpentUnverified != c.wantBooked || totals.SpentVerified != 0 {
					t.Errorf("closing booked %+v, want %d unverified", totals, c.wantBooked)
				}
			})
		})
	}
}

// totals reads the ledger's totals since the beginning.
func (f *fixture) totals() core.BudgetTotals {
	f.t.Helper()
	var totals core.BudgetTotals
	err := f.store.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		totals, err = tx.BudgetTotals(ctx, time.Time{})
		return err
	})
	if err != nil {
		f.t.Fatalf("read the ledger totals: %v", err)
	}
	return totals
}

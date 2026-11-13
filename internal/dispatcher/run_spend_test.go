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

// booked is one usage row of a run in the ledger before its close. A row
// dated before the reservation was made is written by the control zone,
// which may date its rows, standing for one written with plain SQL.
type booked struct {
	run    core.RunID
	cost   core.Micros
	tokens bool
	before bool
}

// A run that will start no more coding attempts gives its reservation up.
// What it spent is read from its rows in the ledger, never from the agent
// zone's record. When all of its work was priced the reservation is
// released and the booked usage stands; when some was not, as the ledger
// shows or as the run's records claim, the part of the Owner's estimate the
// booked cost does not cover is booked as an unverified estimate and the
// reservation is settled. A claim can only raise the charge: a run whose
// record is missing or says nothing is still charged for the unpriced usage
// the ledger shows, and only for what its booked cost does not cover. Usage
// of another run counts for nothing. A capped reservation is never pushed
// over its amount, so closing never suspends it. A reservation already
// closed is left as it is.
func TestClosingARunsReservation(t *testing.T) {
	t.Parallel()
	const amount = core.Micros(1_000_000)
	for name, c := range map[string]struct {
		status     core.ReservationStatus
		rows       []booked
		claimed    bool
		wantStatus core.ReservationStatus
		wantSpent  core.Micros
	}{
		"every attempt priced":           {core.ReservationHeld, []booked{{"run-1", 300_000, true, false}}, false, core.ReservationReleased, 300_000},
		"record says some work unpriced": {core.ReservationHeld, []booked{{"run-1", 300_000, true, false}}, true, core.ReservationSettled, amount},
		"ledger shows work unpriced, record hides it": {core.ReservationHeld,
			[]booked{{"run-1", 300_000, true, false}, {"run-1", 0, true, false}}, false, core.ReservationSettled, amount},
		"no record, priced usage booked": {core.ReservationHeld, []booked{{"run-1", 300_000, true, false}}, false, core.ReservationReleased, 300_000},
		"no record, part of the usage unpriced": {core.ReservationHeld,
			[]booked{{"run-1", 300_000, true, false}, {"run-1", 0, true, false}}, false, core.ReservationSettled, amount},
		"unpriced but booked in full": {core.ReservationHeld,
			[]booked{{"run-1", 1_200_000, true, false}, {"run-1", 0, true, false}}, true, core.ReservationReleased, 1_200_000},
		"nothing booked, record says unpriced": {core.ReservationHeld, nil, true, core.ReservationSettled, amount},
		"another run's usage counts for nothing": {core.ReservationHeld,
			[]booked{{"run-2", 1_200_000, true, false}}, true, core.ReservationSettled, 1_200_000 + amount},
		"another run's unpriced usage counts for nothing": {core.ReservationHeld,
			[]booked{{"run-2", 0, true, false}}, false, core.ReservationSettled, amount},
		"nothing booked, record says priced": {core.ReservationHeld, nil, false, core.ReservationSettled, amount},
		"a row dated before the reservation counts for nothing": {core.ReservationHeld,
			[]booked{{"run-1", 1_200_000, true, true}, {"run-1", 100, true, false}}, true, core.ReservationSettled, 1_200_000 + amount},
		"already settled":                {core.ReservationSettled, nil, true, core.ReservationSettled, 0},
		"already released, still priced": {core.ReservationReleased, nil, false, core.ReservationReleased, 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
				f := newFixture(t, db)
				issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 1}
				gate := budget.NewGate(budget.Config{Clock: f.clock})
				ctx := context.Background()
				usage := func(row booked) core.Usage {
					u := core.Usage{FactoryID: sqlstoretest.Factory, IssueID: issue, RunID: row.run, ReservationID: "res-" + string(row.run[len("run-"):]),
						Step: core.StepCoding, Cost: row.cost}
					if row.tokens {
						u.InputTokens, u.OutputTokens = 100, 20
					}
					if row.before {
						u.At = f.clock.Now().Add(-time.Hour)
					}
					return u
				}
				// The control plane and the supervisor close a run's
				// reservation; the agent zone changes none.
				control := db.Open(t, core.ZoneControl)
				err := control.Update(ctx, func(ctx context.Context, tx state.Tx) error {
					for _, row := range c.rows {
						if row.before {
							if err := tx.AppendUsage(ctx, usage(row)); err != nil {
								return err
							}
						}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				err = control.Update(ctx, func(ctx context.Context, tx state.Tx) error {
					for _, run := range []core.RunID{"run-1", "run-2"} {
						// A reservation is made held and moved on from there.
						r := core.Reservation{ID: "res-" + string(run[len("run-"):]), FactoryID: sqlstoretest.Factory,
							IssueID: issue, RunID: run, Mode: core.BudgetCapped, Amount: amount, Status: core.ReservationHeld,
							IdempotencyKey: "claim/" + string(run), CreatedAt: f.clock.Now()}
						statuses := []core.ReservationStatus{core.ReservationHeld}
						if run == "run-1" {
							statuses = append(statuses, c.status)
						}
						for _, status := range statuses {
							r.Status = status
							if err := tx.PutReservation(ctx, r); err != nil {
								return err
							}
						}
					}
					for _, row := range c.rows {
						if !row.before {
							if err := tx.AppendUsage(ctx, usage(row)); err != nil {
								return err
							}
						}
					}
					return dispatcher.CloseReservationOfRun(ctx, gate, tx, "run-1", "res-1", c.claimed)
				})
				if err != nil {
					t.Fatal(err)
				}
				if got := f.reservation("res-1").Status; got != c.wantStatus {
					t.Errorf("the reservation is %s, want %s", got, c.wantStatus)
				}
				if totals := f.totals(); totals.SpentUnverified != c.wantSpent || totals.SpentVerified != 0 {
					t.Errorf("the ledger shows %+v after the close, want %d unverified", totals, c.wantSpent)
				}
			})
		})
	}
}

// unreadableLedger is a transaction whose zone may append to the ledger but
// not read it; reads counts how often the run's usage was asked for.
type unreadableLedger struct {
	state.Tx
	reads *int
}

func (l unreadableLedger) RunUsage(context.Context, core.RunID, time.Time) (core.RunUsage, error) {
	*l.reads++
	return core.RunUsage{}, core.NewError(core.CodePermissionDenied, "this zone may not read the ledger")
}

// A reservation of no amount is released without reading the ledger. A
// ledger the closing zone may not read does not fail the close and never
// lets the agent zone's figure decide: the whole amount is charged as an
// unverified estimate.
func TestClosingARunsReservationWithoutReadingTheLedger(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		amount     core.Micros
		wantReads  int
		wantStatus core.ReservationStatus
		wantSpent  core.Micros
	}{
		"nothing reserved":  {0, 0, core.ReservationReleased, 0},
		"ledger unreadable": {1_000_000, 1, core.ReservationSettled, 1_000_000},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
				f := newFixture(t, db)
				issue := core.IssueRef{Owner: "acme", Repo: "app", Number: 1}
				gate := budget.NewGate(budget.Config{Clock: f.clock})
				reads := 0
				err := f.store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
					if err := tx.PutReservation(ctx, core.Reservation{ID: "res-1", FactoryID: sqlstoretest.Factory, IssueID: issue, RunID: "run-1",
						Mode: core.BudgetUnlimited, Amount: c.amount, Status: core.ReservationHeld, IdempotencyKey: "claim/run-1", CreatedAt: f.clock.Now()}); err != nil {
						return err
					}
					return dispatcher.CloseReservationOfRun(ctx, gate, unreadableLedger{Tx: tx, reads: &reads}, "run-1", "res-1", false)
				})
				if err != nil {
					t.Fatalf("closing failed: %v", err)
				}
				if reads != c.wantReads {
					t.Errorf("the ledger was read %d times, want %d", reads, c.wantReads)
				}
				if got := f.reservation("res-1").Status; got != c.wantStatus {
					t.Errorf("the reservation is %s, want %s", got, c.wantStatus)
				}
				if totals := f.totals(); totals.SpentUnverified != c.wantSpent {
					t.Errorf("the ledger shows %+v, want %d unverified", totals, c.wantSpent)
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

package dispatcher_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// Until the Owner confirms the budget at install, the factory takes issues
// in but claims nothing: no lease, no reservation, no run, no agent.
func TestNoIssueIsClaimedUntilTheOwnerConfirmsTheBudget(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		issue := f.readyIssue(1)
		d := f.dispatcher("worker-a", nil)

		res := tick(t, d)
		if !core.HasCode(res.Held, core.CodeBudgetUnconfirmed) || len(res.Claimed) != 0 {
			t.Fatalf("an unconfirmed budget did not hold the claim: %+v", res)
		}
		if got := d.Health().Held; got != core.CodeBudgetUnconfirmed {
			t.Errorf("health reports %q as the hold, want %s", got, core.CodeBudgetUnconfirmed)
		}
		switch {
		case f.record(issue).State != core.StateReady:
			t.Errorf("the issue left Ready without a confirmed budget")
		case !f.issueLeaseIsFree(issue):
			t.Errorf("the refused claim left the issue lease held")
		case f.runRecords() != 0:
			t.Errorf("the refused claim left a run record")
		case len(f.agent.Started(issue)) != 0:
			t.Errorf("an agent was started without a confirmed budget")
		}
		var totals core.BudgetTotals
		if err := f.db.Open(t, core.ZoneControl).View(context.Background(), func(ctx context.Context, tx state.Tx) error {
			var err error
			totals, err = tx.BudgetTotals(ctx, time.Time{})
			return err
		}); err != nil || totals != (core.BudgetTotals{}) {
			t.Errorf("the refused claim left budget behind: %+v, %v", totals, err)
		}

		f.confirm()
		if res := tick(t, d); len(res.Claimed) != 1 || res.Held != nil {
			t.Fatalf("after the confirmation the issue was not claimed: %+v", res)
		}
		record := f.record(issue)
		run := runRecord(t, d, record.RunID)
		reservation := f.reservation(run.ReservationID)
		if reservation.Status != core.ReservationHeld || reservation.Mode != core.BudgetUnlimited ||
			reservation.RunID != record.RunID || reservation.HardCapEnforced {
			t.Errorf("the claim is not backed by a held, honest reservation: %+v", reservation)
		}
	})
}

func TestTheBudgetGateRefusesAnIncompleteOrUnsupportedConfirmation(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		store := db.Open(t, core.ZoneAgent)
		now := db.Clock.Now()
		valid := dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertThresholdMicros: 50_000_000, ConfirmedAt: now.Add(-time.Minute)}
		cases := map[string]struct {
			confirmation dispatcher.BudgetConfirmation
			err          error
			want         core.ErrorCode
		}{
			"absent":                         {err: core.NewError(core.CodeNotFound, "none"), want: core.CodeBudgetUnconfirmed},
			"store unavailable":              {err: core.NewError(core.CodeUnavailable, "down"), want: core.CodeUnavailable},
			"no alert decision in unlimited": {confirmation: dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, ConfirmedAt: now}, want: core.CodeBudgetUnconfirmed},
			"unknown mode":                   {confirmation: dispatcher.BudgetConfirmation{Mode: "free", AlertOff: true, ConfirmedAt: now}, want: core.CodeBudgetUnconfirmed},
			"no instant":                     {confirmation: dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true}, want: core.CodeBudgetUnconfirmed},
			"confirmed in the future":        {confirmation: dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: now.Add(time.Hour)}, want: core.CodeBudgetUnconfirmed},
			"negative threshold":             {confirmation: dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertThresholdMicros: -1, ConfirmedAt: now}, want: core.CodeBudgetUnconfirmed},
			"capped cannot be enforced here": {confirmation: dispatcher.BudgetConfirmation{Mode: core.BudgetCapped, AlertOff: true, ConfirmedAt: now}, want: core.CodeNotSupported},
			"complete":                       {confirmation: valid},
		}
		for name, c := range cases {
			gate, err := dispatcher.NewConfirmedBudgetGate(dispatcher.ConfirmationFunc(
				func(context.Context, core.BudgetLedger) (dispatcher.BudgetConfirmation, error) {
					return c.confirmation, c.err
				}), db.Clock)
			if err != nil {
				t.Fatal(err)
			}
			err = store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
				_, err := gate.Reserve(ctx, tx, request("run-1", "key-"+name))
				return err
			})
			if core.CodeOf(err) != c.want {
				t.Errorf("%s: want %q, got %v", name, c.want, err)
			}
		}
	})
}

func request(run core.RunID, key string) core.ReservationRequest {
	return core.ReservationRequest{FactoryID: sqlstoretest.Factory, IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 1},
		RunID: run, IdempotencyKey: key}
}

func TestTheStoredConfirmationIsReadThroughTheClaimTransaction(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		store := db.Open(t, core.ZoneAgent)
		gate, err := dispatcher.NewConfirmedBudgetGate(dispatcher.KVConfirmation{}, db.Clock)
		if err != nil {
			t.Fatal(err)
		}
		reserve := func(key string) error {
			return store.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
				_, err := gate.Reserve(ctx, tx, request("run-1", key))
				return err
			})
		}
		// The installer writes the confirmation, through the control zone.
		control := db.Open(t, core.ZoneControl)
		put := func(value []byte) {
			err := control.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
				entry, err := tx.GetKV(ctx, dispatcher.ConfirmationNamespace, dispatcher.ConfirmationKey)
				if err != nil && !core.HasCode(err, core.CodeNotFound) {
					return err
				}
				_, err = tx.PutKV(ctx, state.KVEntry{Namespace: dispatcher.ConfirmationNamespace, Key: dispatcher.ConfirmationKey,
					Value: value, Version: entry.Version})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := reserve("k1"); !core.HasCode(err, core.CodeBudgetUnconfirmed) {
			t.Fatalf("without a stored confirmation the gate must refuse, got %v", err)
		}
		put([]byte("{not json"))
		if err := reserve("k1"); !core.HasCode(err, core.CodeTampered) {
			t.Fatalf("a damaged confirmation must not admit a run, got %v", err)
		}
		raw, _ := json.Marshal(dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now()})
		put(raw)
		if err := reserve("k1"); err != nil {
			t.Fatalf("a stored confirmation was not honored: %v", err)
		}
	})
}

func TestReservationsAreIdempotentBoundToTheirRunAndSettleOrRelease(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		store := db.Open(t, core.ZoneAgent)
		gate, err := dispatcher.NewConfirmedBudgetGate(dispatcher.ConfirmationFunc(
			func(context.Context, core.BudgetLedger) (dispatcher.BudgetConfirmation, error) {
				return dispatcher.BudgetConfirmation{Mode: core.BudgetUnlimited, AlertOff: true, ConfirmedAt: db.Clock.Now()}, nil
			}), db.Clock)
		if err != nil {
			t.Fatal(err)
		}
		in := func(fn func(ctx context.Context, tx state.Tx) error) error {
			return store.Update(context.Background(), fn)
		}
		var first, again core.Reservation
		if err := in(func(ctx context.Context, tx state.Tx) error {
			var err error
			if first, err = gate.Reserve(ctx, tx, request("run-1", "k1")); err != nil {
				return err
			}
			again, err = gate.Reserve(ctx, tx, request("run-1", "k1"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if first.ID != again.ID || first.Status != core.ReservationHeld {
			t.Fatalf("a repeated reservation differs: %+v %+v", first, again)
		}
		if err := in(func(ctx context.Context, tx state.Tx) error {
			_, err := gate.Reserve(ctx, tx, request("run-2", "k1"))
			return err
		}); !core.HasCode(err, core.CodeConflict) {
			t.Fatalf("a key reused for another run must conflict, got %v", err)
		}
		usage := core.Usage{IssueID: first.IssueID, RunID: "run-9", Cost: 5}
		if err := in(func(ctx context.Context, tx state.Tx) error { return gate.Settle(ctx, tx, first.ID, usage) }); !core.HasCode(err, core.CodeConflict) {
			t.Fatalf("usage of another run must not settle the reservation, got %v", err)
		}
		usage.RunID = first.RunID
		if err := in(func(ctx context.Context, tx state.Tx) error { return gate.Settle(ctx, tx, first.ID, usage) }); err != nil {
			t.Fatal(err)
		}
		var settled core.Reservation
		var totals core.BudgetTotals
		// The agent zone may read the totals of its own factory.
		_ = db.Open(t, core.ZoneControl).View(context.Background(), func(ctx context.Context, tx state.Tx) error {
			settled, _ = tx.GetReservation(ctx, first.ID)
			totals, _ = tx.BudgetTotals(ctx, time.Time{})
			return nil
		})
		if settled.Status != core.ReservationSettled || totals.SpentUnverified != 5 {
			t.Fatalf("settling left %+v and totals %+v", settled, totals)
		}
		if err := in(func(ctx context.Context, tx state.Tx) error { return gate.Release(ctx, tx, first.ID) }); err != nil {
			t.Fatalf("releasing a settled reservation must change nothing, got %v", err)
		}
		if _, err := dispatcher.NewConfirmedBudgetGate(nil, nil); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Fatalf("a gate without a confirmation source must be refused, got %v", err)
		}
	})
}

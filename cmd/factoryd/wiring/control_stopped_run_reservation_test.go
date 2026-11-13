package wiring_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/handoff"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// codedRun opens a request whose coder delivers a patch and returns its
// number and run once the dispatcher recorded the patch: the issue is in
// Developing, its build waits for the supervisor, and the run's
// reservation is still held.
func codedRun(t *testing.T, f *harness.Factory, title string) (int, dispatcher.RunRecord) {
	t.Helper()
	number := f.OpenIssue("maintainer", "MEMBER", title, bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, time.Minute, "diff --git a/x b/x\n")
	for range 12 {
		f.Pass()
		f.Tick()
		if runs := f.Runs(number); len(runs) == 1 && runs[0].Status == dispatcher.RunSucceeded {
			if got := f.Record(number).State; got != core.StateDeveloping {
				t.Fatalf("the coded issue is %s, want Developing", got)
			}
			if r := reservationOf(t, f, runs[0].ReservationID); r.Status != core.ReservationHeld {
				t.Fatalf("the coded run's reservation is %s, want held", r.Status)
			}
			return number, runs[0]
		}
	}
	t.Fatal("the run did not record its patch")
	return 0, dispatcher.RunRecord{}
}

func reservationOf(t *testing.T, f *harness.Factory, id string) core.Reservation {
	t.Helper()
	var r core.Reservation
	err := f.ControlStore.View(context.Background(), func(ctx context.Context, tx state.Tx) error {
		var err error
		r, err = tx.GetReservation(ctx, id)
		return err
	})
	if err != nil {
		t.Fatalf("read reservation %s: %v", id, err)
	}
	return r
}

// forgeRun rewrites the dispatcher's record of run as the agent zone, which
// owns it, could.
func forgeRun(t *testing.T, f *harness.Factory, run dispatcher.RunRecord) {
	t.Helper()
	err := f.AgentStore.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		entry, err := tx.GetKV(ctx, "dispatcher", "run/"+string(run.RunID))
		if err != nil {
			return err
		}
		if entry.Value, err = json.Marshal(run); err != nil {
			return err
		}
		_, err = tx.PutKV(ctx, entry)
		return err
	})
	if err != nil {
		t.Fatalf("forge the record of run %s: %v", run.RunID, err)
	}
}

// A run record marked exhausted blocks its issue, and the block closes the
// run's reservation with it: the build of the run will never run, so
// nothing else would, and a reservation left held counts against the
// budget cap for ever. Here the agent zone forged the mark on a run whose
// patch waits for its build.
func TestBlockingAStoppedRunClosesItsReservation(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number, run := codedRun(t, f, "Greet the world")
	run.Status = dispatcher.RunExhausted
	forgeRun(t, f, run)
	f.Pass()
	if got := f.Record(number).State; got != core.StateBlocked {
		t.Fatalf("the issue of a run marked exhausted is %s, want Blocked", got)
	}
	if r := reservationOf(t, f, run.ReservationID); r.Status == core.ReservationHeld || r.Status == core.ReservationSuspended {
		t.Fatalf("the reservation of the blocked run is still %s", r.Status)
	}
}

// A run record naming the reservation of another run cannot close it, nor
// keep the run's own reservation held: the block closes the reservation
// derived from the run's ID, whatever the record names, and the other
// run's reservation stays held.
func TestARunRecordCannotCloseAnotherRunsReservation(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	_, victim := codedRun(t, f, "Greet the world")
	number, run := codedRun(t, f, "Say goodbye")
	own := run.ReservationID
	run.Status, run.ReservationID = dispatcher.RunExhausted, victim.ReservationID
	forgeRun(t, f, run)
	_ = f.Control.Pass(context.Background())
	if r := reservationOf(t, f, victim.ReservationID); r.Status != core.ReservationHeld {
		t.Fatalf("a record naming another run's reservation closed it: %s", r.Status)
	}
	if r := reservationOf(t, f, own); r.Status == core.ReservationHeld || r.Status == core.ReservationSuspended {
		t.Fatalf("a record naming another run's reservation left the run's own %s", r.Status)
	}
	if got := f.Record(number).State; got != core.StateBlocked {
		t.Fatalf("the issue of a run marked exhausted is %s, want Blocked", got)
	}
}

// holdSuperseded holds the build of run as the supervisor does when the
// policy in force changed under it.
func holdSuperseded(t *testing.T, f *harness.Factory, run dispatcher.RunRecord, attempt int) {
	t.Helper()
	err := f.ControlStore.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
		_, err := handoff.PutBuildOutage(ctx, tx, handoff.BuildOutage{RunID: run.RunID, IssueID: run.Issue, Attempt: attempt,
			Reason: handoff.HeldPolicySuperseded, Held: true, Holds: 1, HeldAt: f.Clock.Now(), UpdatedAt: f.Clock.Now()}, 0)
		return err
	})
	if err != nil {
		t.Fatalf("hold the build of run %s: %v", run.RunID, err)
	}
}

// The block of a run whose policy was superseded closes the run's own
// reservation, derived from the run's ID on the lifecycle record, whether
// or not the dispatcher's record of the run is there and names that run:
// the record is the agent zone's and cannot keep the reservation held, nor,
// by going missing, release it at what the ledger shows.
func TestASupersededRunsReservationClosesWithoutItsRunRecord(t *testing.T) {
	t.Parallel()
	// rewrite replaces the stored record of run, as the agent zone, which
	// owns it, could; it may not delete it, and a record it cannot read is
	// taken for none, as a missing one is.
	rewrite := func(t *testing.T, f *harness.Factory, run dispatcher.RunRecord, value []byte) {
		t.Helper()
		err := f.AgentStore.Update(context.Background(), func(ctx context.Context, tx state.Tx) error {
			entry, err := tx.GetKV(ctx, "dispatcher", "run/"+string(run.RunID))
			if err != nil {
				return err
			}
			entry.Value = value
			_, err = tx.PutKV(ctx, entry)
			return err
		})
		if err != nil {
			t.Fatalf("rewrite the record of run %s: %v", run.RunID, err)
		}
	}
	encoded := func(t *testing.T, run dispatcher.RunRecord) []byte {
		raw, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for name, value := range map[string]func(t *testing.T, run dispatcher.RunRecord) []byte{
		"the run record cannot be read": func(*testing.T, dispatcher.RunRecord) []byte { return []byte("not a run record") },
		"the run record names another run": func(t *testing.T, run dispatcher.RunRecord) []byte {
			run.RunID = "run-elsewhere"
			return encoded(t, run)
		},
		"the run record names another issue": func(t *testing.T, run dispatcher.RunRecord) []byte {
			run.Issue.Number += 1000
			return encoded(t, run)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := harness.New(t, harness.Options{})
			number, run := codedRun(t, f, "Greet the world")
			holdSuperseded(t, f, run, f.Record(number).Attempt)
			rewrite(t, f, run, value(t, run))
			f.Pass()
			if got := f.Record(number).State; got != core.StateBlocked {
				t.Fatalf("the issue of a superseded run is %s, want Blocked", got)
			}
			// Without a record of the run, what it ran is unknown, so its
			// reservation is charged in full rather than released.
			if r := reservationOf(t, f, run.ReservationID); r.Status != core.ReservationSettled {
				t.Fatalf("the reservation of the blocked run is %s, want %s: a run with no record of its own is charged in full",
					r.Status, core.ReservationSettled)
			}
		})
	}
}

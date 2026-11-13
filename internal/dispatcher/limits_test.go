package dispatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// The budget is unlimited in every test here: the limits on concurrency,
// time and attempts hold anyway.

func TestTheConcurrencyCapHoldsWithAnUnlimitedBudget(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		first, second := f.readyIssue(1), f.readyIssue(2)
		for _, issue := range []core.IssueRef{first, second} {
			if err := f.agent.Script(agent.RoleCoder, issue, fake.Hang()); err != nil {
				t.Fatal(err)
			}
		}
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) { c.Limits.MaxConcurrent = 1 })

		res := tick(t, d)
		if len(res.Claimed) != 1 || res.Claimed[0] != first || !core.HasCode(res.Held, core.CodeLimitExceeded) {
			t.Fatalf("with a cap of one the dispatcher claimed %v and held %v", res.Claimed, res.Held)
		}
		for range 3 {
			f.clock.Advance(pollInterval)
			if res := tick(t, d); len(res.Claimed) != 0 || !core.HasCode(res.Held, core.CodeLimitExceeded) {
				t.Fatalf("the cap let another claim through: %+v", res)
			}
		}
		if state := f.record(second).State; state != core.StateReady {
			t.Fatalf("the second issue is %s, want still Ready", state)
		}
		if !f.issueLeaseIsFree(second) || len(f.agent.Started(second)) != 0 {
			t.Fatalf("the held issue was leased or started")
		}
	})
}

func TestAHangingAttemptIsCancelledAtTheRunTimeout(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Hang(), fake.Patch(time.Minute, "")); err != nil {
			t.Fatal(err)
		}
		timeout := 10 * time.Minute
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) { c.Limits.RunTimeout = timeout })
		tick(t, d)
		run := f.record(issue).RunID

		ticks := 0
		f.tickUntil(d, 40, func(dispatcher.TickResult) bool {
			ticks++
			return len(f.agent.Started(issue)) == 2
		})
		if elapsed := time.Duration(ticks) * pollInterval; elapsed < timeout {
			t.Fatalf("the attempt was stopped after %s, before the timeout of %s", elapsed, timeout)
		}
		record := runRecord(t, d, run)
		if len(record.Failures) != 1 || record.Failures[0].Code != core.CodeTimeout || record.Attempt != 2 {
			t.Fatalf("the timed-out attempt was not counted: %+v", record)
		}
		hung := agent.RunRef{Backend: "fake", ID: "fake-1", RunID: run}
		if status, err := f.agent.Status(context.Background(), hung); err != nil || status.Phase != agent.PhaseCancelled {
			t.Fatalf("the hanging agent run was not cancelled: %+v, %v", status, err)
		}
		f.tickUntil(d, 10, func(r dispatcher.TickResult) bool { return len(r.Succeeded) == 1 })
	})
}

func TestTheAttemptLimitStopsARunWithAnUnlimitedBudget(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		// The patch after the limit must never run.
		if err := f.agent.Script(agent.RoleCoder, issue,
			fake.Fail(time.Minute, "tests failed"), fake.Crash(time.Minute), fake.Fail(time.Minute, "tests failed"),
			fake.Patch(0, "")); err != nil {
			t.Fatal(err)
		}
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) { c.Limits.MaxAttempts = 3; c.Limits.MaxConcurrent = 1 })
		tick(t, d)
		run := f.record(issue).RunID
		f.tickUntil(d, 20, func(r dispatcher.TickResult) bool { return len(r.Exhausted) == 1 })

		record := runRecord(t, d, run)
		switch {
		case record.Status != dispatcher.RunExhausted || record.Attempt != 3 || len(record.Failures) != 3:
			t.Fatalf("the run was not stopped after three attempts: %+v", record)
		case record.Failures[1].Code != core.CodeUnavailable:
			t.Errorf("the crashed attempt was recorded as %s", record.Failures[1].Code)
		case f.reservation(record.ReservationID).Status != core.ReservationHeld:
			// The control plane closes it when it blocks the issue; the agent
			// zone changes no reservation.
			t.Errorf("the dispatcher closed the stopped run's reservation itself")
		case !f.issueLeaseIsFree(issue):
			t.Errorf("the stopped run kept its issue lease")
		}
		for range 5 {
			f.clock.Advance(pollInterval)
			tick(t, d)
		}
		if started := len(f.agent.Started(issue)); started != 3 {
			t.Fatalf("the agent was started %d times, want exactly the 3 allowed", started)
		}
		// A stopped run no longer counts against the concurrency cap.
		next := f.readyIssue(2)
		if res := tick(t, d); len(res.Claimed) != 1 || res.Claimed[0] != next {
			t.Fatalf("the next issue was not claimed after the run stopped: %+v", res)
		}
	})
}

func TestAnIssueWithoutAUsableBriefIsNotClaimed(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		unapproved, incomplete := f.readyIssue(1), f.readyIssue(2)
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) {
			c.Briefs = dispatcher.BriefFunc(func(_ context.Context, issue core.IssueRef) (dispatcher.Brief, error) {
				if issue == unapproved {
					return dispatcher.Brief{}, core.NewError(core.CodeApprovalRequired, "scope not approved")
				}
				b := brief()
				b.Handoff.DoD = ""
				return b, nil
			})
		})
		res, err := d.Tick(context.Background())
		if err != nil || len(res.Claimed) != 0 || len(res.Errors) != 2 {
			t.Fatalf("issues without a usable brief were claimed or not reported: %+v, %v", res, err)
		}
		for _, issue := range []core.IssueRef{unapproved, incomplete} {
			if f.record(issue).State != core.StateReady || !f.issueLeaseIsFree(issue) {
				t.Errorf("%s was touched without a usable brief", issue)
			}
		}
	})
}

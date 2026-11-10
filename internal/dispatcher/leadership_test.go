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

func TestOnlyTheDispatcherHoldingTheLeaseClaims(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		active := f.dispatcher("worker-a", nil)
		standby := f.dispatcher("worker-b", nil)
		if res := tick(t, active); !res.Leader {
			t.Fatalf("the first dispatcher did not become active")
		}
		issue := f.readyIssue(1)

		for range 3 {
			f.clock.Advance(pollInterval)
			if res := tick(t, standby); res.Leader || len(res.Claimed) != 0 {
				t.Fatalf("a dispatcher claimed while another held the lease: %+v", res)
			}
			// The active one renews its lease and does the work.
			tick(t, active)
		}
		if record := f.record(issue); record.State != core.StateDeveloping || record.LeaseOwner != "worker-a" {
			t.Fatalf("the issue was not worked by the active dispatcher: %+v", record)
		}
		if standby.Health().Leader || !active.Health().Leader {
			t.Fatalf("health disagrees about who is active: %+v %+v", standby.Health(), active.Health())
		}

		// The active one goes quiet; after its lease lapses the standby takes over.
		f.clock.Advance(leaseTTL)
		if res := tick(t, standby); !res.Leader {
			t.Fatalf("the standby did not take over a lapsed dispatcher lease")
		}
		if res := tick(t, active); res.Leader {
			t.Fatalf("the old dispatcher still believes it is active")
		}
	})
}

func TestTheStandbyClaimsNothingEvenWhenIssuesWait(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		active := f.dispatcher("worker-a", nil)
		standby := f.dispatcher("worker-b", nil)
		// The active dispatcher takes the lease before any issue is Ready, and
		// does not tick again while the standby tries.
		tick(t, active)
		issue := f.readyIssue(1)
		if res := tick(t, standby); res.Leader || len(res.Claimed) != 0 {
			t.Fatalf("the standby claimed while the lease was held: %+v", res)
		}
		if f.record(issue).State != core.StateReady || !f.issueLeaseIsFree(issue) {
			t.Fatalf("the issue was touched by the standby")
		}
		f.clock.Advance(leaseTTL)
		if res := tick(t, standby); !res.Leader || len(res.Claimed) != 1 {
			t.Fatalf("after taking over the lease the standby did not claim: %+v", res)
		}
	})
}

func TestRunTicksOnTheClockAndGivesItsLeasesUpWhenStopped(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Hang()); err != nil {
			t.Fatal(err)
		}
		d := f.dispatcher("worker-a", nil)
		ticks := make(chan dispatcher.TickResult)
		dispatcher.SetTickHook(d, func(res dispatcher.TickResult) { ticks <- res })
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- d.Run(ctx) }()

		if res := <-ticks; len(res.Claimed) != 1 {
			t.Fatalf("the first tick claimed %v", res.Claimed)
		}
		f.clock.Advance(pollInterval)
		if res := <-ticks; !res.Leader {
			t.Fatalf("the second tick was not active")
		}
		cancel()
		f.clock.Advance(pollInterval)
		// A tick may still be under way when the context ends; drain it.
		for {
			select {
			case <-ticks:
				continue
			case err := <-done:
				if err != nil {
					t.Fatalf("run ended with %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("run did not stop after its context ended")
			}
			break
		}
		if !f.issueLeaseIsFree(issue) {
			t.Fatal("a stopped dispatcher kept the issue lease")
		}
		successor := f.dispatcher("worker-b", nil)
		if res := tick(t, successor); !res.Leader || len(res.Resumed) != 1 {
			t.Fatalf("a successor had to wait for leases a stopped dispatcher gave up: %+v", res)
		}
	})
}

func TestAMisconfiguredDispatcherIsRefused(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		cases := map[string]func(*dispatcher.Config){
			"no owner":                   func(c *dispatcher.Config) { c.Owner = "" },
			"no store":                   func(c *dispatcher.Config) { c.State = nil },
			"no agent":                   func(c *dispatcher.Config) { c.Agent = nil },
			"no budget gate":             func(c *dispatcher.Config) { c.Budget = nil },
			"no brief source":            func(c *dispatcher.Config) { c.Briefs = nil },
			"no run timeout":             func(c *dispatcher.Config) { c.Limits.RunTimeout = 0 },
			"no attempt limit":           func(c *dispatcher.Config) { c.Limits.MaxAttempts = 0 },
			"no concurrency cap":         func(c *dispatcher.Config) { c.Limits.MaxConcurrent = 0 },
			"negative estimate":          func(c *dispatcher.Config) { c.Estimate = -1 },
			"poll slower than the lease": func(c *dispatcher.Config) { c.PollInterval = c.LeaseTTL },
		}
		for name, mutate := range cases {
			cfg := f.config("worker-a", f.store)
			mutate(&cfg)
			if _, err := dispatcher.New(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
				t.Errorf("%s: want INVALID_ARGUMENT, got %v", name, err)
			}
		}
	})
}

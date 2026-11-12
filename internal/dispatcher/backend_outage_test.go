package dispatcher_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// flakyBackend is the fake backend behind a link that answers status calls
// with UNAVAILABLE while down is set, as a remote backend does during a
// short outage. The run itself goes on behind it.
type flakyBackend struct {
	*fake.Backend
	mu   sync.Mutex
	down bool
}

func (b *flakyBackend) setDown(down bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.down = down
}

func (b *flakyBackend) Status(ctx context.Context, ref agent.RunRef) (agent.Status, error) {
	b.mu.Lock()
	down := b.down
	b.mu.Unlock()
	if down {
		return agent.Status{}, core.NewError(core.CodeUnavailable, "the agent backend cannot be reached")
	}
	return b.Backend.Status(ctx, ref)
}

func TestABriefBackendOutageSpendsNoAttempt(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Patch(10*time.Minute, "")); err != nil {
			t.Fatal(err)
		}
		backend := &flakyBackend{Backend: f.agent}
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) { c.Agent = backend })
		tick(t, d)
		run := f.record(issue).RunID

		// The backend cannot be reached for less than a lease lifetime.
		backend.setDown(true)
		for range 3 {
			f.clock.Advance(pollInterval)
			if res := tick(t, d); len(res.Failed) != 0 || len(res.Lost) != 0 {
				t.Fatalf("a status call the backend could not answer was counted: %+v", res)
			}
		}
		backend.setDown(false)
		f.tickUntil(d, 40, func(r dispatcher.TickResult) bool { return len(r.Succeeded) == 1 })

		record := runRecord(t, d, run)
		if record.Attempt != 1 || len(record.Failures) != 0 || len(f.agent.Started(issue)) != 1 {
			t.Fatalf("the outage cost an attempt: attempt %d, failures %+v", record.Attempt, record.Failures)
		}
	})
}

func TestABackendThatStaysUnreachableLosesTheAttempt(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Hang(), fake.Patch(time.Minute, "")); err != nil {
			t.Fatal(err)
		}
		backend := &flakyBackend{Backend: f.agent}
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) { c.Agent = backend })
		tick(t, d)
		run := f.record(issue).RunID

		backend.setDown(true)
		ticks := 0
		f.tickUntil(d, 20, func(r dispatcher.TickResult) bool {
			ticks++
			return len(r.Failed) == 1
		})
		if waited := time.Duration(ticks) * pollInterval; waited < leaseTTL {
			t.Fatalf("the attempt was given up after %s of outage, before the lease lifetime of %s", waited, leaseTTL)
		}
		record := runRecord(t, d, run)
		if len(record.Failures) != 1 || record.Failures[0].Code != core.CodeUnavailable {
			t.Fatalf("the unreachable attempt was not recorded as UNAVAILABLE: %+v", record.Failures)
		}
	})
}

func TestARunTheBackendNoLongerKnowsIsLostAtOnce(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Hang(), fake.Patch(time.Minute, "")); err != nil {
			t.Fatal(err)
		}
		forgetful := &forgettingBackend{Backend: f.agent}
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) { c.Agent = forgetful })
		tick(t, d)
		run := f.record(issue).RunID

		forgetful.forget = true
		f.clock.Advance(pollInterval)
		res := tick(t, d)
		if len(res.Failed) != 1 || res.Failed[0].Code != core.CodeNotFound {
			t.Fatalf("a run the backend lost was not counted at the next tick: %+v", res)
		}
		if record := runRecord(t, d, run); record.Attempt != 2 {
			t.Fatalf("the next attempt did not start after the lost run: %+v", record)
		}
	})
}

// forgettingBackend answers NOT_FOUND for the first run once forget is set:
// the backend lost it.
type forgettingBackend struct {
	*fake.Backend
	forget bool
}

func (b *forgettingBackend) Status(ctx context.Context, ref agent.RunRef) (agent.Status, error) {
	if b.forget && ref.ID == "fake-1" {
		return agent.Status{}, core.NewError(core.CodeNotFound, "the backend has no such run")
	}
	return b.Backend.Status(ctx, ref)
}

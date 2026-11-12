package dispatcher_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/internal/dispatcher"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

// backendFault is the error a failingBackend answers status calls with.
const backendFault = "the agent backend failed internally"

// failingBackend is the fake backend answering status calls with INTERNAL
// while failing is set, as a backend with a fault of its own does.
type failingBackend struct {
	*fake.Backend
	mu      sync.Mutex
	failing bool
}

func (b *failingBackend) setFailing(failing bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failing = failing
}

func (b *failingBackend) Status(ctx context.Context, ref agent.RunRef) (agent.Status, error) {
	b.mu.Lock()
	failing := b.failing
	b.mu.Unlock()
	if failing {
		return agent.Status{}, core.NewError(core.CodeInternal, backendFault)
	}
	return b.Backend.Status(ctx, ref)
}

// tickTolerating ticks d and fails on any error other than the backend's
// own status fault, which a tick reports while it waits the fault out.
func tickTolerating(t *testing.T, d *dispatcher.Dispatcher) dispatcher.TickResult {
	t.Helper()
	res, err := d.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	for _, e := range res.Errors {
		if !strings.Contains(e.Error(), backendFault) {
			t.Fatalf("tick reported errors: %v", res.Errors)
		}
	}
	return res
}

// A backend whose status calls keep failing with an error other than
// UNAVAILABLE or NOT_FOUND does not hold a run forever: each attempt is
// given up once the backend kept failing for a lease lifetime, recorded
// with the backend's last error, and the run ends once its attempts are
// used.
func TestABackendWhoseStatusKeepsFailingEndsTheRun(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Hang(), fake.Hang(), fake.Hang()); err != nil {
			t.Fatal(err)
		}
		backend := &failingBackend{Backend: f.agent}
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) { c.Agent = backend })
		tick(t, d)
		run := f.record(issue).RunID
		backend.setFailing(true)

		ticks, firstFailure := 0, 0
		for exhausted := false; !exhausted; {
			if ticks++; ticks > 60 {
				t.Fatal("a backend whose status keeps failing held the run")
			}
			f.clock.Advance(pollInterval)
			res := tickTolerating(t, d)
			if len(res.Failed) > 0 && firstFailure == 0 {
				firstFailure = ticks
			}
			exhausted = len(res.Exhausted) == 1
		}
		if waited := time.Duration(firstFailure) * pollInterval; waited < leaseTTL {
			t.Fatalf("the first attempt was given up after %s of failures, before the lease lifetime of %s", waited, leaseTTL)
		}
		record := runRecord(t, d, run)
		if record.Status != dispatcher.RunExhausted || len(record.Failures) != 3 {
			t.Fatalf("the run is %s with failures %+v, want it exhausted after 3", record.Status, record.Failures)
		}
		for _, failure := range record.Failures {
			if failure.Code != core.CodeInternal || !strings.Contains(failure.Detail, backendFault) {
				t.Errorf("a failure %+v does not carry the backend's last error", failure)
			}
		}
	})
}

// A status call failing once costs no attempt, and the wait for a failing
// backend starts over once it answers again.
func TestABriefBackendStatusFaultSpendsNoAttempt(t *testing.T) {
	t.Parallel()
	sqlstoretest.Run(t, func(t *testing.T, db *sqlstoretest.Database) {
		f := newFixture(t, db)
		f.confirm()
		issue := f.readyIssue(1)
		if err := f.agent.Script(agent.RoleCoder, issue, fake.Patch(10*time.Minute, "")); err != nil {
			t.Fatal(err)
		}
		backend := &failingBackend{Backend: f.agent}
		d := f.dispatcher("worker-a", func(c *dispatcher.Config) { c.Agent = backend })
		tick(t, d)
		run := f.record(issue).RunID

		// Faults of less than a lease lifetime each, with answers between
		// them, add up to more than one but never cost the attempt.
		for range 3 {
			backend.setFailing(true)
			for range 3 {
				f.clock.Advance(pollInterval)
				if res := tickTolerating(t, d); len(res.Failed) != 0 {
					t.Fatalf("a passing status fault was counted: %+v", res)
				}
			}
			backend.setFailing(false)
			f.clock.Advance(pollInterval)
			tick(t, d)
		}
		f.tickUntil(d, 40, func(r dispatcher.TickResult) bool { return len(r.Succeeded) == 1 })
		if record := runRecord(t, d, run); record.Attempt != 1 || len(record.Failures) != 0 {
			t.Fatalf("passing status faults cost an attempt: attempt %d, failures %+v", record.Attempt, record.Failures)
		}
	})
}

package install

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// stepClock moves only when the test moves it, and tells the test when
// something started to wait on it, so no test has to sleep.
type stepClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []stepTimer
	armed  chan struct{}
}

type stepTimer struct {
	due time.Time
	ch  chan time.Time
}

func newStepClock() *stepClock { return &stepClock{now: fixedStart, armed: make(chan struct{}, 256)} }

func (c *stepClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *stepClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, stepTimer{due: c.now.Add(d), ch: ch})
	c.armed <- struct{}{}
	return ch
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.timers[:0]
	for _, t := range c.timers {
		if !t.due.After(c.now) {
			t.ch <- c.now
		} else {
			keep = append(keep, t)
		}
	}
	c.timers = keep
}

// waitArmed blocks until something waits on the clock.
func (c *stepClock) waitArmed(t *testing.T) {
	t.Helper()
	select {
	case <-c.armed:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing waited on the clock")
	}
}

func nextProc(t *testing.T, e *fakeExec) *fakeProc {
	t.Helper()
	select {
	case p := <-e.startCh:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no program was started")
		return nil
	}
}

func runGroup(t *testing.T, g *group) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	return func() error { cancel(); return <-done }
}

func testGroup(e *fakeExec, c *stepClock, names ...string) *group {
	var units []unit
	for _, n := range names {
		units = append(units, unit{name: n, cmd: Cmd{Name: n}})
	}
	return newGroup(e, c, units, func(string, ...any) {})
}

func TestTheGroupStartsEveryUnitAndStopsThemAllTogether(t *testing.T) {
	t.Parallel()
	e, c := newFakeExec(), newStepClock()
	stop := runGroup(t, testGroup(e, c, "app", "control", "dispatcher"))
	procs := map[string]*fakeProc{}
	for range 3 {
		p := nextProc(t, e)
		procs[p.cmd.Name] = p
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	for name, p := range procs {
		if !p.terminated || p.killed {
			t.Errorf("%s: terminated=%v killed=%v, want a clean stop", name, p.terminated, p.killed)
		}
	}
}

func TestAUnitThatDiesIsStartedAgainAfterABackoffThatGrows(t *testing.T) {
	t.Parallel()
	e, c := newFakeExec(), newStepClock()
	stop := runGroup(t, testGroup(e, c, "control"))
	first := nextProc(t, e)
	first.exit(errors.New("crash"))
	c.waitArmed(t)
	select {
	case <-e.startCh:
		t.Fatal("restarted before the backoff elapsed")
	default:
	}
	c.Advance(restartBase)
	second := nextProc(t, e)
	second.exit(errors.New("crash"))
	c.waitArmed(t)
	c.Advance(restartBase) // the second wait is twice as long, so this is not enough
	select {
	case <-e.startCh:
		t.Fatal("the backoff did not grow")
	default:
	}
	c.Advance(restartBase)
	nextProc(t, e)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestAUnitThatIgnoresTheStopRequestIsKilledAfterTheGracePeriod(t *testing.T) {
	t.Parallel()
	e, c := newFakeExec(), newStepClock()
	g := testGroup(e, c, "dispatcher")
	stop := runGroup(t, g)
	p := nextProc(t, e)
	p.ignoreTerm = true
	done := make(chan error, 1)
	go func() { done <- stop() }()
	c.waitArmed(t) // the grace period began
	c.Advance(g.grace)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !p.terminated || !p.killed {
		t.Fatalf("terminated=%v killed=%v, want a request and then a kill", p.terminated, p.killed)
	}
}

func TestTheGroupGivesUpWhenAProgramCannotBeStartedAtAll(t *testing.T) {
	t.Parallel()
	e, c := newFakeExec(), newStepClock()
	e.failStart = "control"
	g := testGroup(e, c, "control")
	ctx := t.Context()
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	for range maxStartFailures - 1 {
		c.waitArmed(t)
		c.Advance(restartMax)
	}
	if err := <-done; err == nil {
		t.Fatal("Run returned nil though control never started")
	}
}

func TestRestartStopsEveryUnitAndStartsThemAgain(t *testing.T) {
	t.Parallel()
	e, c := newFakeExec(), newStepClock()
	g := testGroup(e, c, "app", "control")
	stop := runGroup(t, g)
	first := []*fakeProc{nextProc(t, e), nextProc(t, e)}
	g.Restart()
	second := []*fakeProc{nextProc(t, e), nextProc(t, e)}
	for _, p := range first {
		if !p.terminated {
			t.Errorf("%s kept running through the restart", p.cmd.Name)
		}
	}
	if len(second) != 2 {
		t.Fatal("not every unit came back")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if len(e.startedNames()) != 4 {
		t.Fatalf("started %v, want each unit twice", e.startedNames())
	}
}

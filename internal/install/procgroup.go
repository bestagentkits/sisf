package install

import (
	"context"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Pacing of the process group. A program that dies is started again after
// a wait that doubles from restartBase up to restartMax; one that ran for
// stableAfter is forgiven, so a crash after hours starts again at once.
const (
	restartBase      = time.Second
	restartMax       = 30 * time.Second
	stableAfter      = time.Minute
	maxStartFailures = 5
)

// unit is one program of a Standalone install.
type unit struct {
	name string
	cmd  Cmd
}

// group runs the programs of a Standalone install and keeps them running.
// It is the foreground half of `sisf up --mode standalone`: one process the
// service manager can watch, owning every unit of the factory, so the three
// zones start, stop and restart together.
type group struct {
	exec    Exec
	clock   core.Clock
	units   []unit
	grace   time.Duration
	logf    func(format string, args ...any)
	restart chan struct{}
	// record, when set, records each program as it starts, so it can be
	// found and stopped after this process ended; it returns the function
	// that forgets the record once the program has stopped. A program that
	// cannot be recorded is stopped again and counts as one that failed
	// to start.
	record func(name string, p Process) (func(), error)
}

func newGroup(exec Exec, clock core.Clock, units []unit, logf func(string, ...any)) *group {
	return &group{exec: exec, clock: clock, units: units, grace: gracePeriod, logf: logf, restart: make(chan struct{}, 1)}
}

// Restart stops every program and starts them again. Requests that arrive
// while one restart is pending are folded into it.
func (g *group) Restart() {
	select {
	case g.restart <- struct{}{}:
	default:
	}
}

// Run starts every unit and returns when ctx ends, after all of them were
// stopped, or at once when a program cannot be started at all.
func (g *group) Run(ctx context.Context) error {
	for {
		gctx, cancel := context.WithCancel(ctx)
		fatal := make(chan error, len(g.units))
		var wg sync.WaitGroup
		for _, u := range g.units {
			wg.Add(1)
			go func() {
				defer wg.Done()
				g.supervise(gctx, u, fatal)
			}()
		}
		var result error
		again := false
		select {
		case <-ctx.Done():
		case result = <-fatal:
		case <-g.restart:
			again = true
			g.logf("restarting every unit\n")
		}
		cancel()
		wg.Wait()
		if result != nil || !again {
			return result
		}
	}
}

// supervise keeps one program running until ctx ends.
func (g *group) supervise(ctx context.Context, u unit, fatal chan<- error) {
	delay, failures := restartBase, 0
	for {
		p, err := g.exec.Start(ctx, u.cmd)
		forget := func() {}
		if err == nil && g.record != nil {
			if forget, err = g.record(u.name, p); err != nil {
				_ = p.Kill() // it could not be found again once this process ended
				_ = p.Wait()
			}
		}
		if err != nil {
			failures++
			g.logf("%s cannot start: %v\n", u.name, err)
			if failures >= maxStartFailures {
				fatal <- unavailablef(err, "%s cannot be started; check that it was built and that its files are readable", u.name)
				return
			}
		} else {
			failures = 0
			startedAt := g.clock.Now()
			exited := make(chan error, 1)
			go func() { exited <- p.Wait() }()
			g.logf("started %s\n", u.name)
			select {
			case werr := <-exited:
				forget()
				g.logf("%s exited (%v); starting it again\n", u.name, werr)
				if g.clock.Now().Sub(startedAt) >= stableAfter {
					delay = restartBase
				}
			case <-ctx.Done():
				g.stop(u, p, exited)
				forget()
				return
			}
		}
		select {
		case <-g.clock.After(delay):
		case <-ctx.Done():
			return
		}
		delay = min(delay*2, restartMax)
	}
}

// stop asks a program to end and kills it when it does not within the grace
// period. exited delivers the result of Wait once.
func (g *group) stop(u unit, p Process, exited <-chan error) {
	if err := p.Terminate(); err != nil {
		g.logf("%s: terminate: %v\n", u.name, err)
	}
	select {
	case <-exited:
		g.logf("stopped %s\n", u.name)
	case <-g.clock.After(g.grace):
		g.logf("%s ignored the stop request; killing it\n", u.name)
		_ = p.Kill() // the group is already gone if this fails
		<-exited
	}
}

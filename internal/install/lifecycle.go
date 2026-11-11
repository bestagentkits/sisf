package install

import (
	"context"
	"syscall"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// UpOptions is what `sisf up` is told.
type UpOptions struct {
	// Init is used when the directory holds no install yet, so one command
	// takes a fresh clone to a running factory. When an install exists, its
	// Mode and Sim must agree with the install: they are fixed at init.
	Init Options
	// Budget confirms the budget of an install that is not confirmed yet.
	Budget *BudgetChoice
	// Reload delivers a request to restart every unit (Standalone mode).
	Reload <-chan struct{}
}

// Up brings the whole factory up: all three zones, from one manifest.
func (in *Installer) Up(ctx context.Context, o UpOptions) error {
	m, err := in.ensure(ctx, o)
	if err != nil {
		return err
	}
	if m.Mode == manifest.ModeDocker {
		return in.upDocker(ctx, m)
	}
	return in.upStandalone(ctx, m, o.Reload)
}

// ensure initializes the directory if it holds no install, applies a budget
// confirmation, and returns the manifest of the install.
func (in *Installer) ensure(ctx context.Context, o UpOptions) (*manifest.Manifest, error) {
	done, err := in.Layout.installed()
	if err != nil {
		return nil, unavailablef(err, "cannot inspect %s", in.Layout.Dir)
	}
	if !done {
		o.Init.Dir = in.Layout.Dir
		res, err := in.Init(ctx, o.Init)
		if err != nil {
			return nil, err
		}
		for _, w := range res.Warnings {
			in.say("warning: %s\n", w)
		}
	}
	m, err := in.Layout.LoadManifest()
	if err != nil {
		return nil, err
	}
	if done {
		if o.Init.Mode != "" && o.Init.Mode != m.Mode {
			return nil, invalidf("this install runs in %s mode; the mode is fixed when it is created", m.Mode)
		}
		if wantSim := o.Init.Sim; wantSim != (m.Extensions["profile"] == profileSim) {
			return nil, invalidf("the simulation profile is fixed when the install is created")
		}
	}
	if o.Budget != nil && o.Budget.Confirmed {
		if err := in.confirmBudget(m, *o.Budget); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// confirmBudget records the Owner's confirmation on an install that has none.
// The confirmation is made once; changing it later is a policy change.
func (in *Installer) confirmBudget(m *manifest.Manifest, c BudgetChoice) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if _, ok, err := readBudgetConfirmation(in.Layout.BudgetConfirmation()); err != nil {
		return err
	} else if ok {
		return core.NewError(core.CodeAlreadyExists, "the budget is already confirmed; it is confirmed once")
	}
	rec, _ := c.confirmation(in.Clock.Now())
	if err := writeBudgetConfirmation(in.Layout.BudgetConfirmation(), rec); err != nil {
		return err
	}
	m.Budget = budget(c)
	return writeManifest(in.Layout.Config(), m)
}

// Stop stops the factory and keeps everything it made.
func (in *Installer) Stop(ctx context.Context) error {
	m, err := in.Layout.LoadManifest()
	if err != nil {
		return err
	}
	if m.Mode == manifest.ModeDocker {
		return in.dockerStop(ctx, m, "stop")
	}
	return in.stopStandalone(ctx)
}

// stopStandalone stops the foreground process when one runs. A factory
// that is not running is already stopped: stop and down are idempotent, so
// a cleanup script, or a factory left behind by a crash, needs no special
// case.
func (in *Installer) stopStandalone(ctx context.Context) error {
	running, err := in.Running()
	if err != nil || !running {
		return err
	}
	return in.signalStandalone(ctx, syscall.SIGTERM)
}

// Down stops the factory and removes what a run created. In Docker mode that
// is the containers and networks, and with removeData the volumes too; in
// Standalone mode there is nothing to remove but the data directory, and only
// removeData deletes it. Secrets and the manifest are never removed here,
// nor the control zone's secret store below the data directory.
func (in *Installer) Down(ctx context.Context, removeData bool) error {
	m, err := in.Layout.LoadManifest()
	if err != nil {
		return err
	}
	if m.Mode == manifest.ModeDocker {
		return in.dockerDown(ctx, m, removeData)
	}
	if err := in.stopStandalone(ctx); err != nil {
		return err
	}
	if !removeData {
		return nil
	}
	// The run lock is held while the data goes, so an `up` started meanwhile
	// fails instead of running on a half-removed directory.
	lock, err := in.lockRun()
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }() // drops the lock
	return in.removeData()
}

// Restart restarts every unit of the running factory.
func (in *Installer) Restart(ctx context.Context) error {
	m, err := in.Layout.LoadManifest()
	if err != nil {
		return err
	}
	if m.Mode == manifest.ModeDocker {
		return in.dockerStop(ctx, m, "restart")
	}
	return in.signalStandalone(ctx, syscall.SIGHUP)
}

// Running reports whether the factory of a Standalone install runs: its
// foreground `sisf up` process is alive. Docker mode answers false; Compose
// knows its own containers.
func (in *Installer) Running() (bool, error) {
	pid, err := in.runningPID()
	return pid != 0, err
}

// signal sends sig to a process.
func (in *Installer) signal(pid int, sig syscall.Signal) error {
	if in.Signal != nil {
		return in.Signal(pid, sig)
	}
	return syscall.Kill(pid, sig)
}

// runningPID returns the pid of the foreground `sisf up`, or 0 when none is
// running. The run lock decides (see Layout.RunLock): a pid file whose lock
// no process holds is stale, whatever process now has its pid, and is
// removed rather than trusted.
func (in *Installer) runningPID() (int, error) {
	held, err := in.runLockHeld()
	if err != nil || !held {
		return 0, err
	}
	pid := readPIDFile(in.Layout.PIDFile())
	if pid == 0 {
		// The lock is taken just before the pid is written.
		return 0, core.NewError(core.CodeUnavailable, "a `sisf up` of this install holds its run lock but has not recorded its process yet; try again in a moment")
	}
	return pid, nil
}

// signalStandalone signals the foreground process. For SIGTERM it waits until
// the process has stopped, so a caller that goes on to delete data does not
// race the units still writing it.
func (in *Installer) signalStandalone(ctx context.Context, sig syscall.Signal) error {
	pid, err := in.runningPID()
	if err != nil {
		return err
	}
	if pid == 0 {
		return core.NewError(core.CodeNotFound, "the factory in "+in.Layout.Dir+" is not running",
			core.NextAction{Action: "sisf up --dir " + in.Layout.Dir, Description: "start it"})
	}
	if err := in.signal(pid, sig); err != nil {
		return unavailablef(err, "cannot signal the factory process")
	}
	if sig != syscall.SIGTERM {
		return nil
	}
	for range stopWaitPolls {
		// The lock is dropped by the kernel when the process has ended.
		if held, err := in.runLockHeld(); err != nil || !held {
			return err
		}
		select {
		case <-in.Clock.After(stopPoll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return core.NewError(core.CodeTimeout, "the factory did not stop in time; its units are still running")
}

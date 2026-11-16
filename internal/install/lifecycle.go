package install

import (
	"context"
	"errors"
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
		// An install that could never start is not created by `up`.
		if needsBuildSandbox(o.Init, o.Init.Mode) {
			return nil, refusedf("%s", noBuildSandbox)
		}
		o.Init.Dir = in.Layout.Dir
		res, err := in.Init(ctx, o.Init)
		if err != nil {
			return nil, err
		}
		for _, w := range res.Warnings {
			in.say("warning: %s\n", w)
		}
		if in.SourceDir != "" {
			in.say("source checkout %s: every `sisf up` of this install builds its programs from it\n", in.SourceDir)
		}
	}
	m, err := in.Layout.LoadManifest()
	if err != nil {
		return nil, err
	}
	if done {
		if o.Init.Mode != "" && o.Init.Mode != m.Mode {
			return nil, refusedf("this install runs in %s mode; the mode is fixed when it is created", m.Mode)
		}
		if wantSim := o.Init.Sim; wantSim != (m.Extensions["profile"] == profileSim) {
			return nil, refusedf("the simulation profile is fixed when the install is created")
		}
		if err := o.Init.refuseDockerBuildFlags(m.Mode); err != nil {
			return nil, err
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

// stopStandalone stops the foreground process when one runs, or the units
// a `sisf up` that has ended left running. A factory that is not running is
// already stopped: stop and down are idempotent, so a cleanup script, or a
// factory left behind by a crash, needs no special case.
func (in *Installer) stopStandalone(ctx context.Context) error {
	st, err := in.inspectRun()
	asked := map[int]bool{}
	switch {
	case err != nil || !st.running():
		return err
	case st.owner.Role == roleUp:
		if err := in.signal(st.owner.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return unavailablef(err, "cannot signal the factory process")
		}
	case st.owner.valid():
		return in.runningRefusal(st)
	case len(st.units) > 0:
		in.say("the `sisf up` of this install has ended, but its units still run; stopping %s\n", describeUnits(st.units))
		if err := in.stopOrphans(st.units, asked, false); err != nil {
			return err
		}
	default:
		return in.unknownHolderError()
	}
	return in.waitStopped(ctx, asked)
}

// waitStopped waits until nothing of the install runs, so a caller that
// goes on to delete data does not race the units still writing it. Units
// left behind by a `sisf up` that has ended are asked to stop and killed
// when they outlast the grace period; each is shown to be the recorded
// process again, by its start time, just before it is signalled.
// asked holds the pids already asked to stop. A lock that outlives every
// recorded process is waited for too, since a program a unit started may
// still be leaving; if it does not leave, the data may still be written,
// and the answer says what holds the lock instead of that it timed out.
func (in *Installer) waitStopped(ctx context.Context, asked map[int]bool) error {
	killAfter := int(gracePeriod / stopPoll)
	unknown := false
	for poll := range stopWaitPolls {
		st, err := in.inspectRun()
		unknown = st.unknownHolder()
		switch {
		case err != nil || !st.running():
			return err
		case !st.owner.valid() && len(st.units) > 0:
			if err := in.stopOrphans(st.units, asked, poll >= killAfter); err != nil {
				return err
			}
		}
		select {
		case <-in.Clock.After(stopPoll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if unknown {
		return in.unknownHolderError()
	}
	return core.NewError(core.CodeTimeout, "the factory did not stop in time; its units are still running")
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
	lock, err := in.takeRunLock(roleDown)
	if err != nil {
		return err
	}
	defer dropRunLock(lock)
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
	return in.signalStandalone(syscall.SIGHUP)
}

// Running reports whether the factory of a Standalone install runs: its
// foreground `sisf up` process, a `sisf down` removing its data, or units
// a `sisf up` that has ended left running. A run lock held by a process the
// install cannot name is an error, never a stopped factory. Docker mode
// answers false; Compose knows its own containers.
func (in *Installer) Running() (bool, error) {
	st, err := in.inspectRun()
	if err != nil {
		return false, err
	}
	if st.unknownHolder() {
		return false, in.unknownHolderError()
	}
	return st.running(), nil
}

// stopOrphans asks each unit that is not yet asked to stop, and kills
// every one when kill is set.
func (in *Installer) stopOrphans(units []processRecord, asked map[int]bool, kill bool) error {
	if kill {
		return in.signalUnits(units, syscall.SIGKILL)
	}
	var fresh []processRecord
	for _, u := range units {
		if !asked[u.PID] {
			asked[u.PID] = true
			fresh = append(fresh, u)
		}
	}
	return in.signalUnits(fresh, syscall.SIGTERM)
}

// signal sends sig to a process, or to a process group for a negative pid.
func (in *Installer) signal(pid int, sig syscall.Signal) error {
	if in.Signal != nil {
		return in.Signal(pid, sig)
	}
	return syscall.Kill(pid, sig)
}

// signalStandalone signals the foreground `sisf up`, which the run lock
// names (see Layout.RunLock) and which is shown to be the process recorded
// there, by its start time, before it is signalled.
func (in *Installer) signalStandalone(sig syscall.Signal) error {
	st, err := in.inspectRun()
	switch {
	case err != nil:
		return err
	case st.owner.Role == roleUp:
		if err := in.signal(st.owner.PID, sig); err != nil {
			return unavailablef(err, "cannot signal the factory process")
		}
		return nil
	case len(st.units) > 0:
		return core.NewError(core.CodeConflict, "the `sisf up` of this install has ended, but its units still run ("+describeUnits(st.units)+"); they cannot be restarted without it",
			core.NextAction{Action: "sisf stop --dir " + in.Layout.Dir, Description: "stop them"},
			core.NextAction{Action: "sisf up --dir " + in.Layout.Dir, Description: "then start the factory again"})
	case st.running():
		return in.runningRefusal(st)
	}
	return core.NewError(core.CodeNotFound, "the factory in "+in.Layout.Dir+" is not running",
		core.NextAction{Action: "sisf up --dir " + in.Layout.Dir, Description: "start it"})
}

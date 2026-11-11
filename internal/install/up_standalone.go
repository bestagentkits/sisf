package install

import (
	"context"
	"os"

	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// upStandalone builds and pins the programs, prepares the state file, and
// then stays in the foreground supervising every unit until ctx ends. One
// command, one process for a service manager to watch, and one lifecycle for
// the three zones.
func (in *Installer) upStandalone(ctx context.Context, m *manifest.Manifest, reload <-chan struct{}) error {
	// The run lock is held from before the build until the units stopped;
	// a second `up` of this install fails here at once.
	lock, err := in.acquireRunLock()
	if err != nil {
		return err
	}
	defer lock.release()
	b, err := in.release(ctx, m)
	if err != nil {
		return err
	}
	if err := in.refreshGHToken(ctx, m); err != nil {
		return err
	}
	for _, dir := range []string{"state", "app", "control", "trust", "workspaces", "qa-workspaces", "artifacts"} {
		if err := os.MkdirAll(in.Layout.Data(dir), 0o700); err != nil {
			return unavailablef(err, "cannot create the data directory %s", dir)
		}
	}
	if err := in.bootstrapState(ctx, m); err != nil {
		return err
	}
	if err := in.installTrust(ctx, m, b); err != nil {
		return err
	}
	if err := in.writeAppLauncher(); err != nil {
		return err
	}
	if m.Extensions["profile"] == profileSim || !agentsOf(m).real() {
		if err := in.ensureSimPatch(); err != nil {
			return err
		}
	}
	units, closers, err := in.standaloneUnits(m)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close() // logs only; the units have stopped by now
		}
	}()
	in.say("factory %s is up: control http://%s, app via edge http://%s\n", m.FactoryID, addr(PortControl), addr(PortEdge))
	return in.superviseUnits(ctx, lock, units, reload)
}

// superviseUnits runs the units until ctx ends. Every unit inherits the
// open file that holds the run lock and is recorded with its start time, so
// the install still counts as running, and its units can be found and
// stopped, if this process ends without stopping them.
func (in *Installer) superviseUnits(ctx context.Context, lock *runLock, units []unit, reload <-chan struct{}) error {
	for i := range units {
		units[i].cmd.ExtraFiles = []*os.File{lock.file} // descriptor RunLockFD
	}
	g := newGroup(in.Exec, in.Clock, units, in.say)
	g.record = in.recordUnit
	if reload != nil {
		go func() {
			for {
				select {
				case <-reload:
					g.Restart()
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	return g.Run(ctx)
}

// bootstrapState migrates the SQLite store and records the confirmation.
func (in *Installer) bootstrapState(ctx context.Context, m *manifest.Manifest) error {
	cfg := BootstrapConfig{
		FactoryID: m.FactoryID, Dialect: sqlstore.DialectSQLite, DSN: in.Layout.StateDB(), Clock: in.Clock,
	}
	rec, ok, err := readBudgetConfirmation(in.Layout.BudgetConfirmation())
	if err != nil {
		return err
	}
	if ok {
		cfg.Budget = &rec
	}
	return Bootstrap(ctx, cfg)
}

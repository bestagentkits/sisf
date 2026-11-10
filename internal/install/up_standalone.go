package install

import (
	"context"
	"os"
	"path/filepath"
	"strconv"

	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// upStandalone builds and pins the programs, prepares the state file, and
// then stays in the foreground supervising every unit until ctx ends. One
// command, one process for a service manager to watch, and one lifecycle for
// the three zones.
func (in *Installer) upStandalone(ctx context.Context, m *manifest.Manifest, reload <-chan struct{}) error {
	if pid, err := in.runningPID(); err != nil {
		return err
	} else if pid != 0 {
		return invalidf("this install is already running (process %d)", pid)
	}
	b, err := in.release(ctx, m)
	if err != nil {
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
	if m.Extensions["profile"] == profileSim {
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
	if err := in.writePID(); err != nil {
		return err
	}
	defer func() { _ = os.Remove(in.Layout.PIDFile()) }()

	g := newGroup(in.Exec, in.Clock, units, in.say)
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
	in.say("factory %s is up: control http://%s, app via edge http://%s\n", m.FactoryID, addr(PortControl), addr(PortEdge))
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

func (in *Installer) writePID() error {
	path := in.Layout.PIDFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return unavailablef(err, "cannot create the run directory")
	}
	return writeSecret(path, []byte(strconv.Itoa(os.Getpid())+"\n"))
}

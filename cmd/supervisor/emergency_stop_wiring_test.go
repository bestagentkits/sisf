package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor"
	"github.com/bestagentkits/sisf/internal/supervisor/killswitch"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore"
)

// preparedStore creates a migrated SQLite store for the test factory and
// returns its path.
func preparedStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := sqlstore.Open(context.Background(), sqlstore.Config{
		Dialect: sqlstore.DialectSQLite, DSN: path, FactoryID: testFactory, Zone: core.ZoneControl,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Migrate(context.Background(), state.MigrateOptions{}); err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	return path
}

func TestASupervisorGivenTheStoreServesTheEmergencyStop(t *testing.T) {
	t.Parallel()
	inst := newInstall(t, mustOwnDigest(t))
	inst.env[killswitch.EnvStateDSN] = preparedStore(t)
	ctx, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	stderr := newOutput("serving status")
	exited := make(chan int, 1)
	go func() {
		exited <- run(ctx, inst.runArgs(), env{stdout: newOutput(""), stderr: stderr, lookupEnv: inst.lookupEnv})
	}()
	select {
	case <-stderr.seen:
	case status := <-exited:
		t.Fatalf("the supervisor exited with status %d instead of serving:\n%s", status, stderr)
	}

	client, err := supervisor.NewClient("unix:" + inst.socket)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	report, err := client.EmergencyStop(ctx, supervisor.EmergencyStopRequest{Actor: "user:owner", Reason: "runaway spend"})
	if err != nil || report.StopID == "" || !report.PublishingFrozen {
		t.Fatalf("EmergencyStop = %+v, %v", report, err)
	}
	status, err := client.FreezeStatus(ctx)
	if err != nil || !status.Frozen || status.StopID != report.StopID {
		t.Fatalf("FreezeStatus = %+v, %v", status, err)
	}
	interrupt()
	if status := <-exited; status != exitOK {
		t.Errorf("the supervisor exited %d after an interrupt:\n%s", status, stderr)
	}
}

func TestASupervisorGivenAStoreItCannotOpenRefusesToStart(t *testing.T) {
	t.Parallel()
	inst := newInstall(t, mustOwnDigest(t))
	inst.env[killswitch.EnvStateDSN] = filepath.Join(t.TempDir(), "unprepared.db")
	status, _, stderr := invoke(inst.lookupEnv, inst.runArgs()...)
	if status != exitFailure || !strings.Contains(stderr, "emergency stop") {
		t.Fatalf("status %d, stderr %q, want a refusal naming the emergency stop", status, stderr)
	}
	if inst.socketExists() {
		t.Error("a refused supervisor opened its admin socket")
	}
}

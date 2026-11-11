package local_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/local"
)

// sleeper is a real operating-system process the test owns: it stands for
// an application process a supervisor started and lost track of.
type sleeper struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

func startSleeper(t *testing.T) *sleeper {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &sleeper{cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait() // reap it, so a stopped sleeper is gone rather than a zombie
		close(s.exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-s.exited
	})
	return s
}

func (s *sleeper) gone(within time.Duration) bool {
	select {
	case <-s.exited:
		return true
	case <-time.After(within):
		return false
	}
}

// pidStarter starts the fake world's processes, but each says it is the
// operating-system process of a sleeper, so the backend records it.
type pidStarter struct {
	local.Starter
	pid int
}

type pidProcess struct {
	local.Process
	pid int
}

func (p pidProcess) PID() int { return p.pid }

func (s pidStarter) Start(ctx context.Context, spec local.StartSpec) (local.Process, error) {
	p, err := s.Starter.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return pidProcess{Process: p, pid: s.pid}, nil
}

// slotRecord is the backend's record of a slot, as far as the tests read it.
type slotRecord struct {
	Slot  string `json:"slot"`
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

func readSlotRecord(t *testing.T, path string) (slotRecord, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return slotRecord{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var rec slotRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	return rec, true
}

// stagedWithRecord stages digestA on the blue slot of a backend whose
// process is the sleeper, and returns the state directory it recorded the
// slot in.
func stagedWithRecord(t *testing.T, app *sleeper) (*world, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "slots")
	w := newWorld(t, worldOptions{cfg: func(c *local.Config) {
		c.StateDir = dir
		c.Starter = pidStarter{Starter: c.Starter, pid: app.cmd.Process.Pid}
	}})
	w.stage(digestA)
	rec, ok := readSlotRecord(t, filepath.Join(dir, "blue.json"))
	if !ok || rec.PID != app.cmd.Process.Pid || rec.Slot != "blue" || rec.Start == "" {
		t.Fatalf("the staged slot was not recorded: %+v", rec)
	}
	return w, dir
}

// restarted is a new backend over the same state directory, as after a
// restart of the supervisor: it runs nothing of its own.
func restarted(t *testing.T, dir string) *local.Backend {
	t.Helper()
	w := newWorld(t, worldOptions{cfg: func(c *local.Config) { c.StateDir = dir }})
	return w.backend
}

// A backend with a state directory records the process of each slot it
// starts, forgets it once the process is stopped, and never takes its own
// running processes for stale ones.
func TestTheProcessOfASlotIsRecordedWhileItRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	app := startSleeper(t)
	dir := filepath.Join(t.TempDir(), "slots")
	w := newWorld(t, worldOptions{cfg: func(c *local.Config) {
		c.StateDir = dir
		c.Starter = pidStarter{Starter: c.Starter, pid: app.cmd.Process.Pid}
	}})
	stable := w.release(digestA)
	w.stage(digestB)
	for _, slot := range []string{"blue", "green"} {
		if rec, ok := readSlotRecord(t, filepath.Join(dir, slot+".json")); !ok || rec.PID != app.cmd.Process.Pid {
			t.Fatalf("slot %s is not recorded: %+v", slot, rec)
		}
	}
	if err := w.backend.StopStale(ctx); err != nil {
		t.Fatalf("a backend stopped its own processes as stale: %v", err)
	}
	if app.gone(100 * time.Millisecond) {
		t.Fatal("a backend signalled a process it runs itself")
	}
	// The rollback stops the staged revision on green.
	if err := w.backend.Rollback(ctx, stable); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSlotRecord(t, filepath.Join(dir, "green.json")); ok {
		t.Fatal("the record of a stopped slot is still there")
	}
	if _, ok := readSlotRecord(t, filepath.Join(dir, "blue.json")); !ok {
		t.Fatal("the running slot's record is gone")
	}
}

// After a restart, the process an earlier backend started and recorded is
// stopped before anything is staged, and its record is dropped.
func TestARestartedBackendStopsTheProcessAnEarlierOneLeftRunning(t *testing.T) {
	t.Parallel()
	app := startSleeper(t)
	_, dir := stagedWithRecord(t, app)
	if err := restarted(t, dir).StopStale(context.Background()); err != nil {
		t.Fatalf("StopStale: %v", err)
	}
	if !app.gone(10 * time.Second) {
		t.Fatal("the stale process still runs")
	}
	if _, ok := readSlotRecord(t, filepath.Join(dir, "blue.json")); ok {
		t.Fatal("the record of the stopped process is still there")
	}
}

// A recorded pid that no longer names the recorded process, here because
// the process that has it started at another time, is never signalled:
// the record is dropped and the process runs on.
func TestARecordWhosePidNamesAnotherProcessIsDroppedWithoutASignal(t *testing.T) {
	t.Parallel()
	app := startSleeper(t)
	_, dir := stagedWithRecord(t, app)
	path := filepath.Join(dir, "blue.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	rec["start"] = "Thu Jan  1 00:00:00 1970"
	if raw, err = json.Marshal(rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restarted(t, dir).StopStale(context.Background()); err != nil {
		t.Fatalf("StopStale: %v", err)
	}
	if app.gone(200 * time.Millisecond) {
		t.Fatal("a process the record does not name was signalled")
	}
	if _, ok := readSlotRecord(t, path); ok {
		t.Fatal("the record of a process that is gone is still there")
	}
}

// A state directory must be an absolute path.
func TestTheStateDirectoryMustBeAbsolute(t *testing.T) {
	t.Parallel()
	starter, ports := newFakeStarter(t)
	if _, err := local.New(local.Config{Factory: factory, Ports: ports, Starter: starter, Edge: brokenEdge{}, StateDir: "slots"}); err == nil {
		t.Fatal("a relative state directory was accepted")
	}
}

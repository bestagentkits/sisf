package local_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/deploy/local"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/deploy"
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
// restart of the supervisor: it has started nothing itself.
func restarted(t *testing.T, dir string) *world {
	t.Helper()
	return newWorld(t, worldOptions{cfg: func(c *local.Config) { c.StateDir = dir }})
}

// A backend with a state directory records the process of each slot it
// starts and forgets it once the process is stopped; a backend built over
// the records finds the running processes again without signalling them.
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
	restarted(t, dir)
	if app.gone(100 * time.Millisecond) {
		t.Fatal("a backend built over the records signalled a process it found again")
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
// the process of its slot again: it is not signalled when the backend is
// built, and it is stopped, its record replaced, when the slot is staged
// over, so it never holds the port of what is staged.
func TestARestartedBackendStopsTheProcessAnEarlierOneLeftWhenItStagesOverIt(t *testing.T) {
	t.Parallel()
	app := startSleeper(t)
	_, dir := stagedWithRecord(t, app)
	w := restarted(t, dir)
	if app.gone(100 * time.Millisecond) {
		t.Fatal("the process found again was signalled before its slot was used")
	}
	w.stage(digestB)
	if !app.gone(10 * time.Second) {
		t.Fatal("the process an earlier backend left still runs on the slot staged over")
	}
	if rec, ok := readSlotRecord(t, filepath.Join(dir, "blue.json")); ok && rec.PID == app.cmd.Process.Pid {
		t.Fatal("the record of the stopped process is still there")
	}
}

// A recorded pid that no longer names the recorded process, here because
// the process that has it started at another time, is never signalled:
// the record is dropped when the backend is built, the process runs on
// while the slot is staged over, and the slot is not taken as running.
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
	w := restarted(t, dir)
	if _, ok := readSlotRecord(t, path); ok {
		t.Fatal("the record of a process that is gone is still there")
	}
	if obs, err := w.backend.Measure(context.Background(), deploy.Revision{Unit: "app", Digest: digestA}, time.Minute); err == nil && obs.Healthy {
		t.Fatal("a slot whose record names another process is taken as running")
	}
	w.stage(digestB)
	if app.gone(200 * time.Millisecond) {
		t.Fatal("a process the record does not name was signalled")
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

// execBackedStarter starts the fake world's application for the readiness
// probes and, beside it, a real process through the ExecStarter: a
// launcher that execs the program, as the installed launcher does. The
// backend records the real process.
type execBackedStarter struct {
	local.Starter
	real local.ExecStarter
}

type execBackedProcess struct {
	local.Process
	real local.Process
}

func (p execBackedProcess) PID() int {
	return p.real.(interface{ PID() int }).PID()
}

func (s execBackedStarter) Start(ctx context.Context, spec local.StartSpec) (local.Process, error) {
	fake, err := s.Starter.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	real, err := s.real.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	return execBackedProcess{Process: fake, real: real}, nil
}

// alive reports whether pid names a process.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

// A process the ExecStarter started through a launcher that execs the
// release's program is still found by a restarted backend, and stopped
// when its slot is staged over: the exec changes the command line, not the
// identity recorded.
func TestARestartedBackendStopsAProcessItsLauncherExecedInto(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "slots")
	launcher := filepath.Join(t.TempDir(), "launcher")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec /bin/sleep 300\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	real := local.ExecStarter{AddrEnv: "LISTEN", Env: []string{"PATH=/usr/bin:/bin"},
		Command: func(string) (string, []string, error) { return launcher, nil, nil }}
	w := newWorld(t, worldOptions{cfg: func(c *local.Config) {
		c.StateDir = dir
		c.Starter = execBackedStarter{Starter: c.Starter, real: real}
	}})
	w.stage(digestA)
	rec, ok := readSlotRecord(t, filepath.Join(dir, "blue.json"))
	if !ok || rec.PID <= 1 {
		t.Fatalf("the started process was not recorded: %+v", rec)
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(rec.PID); err == nil {
			_ = p.Kill()
		}
	})
	// Let the launcher become the program before the restart.
	time.Sleep(200 * time.Millisecond)
	restarted(t, dir).stage(digestB)
	deadline := time.Now().Add(10 * time.Second)
	for alive(rec.PID) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still runs after the restarted backend stopped it", rec.PID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A process found again after a restart is outside the process group of
// the supervisor that found it, so it is stopped when that supervisor
// closes the backend; a process the backend started itself is left to end
// with its owner.
func TestClosingABackendStopsOnlyTheProcessesItFoundAgain(t *testing.T) {
	t.Parallel()
	app := startSleeper(t)
	_, dir := stagedWithRecord(t, app)
	w := restarted(t, dir)
	// The edge serves the release found again on blue, so green is staged.
	if err := w.splitter.SetSplit(context.Background(), edge.Split{Stable: edge.Target{Revision: string(digestA), URL: "http://" + w.addr(0)}}); err != nil {
		t.Fatal(err)
	}
	w.stage(digestB)
	if app.gone(100 * time.Millisecond) {
		t.Fatal("the process found again was stopped before the backend closed")
	}
	if err := w.backend.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !app.gone(10 * time.Second) {
		t.Fatal("the process found again still runs after the backend closed")
	}
	if _, ok := readSlotRecord(t, filepath.Join(dir, "blue.json")); ok {
		t.Fatal("the record of the stopped process is still there")
	}
	if !w.alive(1) {
		t.Fatal("closing the backend stopped a process it started itself")
	}
}

// A record an earlier build wrote with ps's start, before Linux read the
// start from /proc, still names its process: it is checked the way it was
// written, so the upgrade does not lose the process that serves.
func TestARecordOfAnEarlierBuildIsCheckedTheWayItWasWritten(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps is not installed")
	}
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
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(app.cmd.Process.Pid))
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	rec["start"] = strings.Join(strings.Fields(string(out)), " ")
	if raw, err = json.Marshal(rec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted(t, dir)
	if again, ok := readSlotRecord(t, path); !ok || again.Start != rec["start"] {
		t.Fatalf("a restarted backend dropped the record of an earlier build: %+v", again)
	}
}

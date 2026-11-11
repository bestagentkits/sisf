package install

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// orphanUnit stands in for a `sisf up` (pid 4242) that was killed while its
// control unit (pid 5000) ran on: the unit holds the run lock it inherited
// and is recorded with its start time. It returns the data directory.
func orphanUnit(t *testing.T, signals *fakeSignals) (*Installer, string) {
	t.Helper()
	in := withPIDFile(t, 4242, signals)
	signals.mu.Lock()
	signals.alive[4242], signals.alive[5000] = false, true
	signals.locks[5000], signals.locks[4242] = signals.locks[4242], nil
	signals.mu.Unlock()
	writeUnitRecord(t, in, processRecord{Role: "control", PID: 5000, Start: fakeStart(5000)})
	data := in.Layout.Data("state")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	return in, data
}

// namesTheLockHolder reports whether err is the refusal of a run lock held
// by no recorded process, with the command that finds the holder.
func namesTheLockHolder(err error) bool {
	var typed *core.Error
	if !asCoreError(err, &typed) || typed.Code != core.CodeConflict {
		return false
	}
	for _, a := range typed.NextActions {
		if strings.HasPrefix(a.Action, "lsof ") {
			return true
		}
	}
	return false
}

func writeUnitRecord(t *testing.T, in *Installer, rec processRecord) {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(in.Layout.UnitRecords(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.unitRecordFile(rec.Role), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Units of a `sisf up` that was killed keep the install running: a look
// says so, a new `up` refuses and says to stop them first, and restart
// cannot restart them.
func TestUnitsOfAKilledUpKeepTheInstallRunningAndANewUpRefuses(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in, _ := orphanUnit(t, signals)
	if running, err := in.Running(); err != nil || !running {
		t.Fatalf("Running = %v %v while a unit of a killed up runs", running, err)
	}
	err := in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeStandalone)})
	if err == nil || !strings.Contains(err.Error(), "sisf stop") || !strings.Contains(err.Error(), "control: process 5000") {
		t.Errorf("up = %v, want a refusal that names the unit and says to run sisf stop first", err)
	}
	if err := in.Restart(t.Context()); !core.HasCode(err, core.CodeConflict) {
		t.Errorf("restart = %v, want CONFLICT", err)
	}
	if len(signals.sent) != 0 {
		t.Fatalf("signals %v were sent", signals.sent)
	}
}

// Stop finds the recorded units of a `sisf up` that has ended and stops
// their process groups; their records go once the lock is free.
func TestStopStopsTheRecordedUnitsOfAnUpThatHasEnded(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in, _ := orphanUnit(t, signals)
	if err := in.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(signals.sentTo, []int{5000}) || signals.sent[0] != syscall.SIGTERM {
		t.Fatalf("signals %v to %v, want one SIGTERM to the unit", signals.sent, signals.sentTo)
	}
	if running, err := in.Running(); err != nil || running {
		t.Fatalf("Running = %v %v after stop", running, err)
	}
	if _, err := os.Stat(in.unitRecordFile("control")); !os.IsNotExist(err) {
		t.Error("the record of a stopped unit was kept")
	}
}

// down -v stops those units first and removes the data only once nothing
// holds the run lock.
func TestDownWithDataStopsTheUnitsOfAnUpThatHasEndedBeforeRemovingAnything(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in, data := orphanUnit(t, signals)
	in.Signal = func(pid int, sig syscall.Signal) error {
		if _, err := os.Stat(data); err != nil {
			t.Errorf("the data was removed before process %d was sent %v", pid, sig)
		}
		return signals.send(pid, sig)
	}
	if err := in.Down(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(signals.sentTo, 5000) {
		t.Fatal("the unit was not stopped")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("down -v kept the data")
	}
}

// A unit record whose pid now names a process that started at another
// time is not the unit: it is never signalled. With the lock still held by
// something unnamed, nothing is called stopped and no data goes; once the
// lock is free the record is stale and removed.
func TestStopNeverSignalsAPIDWhoseStartTimeDiffersFromTheRecord(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in, data := orphanUnit(t, signals)
	signals.starts = map[int]string{5000: "a later start"}
	for name, run := range map[string]func() error{
		"stop":    func() error { return in.Stop(t.Context()) },
		"down -v": func() error { return in.Down(t.Context(), true) },
		"up":      func() error { return in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeStandalone)}) },
	} {
		if err := run(); !namesTheLockHolder(err) {
			t.Errorf("%s = %v, want CONFLICT that says how to find the lock's holder", name, err)
		}
	}
	if len(signals.sent) != 0 {
		t.Fatalf("signals %v were sent to %v", signals.sent, signals.sentTo)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatal("the data was removed under a held run lock")
	}
	// The holder leaves: the record names no unit and is removed.
	signals.mu.Lock()
	_ = signals.locks[5000].Close()
	signals.mu.Unlock()
	// A process another test forks holds the lock for a moment too.
	running, err := in.Running()
	for try := 0; (err != nil || running) && try < 100; try++ {
		time.Sleep(10 * time.Millisecond)
		running, err = in.Running()
	}
	if err != nil || running {
		t.Fatalf("Running = %v %v once the lock is free", running, err)
	}
	if _, err := os.Stat(in.unitRecordFile("control")); !os.IsNotExist(err) {
		t.Error("a stale unit record was kept")
	}
	if len(signals.sent) != 0 {
		t.Fatalf("signals %v were sent to %v", signals.sent, signals.sentTo)
	}
}

// fakeUpEnv makes the test binary act as a `sisf up` of the install in the
// directory it names, with one unit that sleeps.
const fakeUpEnv = "SISF_TEST_FAKE_UP_DIR"

// TestFakeUpProcess is not a test: run by another test with fakeUpEnv set,
// it takes the run lock and runs one real unit until it is killed.
func TestFakeUpProcess(t *testing.T) {
	dir := os.Getenv(fakeUpEnv)
	if dir == "" {
		return
	}
	in := &Installer{Layout: Layout{Dir: dir}, Exec: OSExec{}, Clock: core.SystemClock{}, Out: os.Stderr}
	lock, err := in.acquireRunLock()
	if err != nil {
		t.Fatal(err)
	}
	units := []unit{{"control", Cmd{Name: "/bin/sleep", Args: []string{"300"}}}}
	_ = in.superviseUnits(context.Background(), lock, units, nil)
}

// A real `sisf up` is killed with SIGKILL while its unit runs, in a process
// group of its own as every unit is. The unit keeps the run lock it
// inherited, so the install still runs; a new `up` refuses; `down -v`
// stops the unit, which was recorded with its start time, before it
// removes the data.
func TestAUnitThatOutlivesAKilledUpKeepsTheInstallRunningUntilStopped(t *testing.T) {
	// Not parallel: a process forked meanwhile would hold the run lock
	// of a parallel test for a moment.
	if testing.Short() {
		t.Skip("starts real processes")
	}
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	in.Exec, in.Clock, in.ProcessStart = OSExec{}, core.SystemClock{}, nil
	data := in.Layout.Data("state")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	up := exec.Command(os.Args[0], "-test.run=^TestFakeUpProcess$")
	up.Env = append(os.Environ(), fakeUpEnv+"="+in.Layout.Dir)
	var upOut bytes.Buffer
	up.Stdout, up.Stderr = &upOut, &upOut
	if err := up.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = up.Process.Kill(); _ = up.Wait() })
	var rec processRecord
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var ok bool
		if rec, ok = readUnitRecord(in.unitRecordFile("control")); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fake up recorded no unit: %s", upOut.String())
		}
	}
	t.Cleanup(func() {
		// Only the recorded unit, and only while it is still that process.
		if ok, _ := in.alive(rec); ok {
			_ = syscall.Kill(rec.PID, syscall.SIGKILL)
		}
	})
	if err := up.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = up.Wait()

	if running, err := in.Running(); err != nil || !running {
		t.Fatalf("Running = %v %v while the unit of a killed up runs", running, err)
	}
	if _, err := in.acquireRunLock(); err == nil || !strings.Contains(err.Error(), "sisf stop") {
		t.Fatalf("up = %v, want a refusal that says to run sisf stop first", err)
	}
	if err := in.Down(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if ok, err := in.alive(rec); err != nil || ok {
		t.Fatalf("the unit (process %d) still runs after down -v (%v)", rec.PID, err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatal("down -v kept the data")
	}
}

// fakeUnitEnv makes the test binary act as a unit that received the run
// lock and starts a program of its own.
const fakeUnitEnv = "SISF_TEST_FAKE_UNIT"

// TestFakeUnitProcess is not a test: run by another test with fakeUnitEnv
// set, it does what a unit does first, then reports whether a program it
// starts inherits the run lock's descriptor.
func TestFakeUnitProcess(t *testing.T) {
	if os.Getenv(fakeUnitEnv) == "" {
		return
	}
	KeepInheritedRunLockFromChildren()
	out, err := exec.Command("/bin/sh", "-c", "if (: <&3) 2>/dev/null; then echo inherited; else echo closed; fi").Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		t.Fatal(err)
	}
	// The unit itself still holds the lock.
	if err := syscall.Flock(RunLockFD, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the unit lost the run lock: %v", err)
	}
}

// A unit keeps the run lock, but a program it starts, such as an agent or
// the application, does not inherit it.
func TestAProgramAUnitStartsDoesNotInheritTheRunLock(t *testing.T) {
	// Not parallel: it forks, see above.
	lock, err := os.OpenFile(filepath.Join(t.TempDir(), "sisf.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	unit := exec.Command(os.Args[0], "-test.run=^TestFakeUnitProcess$")
	unit.Env = append(os.Environ(), fakeUnitEnv+"=1")
	unit.ExtraFiles = []*os.File{lock}
	out, err := unit.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.Contains(string(out), "closed") || strings.Contains(string(out), "inherited") {
		t.Fatalf("a program the unit started inherited the run lock: %s", out)
	}
}

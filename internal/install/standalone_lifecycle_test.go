package install

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// fakeSignals stands in for the operating system's process table: a process
// is alive until it is sent SIGTERM, and every signal is recorded. A process
// that ends drops the run lock it holds, as the kernel does.
type fakeSignals struct {
	mu         sync.Mutex
	alive      map[int]bool
	sent       []syscall.Signal
	ignoreTerm bool
	locks      map[int]*os.File
}

func (f *fakeSignals) send(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.alive[pid] {
		return syscall.ESRCH
	}
	if sig == 0 {
		return nil
	}
	f.sent = append(f.sent, sig)
	if sig == syscall.SIGTERM && !f.ignoreTerm {
		f.alive[pid] = false
		if lock := f.locks[pid]; lock != nil {
			_ = lock.Close()
		}
	}
	return nil
}

// holdRunLock takes the run lock of the install as the `sisf up` process pid
// would, through a file of its own, and records pid in the pid file. The
// lock is dropped when the test ends or the fake process is stopped.
func holdRunLock(t *testing.T, in *Installer, pid int, signals *fakeSignals) *os.File {
	t.Helper()
	if err := os.MkdirAll(in.Layout.Dir+"/run", 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(in.Layout.RunLock(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.Layout.PIDFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if signals != nil {
		signals.mu.Lock()
		if signals.locks == nil {
			signals.locks = map[int]*os.File{}
		}
		signals.locks[pid] = lock
		signals.mu.Unlock()
	}
	return lock
}

// withPIDFile gives the install a running fake `sisf up`: a live fake
// process that holds the run lock and is named in the pid file.
func withPIDFile(t *testing.T, pid int, signals *fakeSignals) *Installer {
	t.Helper()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	signals.alive = map[int]bool{pid: true}
	in.Signal = signals.send
	holdRunLock(t, in, pid, signals)
	return in
}

func TestStopAsksTheRunningFactoryToStopAndWaitsForIt(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in := withPIDFile(t, 4242, signals)
	if err := in.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(signals.sent) != 1 || signals.sent[0] != syscall.SIGTERM {
		t.Fatalf("signals = %v, want one SIGTERM", signals.sent)
	}
}

func TestRestartReloadsWithoutStoppingTheFactory(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in := withPIDFile(t, 4242, signals)
	if err := in.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(signals.sent) != 1 || signals.sent[0] != syscall.SIGHUP || !signals.alive[4242] {
		t.Fatalf("signals = %v alive=%v, want one SIGHUP and a live process", signals.sent, signals.alive)
	}
}

func TestDownKeepsDataUnlessAskedAndNeverTheSecrets(t *testing.T) {
	t.Parallel()
	for _, remove := range []bool{false, true} {
		signals := &fakeSignals{}
		in := withPIDFile(t, 4242, signals)
		data := in.Layout.Data("app")
		if err := os.MkdirAll(data, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := in.Down(t.Context(), remove); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(data)
		if kept := err == nil; kept == remove {
			t.Errorf("down (remove data = %v): data kept = %v", remove, kept)
		}
		for _, keep := range []string{in.Layout.Config(), in.Layout.Secret(areaControl, "root-key.pem")} {
			if _, err := os.Stat(keep); err != nil {
				t.Errorf("down (remove data = %v) removed %s", remove, keep)
			}
		}
	}
}

// Stop and down are idempotent, so cleanup scripts and a factory left
// behind by a crash need no special case; down -v still removes the data.
// Restart has nothing to restart and says how to start.
func TestWithoutARunningFactoryStopAndDownSucceedAndRestartSaysHowToStart(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	in.Signal = (&fakeSignals{alive: map[int]bool{}}).send
	if err := os.MkdirAll(in.Layout.Data("state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := in.Stop(t.Context()); err != nil {
		t.Errorf("stop of a stopped factory = %v", err)
	}
	if err := in.Down(t.Context(), false); err != nil {
		t.Errorf("down of a stopped factory = %v", err)
	}
	if _, err := os.Stat(in.Layout.Data()); err != nil {
		t.Error("down without -v removed the data")
	}
	if err := in.Down(t.Context(), true); err != nil {
		t.Errorf("down -v of a stopped factory = %v", err)
	}
	if _, err := os.Stat(in.Layout.Data()); err == nil {
		t.Error("down -v of a stopped factory kept the data")
	}
	err := in.Restart(t.Context())
	var typed *core.Error
	if !asCoreError(err, &typed) || typed.Code != core.CodeNotFound || len(typed.NextActions) == 0 || !strings.Contains(typed.NextActions[0].Action, "sisf up") {
		t.Errorf("restart = %v, want NOT_FOUND with the start command", err)
	}
	for _, keep := range []string{in.Layout.Config(), in.Layout.Secret(areaControl, "root-key.pem")} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s is gone", keep)
		}
	}
	if running, err := in.Running(); err != nil || running {
		t.Errorf("Running = %v, %v", running, err)
	}
}

func TestAPIDFileOfAProcessThatIsGoneIsNotARunningFactory(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in := withPIDFile(t, 4242, signals)
	// The process ends without removing its pid file: the kernel drops its lock.
	signals.mu.Lock()
	signals.alive[4242] = false
	_ = signals.locks[4242].Close()
	signals.mu.Unlock()
	pid, err := in.runningPID()
	if err != nil || pid != 0 {
		t.Fatalf("runningPID = %d %v, want none", pid, err)
	}
}

// A pid file survives a reboot, and its pid may then belong to an unrelated
// process. Without the run lock it is stale: nothing is signalled, it is
// removed, and stop, down -v and a new up go ahead.
func TestAPIDFileWhoseRunLockNobodyHoldsIsStaleEvenWhenItsPIDIsAlive(t *testing.T) {
	t.Parallel()
	for _, write := range []bool{false, true} {
		signals := &fakeSignals{alive: map[int]bool{4242: true}}
		in, _, out := newTestInstaller(t)
		mustInit(t, in, manifest.ModeStandalone)
		in.Signal = signals.send
		if err := os.MkdirAll(in.Layout.Dir+"/run", 0o700); err != nil {
			t.Fatal(err)
		}
		if write { // a lock file left by an earlier `up`, unlocked
			if err := os.WriteFile(in.Layout.RunLock(), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(in.Layout.PIDFile(), []byte("4242\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if running, err := in.Running(); err != nil || running {
			t.Fatalf("Running = %v %v, want a stale pid file to be no running factory", running, err)
		}
		if _, err := os.Stat(in.Layout.PIDFile()); !os.IsNotExist(err) {
			t.Error("the stale pid file was kept")
		}
		if !strings.Contains(out.String(), "stale pid file") {
			t.Errorf("output %q does not say the pid file was stale", out.String())
		}
		if err := os.WriteFile(in.Layout.PIDFile(), []byte("4242\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := in.Down(t.Context(), true); err != nil {
			t.Fatalf("down -v with a stale pid file = %v", err)
		}
		if len(signals.sent) != 0 {
			t.Fatalf("signals %v were sent to the process a stale pid file names", signals.sent)
		}
	}
}

// The run lock is taken before the build and held for the whole run, so two
// overlapping `up` runs cannot both pass, and the pid file is removed only by
// the run it names.
func TestTheRunLockAdmitsOneUpAndItsPIDFileIsRemovedOnlyByItsOwner(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	first, err := in.acquireRunLock()
	if err != nil {
		t.Fatal(err)
	}
	if readPIDFile(in.Layout.PIDFile()) != os.Getpid() {
		t.Fatal("the pid file does not name the process that holds the lock")
	}
	_, err = in.acquireRunLock()
	var typed *core.Error
	if !asCoreError(err, &typed) || typed.Code != core.CodeInvalidArgument || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second acquire = %v, want a refusal that says the install is already running", err)
	}
	if running, err := in.Running(); err != nil || !running {
		t.Fatalf("Running = %v %v while the lock is held", running, err)
	}
	// A pid file that another process rewrote is not this run's to remove.
	if err := os.WriteFile(in.Layout.PIDFile(), []byte("4243\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first.release()
	if readPIDFile(in.Layout.PIDFile()) != 4243 {
		t.Fatal("release removed a pid file that names another process")
	}
	again, err := in.acquireRunLock()
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again.release()
	if _, err := os.Stat(in.Layout.PIDFile()); !os.IsNotExist(err) {
		t.Fatal("release kept the pid file of its own run")
	}
	if _, err := os.Stat(in.Layout.RunLock()); err != nil {
		t.Fatal("the lock file was removed; a second up could then lock a new one")
	}
}

func TestADownThatTheFactoryDoesNotObeyReportsATimeoutAndKeepsTheData(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{ignoreTerm: true}
	in := withPIDFile(t, 4242, signals)
	data := in.Layout.Data("app")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	// Each wait the installer arms is ended at once by advancing the clock.
	clock := newStepClock()
	in.Clock = clock
	go func() {
		for {
			select {
			case <-clock.armed:
				clock.Advance(stopPoll)
			case <-t.Context().Done():
				return
			}
		}
	}()
	var typed *core.Error
	if err := in.Down(t.Context(), true); !asCoreError(err, &typed) || typed.Code != core.CodeTimeout {
		t.Fatalf("Down = %v, want TIMEOUT", err)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatal("data was deleted while the units were still running")
	}
}

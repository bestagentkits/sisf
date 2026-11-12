package install

import (
	"os"
	"path/filepath"
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
// that ends drops the run lock it holds, as the kernel does. A negative pid
// names the process group the process of that pid leads. A live process
// started at "start-<pid>" unless starts says otherwise.
type fakeSignals struct {
	mu         sync.Mutex
	alive      map[int]bool
	sent       []syscall.Signal
	sentTo     []int
	ignoreTerm bool
	locks      map[int]*os.File
	starts     map[int]string
}

// start answers when a fake process started, as Installer.ProcessStart.
// The test's own process lives too: a `sisf down` it runs records it.
func (f *fakeSignals) start(pid int) (string, error) {
	if pid == os.Getpid() {
		return thisProcessStart, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.alive[pid] {
		return "", nil
	}
	if s, ok := f.starts[pid]; ok {
		return s, nil
	}
	return fakeStart(pid), nil
}

// fakeStart is the start time of a fake process.
func fakeStart(pid int) string { return "start-" + strconv.Itoa(pid) }

// everyPIDStarted makes every pid a live process that started at
// fakeStart, and the test's own process itself.
func everyPIDStarted(pid int) (string, error) {
	if pid == os.Getpid() {
		return thisProcessStart, nil
	}
	return fakeStart(pid), nil
}

func (f *fakeSignals) send(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pid < 0 {
		pid = -pid
	}
	if !f.alive[pid] {
		return syscall.ESRCH
	}
	if sig == 0 {
		return nil
	}
	f.sent = append(f.sent, sig)
	f.sentTo = append(f.sentTo, pid)
	if sig == syscall.SIGKILL || sig == syscall.SIGTERM && !f.ignoreTerm {
		f.alive[pid] = false
		if lock := f.locks[pid]; lock != nil {
			_ = lock.Close()
		}
	}
	return nil
}

// holdRunLock takes the run lock of the install as the `sisf up` process pid
// would, through a file of its own, and records pid and its start time in
// the locked file and in the pid file. The lock is dropped when the test
// ends or the fake process is stopped.
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
	if err := writeOwner(lock, processRecord{Role: roleUp, PID: pid, Start: fakeStart(pid)}); err != nil {
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
	in.Signal, in.ProcessStart = signals.send, signals.start
	autoAdvance(t, in)
	holdRunLock(t, in, pid, signals)
	return in
}

// autoAdvance gives the installer a clock that ends each wait it arms at
// once. A process another test starts in parallel holds every open file for
// a moment while it forks, the run lock too; a stop that sees it then waits
// a poll and looks again, as it does on the wall clock.
func autoAdvance(t *testing.T, in *Installer) {
	t.Helper()
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
	st, err := in.inspectRun()
	if err != nil || st.running() {
		t.Fatalf("inspectRun = %+v %v, want nothing running", st, err)
	}
}

// A pid file survives a reboot, and its pid may then belong to an unrelated
// process. Without the run lock it is stale: nothing is signalled, it is
// removed, and stop, down -v and a new up go ahead.
func TestAPIDFileWhoseRunLockNobodyHoldsIsStaleEvenWhenItsPIDIsAlive(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{alive: map[int]bool{4242: true}}
	in, _, out := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	in.Signal = signals.send
	if err := os.MkdirAll(in.Layout.Dir+"/run", 0o700); err != nil {
		t.Fatal(err)
	}
	// The lock file an earlier `up` of this release left, unlocked.
	if err := os.WriteFile(in.Layout.RunLock(), nil, 0o600); err != nil {
		t.Fatal(err)
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

// An install that no `up` with the run lock ever started has no lock file.
// A live process its pid file names may then be a factory started by an
// older sisf, or an unrelated process after a reboot: it is neither
// signalled nor trusted, the data stays, and the operator is told how to
// tell them apart. A pid file of a process that is gone is stale.
func TestAPIDFileWithoutAnyRunLockFileIsNeitherSignalledNorTrusted(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{alive: map[int]bool{4242: true}}
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	in.Signal = signals.send
	data := in.Layout.Data("state")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(in.Layout.Dir+"/run", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.Layout.PIDFile(), []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func() error{
		"stop":    func() error { return in.Stop(t.Context()) },
		"down -v": func() error { return in.Down(t.Context(), true) },
		"up":      func() error { return in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeStandalone)}) },
	} {
		if err := run(); !core.HasCode(err, core.CodeConflict) || !strings.Contains(err.Error(), "older sisf") {
			t.Errorf("%s = %v, want CONFLICT saying it may be an older sisf", name, err)
		}
	}
	if len(signals.sent) != 0 {
		t.Fatalf("signals %v were sent", signals.sent)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatal("the data was removed")
	}
	if _, err := os.Stat(in.Layout.PIDFile()); err != nil {
		t.Fatal("the pid file was removed")
	}
	signals.alive[4242] = false
	if running, err := in.Running(); err != nil || running {
		t.Fatalf("Running = %v %v once the process is gone", running, err)
	}
	if _, err := os.Stat(in.Layout.PIDFile()); !os.IsNotExist(err) {
		t.Fatal("the pid file of a process that is gone was kept")
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

// The control zone keeps its encrypted secret store below the data
// directory. Down -v removes the data a run made, never those secrets.
func TestDownWithDataKeepsTheControlZonesSecretStore(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in := withPIDFile(t, 4242, signals)
	store := in.Layout.ControlSecretStore()
	for _, f := range []string{
		filepath.Join(store, "control.key"), filepath.Join(store, "control.secrets"),
		in.Layout.StateDB(), in.Layout.Data("control", "other"), in.Layout.Data("workspaces", "w1", "file"),
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := in.Down(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{filepath.Join(store, "control.key"), filepath.Join(store, "control.secrets")} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("down -v removed the secret store file %s", kept)
		}
	}
	for _, gone := range []string{in.Layout.Data("state"), in.Layout.Data("control", "other"), in.Layout.Data("workspaces")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("down -v kept %s", gone)
		}
	}
}

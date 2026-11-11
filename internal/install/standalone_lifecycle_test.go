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
// is alive until it is sent SIGTERM, and every signal is recorded.
type fakeSignals struct {
	mu         sync.Mutex
	alive      map[int]bool
	sent       []syscall.Signal
	ignoreTerm bool
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
	}
	return nil
}

// withPIDFile gives the install a pid file naming a live fake process.
func withPIDFile(t *testing.T, pid int, signals *fakeSignals) *Installer {
	t.Helper()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	if err := os.MkdirAll(in.Layout.Dir+"/run", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.Layout.PIDFile(), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	signals.alive = map[int]bool{pid: true}
	in.Signal = signals.send
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
	signals.alive[4242] = false
	pid, err := in.runningPID()
	if err != nil || pid != 0 {
		t.Fatalf("runningPID = %d %v, want none", pid, err)
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

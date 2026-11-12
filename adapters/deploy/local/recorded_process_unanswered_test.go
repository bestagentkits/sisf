package local

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
)

// recordedStart is a start as ps prints it, which is read with ps on every
// system.
const recordedStart = "Thu Nov 12 02:35:00 2026"

// fakePS puts a ps that runs script first on PATH for the rest of the test.
func fakePS(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// manualClock moves only when something waits on it: After advances the
// clock by the wait and fires at once.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// A recorded process the system could not be asked about is not taken for
// gone: it counts as running, and its stop is not reported done.
func TestARecordedProcessThatCannotBeAskedAboutIsNotTakenForGone(t *testing.T) {
	fakePS(t, "echo no answer\nexit 2")
	rec := slotRecord{PID: os.Getpid(), Start: recordedStart}
	if same, err := sameProcess(rec); same || err == nil {
		t.Fatalf("sameProcess = %v, %v; want an error", same, err)
	}
	p := &recordedProcess{rec: rec, clock: core.SystemClock{}}
	if !p.Alive() {
		t.Fatal("a process that could not be asked about was reported gone")
	}
	if err := p.Stop(context.Background()); err == nil {
		t.Fatal("the stop of a process that could not be asked about was reported done")
	}
}

// A backend built over a slot record whose process cannot be asked about
// refuses to start, UNAVAILABLE, and keeps the record: dropping it would
// leave a process that may still run with nothing to stop it.
func TestABackendRefusesARecordWhoseProcessCannotBeAskedAbout(t *testing.T) {
	dir := t.TempDir()
	rec := slotRecord{Slot: "blue", Port: 41001, Unit: "app", Digest: core.ReleaseDigest("sha256:" + strings.Repeat("a", 64)),
		PID: os.Getpid(), Start: recordedStart}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "blue.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	splitter, err := edge.NewSplitter(core.SystemClock{}, nil, edge.Target{Revision: "bootstrap", URL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	fakePS(t, "echo no answer\nexit 2")
	_, err = New(Config{Factory: "acme-app", Ports: [2]int{41001, 41002}, Starter: ExecStarter{}, Edge: splitter, StateDir: dir})
	if !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("New = %v, want UNAVAILABLE", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the record of a process that could not be asked about is gone: %v", err)
	}
}

// A stop that cannot show, by the deadline, that the pid still names the
// recorded process gives up with an error and sends no kill: the pid may
// name another process by then.
func TestAStopThatCannotConfirmTheProcessAtTheDeadlineSendsNoKill(t *testing.T) {
	// The process ignores SIGTERM, so only a kill would end it.
	app := exec.Command("/bin/sh", "-c", "trap '' TERM; echo ready; exec sleep 60")
	out, err := app.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = app.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = app.Process.Kill()
		<-exited
	})
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("the process did not start: %q, %v", line, err)
	}

	// ps names the process as recorded once, then cannot be asked again.
	answered := filepath.Join(t.TempDir(), "answered")
	fakePS(t, "if [ -e '"+answered+"' ]; then echo no answer; exit 2; fi\n: > '"+answered+"'\necho '"+recordedStart+" S'")
	rec := slotRecord{PID: app.Process.Pid, Start: recordedStart}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := stopRecorded(ctx, rec, &manualClock{now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}); err == nil || ctx.Err() != nil {
		t.Fatalf("stopRecorded = %v, want it to give up with an error before the test's own deadline", err)
	}
	if _, err := os.Stat(answered); err != nil {
		t.Fatal("ps was never asked about the process")
	}
	select {
	case <-exited:
		t.Fatal("a process not shown to be the recorded one was killed")
	case <-time.After(200 * time.Millisecond):
	}
}

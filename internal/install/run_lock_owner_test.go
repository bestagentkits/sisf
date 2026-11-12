package install

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// A crash leaves the pid file of an earlier `up` behind, and a new `up`
// takes the lock before anything else. The process to signal is the one
// recorded in the locked file itself, never the one a pid file names.
func TestStopSignalsTheProcessTheLockedFileNamesNotAStalePIDFile(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in := withPIDFile(t, 4242, signals)
	signals.alive[999] = true // an unrelated process now has the old pid
	if err := os.WriteFile(in.Layout.PIDFile(), []byte("999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := in.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(signals.sentTo, []int{4242}) {
		t.Fatalf("signalled %v, want only the lock's owner 4242", signals.sentTo)
	}
}

// The record in the lock file names a process by its pid and start time.
// When its pid now names a process that started at another time, the
// holder has ended and is never signalled.
func TestAnOwnerWhosePIDWasGivenToAnotherProcessIsNeverSignalled(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{}
	in := withPIDFile(t, 4242, signals)
	signals.starts = map[int]string{4242: "a later start"}
	data := in.Layout.Data("state")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func() error{
		"stop":    func() error { return in.Stop(t.Context()) },
		"down -v": func() error { return in.Down(t.Context(), true) },
		"restart": func() error { return in.Restart(t.Context()) },
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
}

// probeAs holds the run gate and the run lock the way a command that only
// looks does, for a moment, until release is closed.
func probeAs(t *testing.T, in *Installer, release <-chan struct{}) <-chan struct{} {
	t.Helper()
	held := make(chan struct{})
	go func() {
		err := in.withRunGate(func() error {
			f, err := in.openRunLock()
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			if ok, err := in.tryRunLock(f); err != nil || !ok {
				t.Errorf("the probe could not take a free lock: %v", err)
			}
			close(held)
			<-release
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}()
	return held
}

// A command that only looks holds the run lock for a moment when it finds
// it free. Another look, or an `up`, in that moment waits for it instead of
// taking it for a running install.
func TestALookThatHoldsTheRunLockForAMomentIsNotARunningInstall(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	for name, run := range map[string]func() error{
		"look": func() error {
			if running, err := in.Running(); err != nil || running {
				t.Errorf("Running = %v %v while another command looked", running, err)
			}
			return nil
		},
		"up": func() error {
			lock, err := in.acquireRunLock()
			if err == nil {
				lock.release()
			}
			return err
		},
	} {
		release := make(chan struct{})
		<-probeAs(t, in, release)
		done := make(chan error, 1)
		go func() { done <- run() }()
		select {
		case err := <-done:
			t.Fatalf("%s answered %v while another command held the lock for a moment; it must wait for it", name, err)
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		if err := <-done; err != nil {
			t.Errorf("%s = %v once the look ended", name, err)
		}
	}
}

// While `sisf down -v` removes the data it holds the run lock: a look sees
// the install busy, and stop neither signals the removal nor calls the
// install stopped.
func TestARemovalInProgressIsNeitherSignalledNorTakenForAStoppedInstall(t *testing.T) {
	t.Parallel()
	signals := &fakeSignals{alive: map[int]bool{}}
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	in.Signal, in.ProcessStart = signals.send, signals.start
	lock, err := in.takeRunLock(roleDown)
	if err != nil {
		t.Fatal(err)
	}
	defer dropRunLock(lock)
	if running, err := in.Running(); err != nil || !running {
		t.Errorf("Running = %v %v during a removal", running, err)
	}
	if err := in.Stop(t.Context()); !core.HasCode(err, core.CodeConflict) || !strings.Contains(err.Error(), "removing its data") {
		t.Errorf("stop = %v, want CONFLICT that a removal runs", err)
	}
	if _, err := in.acquireRunLock(); !core.HasCode(err, core.CodeConflict) {
		t.Errorf("up = %v, want CONFLICT that a removal runs", err)
	}
	if len(signals.sent) != 0 {
		t.Fatalf("signals %v were sent", signals.sent)
	}
}

// The lock file is opened for writing by every command, also those that
// only look, so flock works on file systems that refuse it on a read-only
// descriptor; a failure other than a held lock is an error, never a free
// lock.
func TestAFlockFailureIsAnErrorNeverAFreeLock(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	mustInit(t, in, manifest.ModeStandalone)
	f, err := in.openRunLock()
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close() // flock on a closed descriptor fails with EBADF
	if ok, err := in.tryRunLock(f); ok || err == nil || !core.HasCode(err, core.CodeUnavailable) {
		t.Fatalf("tryRunLock on a bad descriptor = %v %v, want UNAVAILABLE", ok, err)
	}
	info, err := os.Stat(in.Layout.RunLock())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the run lock has mode %v, want 0600", info.Mode().Perm())
	}
}

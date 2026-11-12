package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bestagentkits/sisf/internal/processstart"
	"github.com/bestagentkits/sisf/pkg/core"
)

// staleStopGrace is how long a recorded process gets to leave after
// SIGTERM before it is killed, and stalePoll how often its exit is looked
// for.
const (
	staleStopGrace = 5 * time.Second
	stalePoll      = 100 * time.Millisecond
)

// identifiedProcess is a Process that can say which operating-system
// process it is. The backend records such a process in its state
// directory, so a later backend can find it again after a restart.
type identifiedProcess interface {
	PID() int
}

// slotRecord is what the backend keeps on disk about the process of one
// slot. Start, when the process started as internal/processstart reads it,
// identifies it beyond its pid, which the system may give to another
// process once this one has gone; it survives the exec by which a launcher
// becomes the release's program. On Linux it is read from /proc, the boot
// and the clock ticks from boot, which a step of the clock does not
// change; elsewhere, and in a record an earlier build wrote, it is ps's
// start to the second, and each record is checked the way it was written.
// Command is the command line when the record was written, kept for the
// log only: a launcher that execs changes it.
type slotRecord struct {
	Slot    string             `json:"slot"`
	Port    int                `json:"port"`
	Unit    string             `json:"unit"`
	Digest  core.ReleaseDigest `json:"digest"`
	PID     int                `json:"pid"`
	Start   string             `json:"start"`
	Command string             `json:"command"`
}

// processIdentity reads when pid started (processstart.Read) and, for the
// log only, the command line it runs as ps prints it, "" when ps cannot
// say. It fails when there is no such process.
func processIdentity(ctx context.Context, pid int) (start, command string, err error) {
	if start, err = processstart.Read(pid); err != nil {
		return "", "", err
	}
	if start == "" {
		return "", "", fmt.Errorf("process %d is gone", pid)
	}
	cmd := exec.CommandContext(ctx, "ps", "-o", "args=", "-p", strconv.Itoa(pid))
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	if out, err := cmd.Output(); err == nil {
		command = strings.TrimSpace(string(out))
	}
	return start, command, nil
}

// slotFile is where the record of a slot is kept.
func (b *Backend) slotFile(s *slot) string {
	return filepath.Join(b.cfg.StateDir, s.name+".json")
}

// recordSlot writes the record of the process a slot now runs. Without a
// state directory, or for a process that cannot say which it is, nothing
// is written. A record that cannot be written is logged: the process runs
// regardless, and only a later restart loses track of it.
func (b *Backend) recordSlot(ctx context.Context, s *slot, unit string, digest core.ReleaseDigest, proc Process) {
	if b.cfg.StateDir == "" {
		return
	}
	identified, ok := proc.(identifiedProcess)
	if !ok {
		return
	}
	rec := slotRecord{Slot: s.name, Port: s.port, Unit: unit, Digest: digest, PID: identified.PID()}
	var err error
	if rec.Start, rec.Command, err = processIdentity(ctx, rec.PID); err == nil {
		err = writeSlotRecord(b.slotFile(s), rec)
	}
	if err != nil {
		b.cfg.Logger.Warn("the process of a slot could not be recorded; a restart will not find it", "slot", s.name, "pid", rec.PID, "error", err.Error())
	}
}

// writeSlotRecord writes rec to path through a temporary file, so a crash
// leaves the old record or the new one, never half of one.
func writeSlotRecord(path string, rec slotRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// forgetSlot removes the record of a slot whose process was stopped.
func (b *Backend) forgetSlot(s *slot) {
	if b.cfg.StateDir == "" {
		return
	}
	if err := os.Remove(b.slotFile(s)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		b.cfg.Logger.Warn("the record of a stopped slot could not be removed", "slot", s.name, "error", err.Error())
	}
}

// reattach rebuilds the slots from the records an earlier backend kept in
// the state directory: a recorded process that still runs as the one
// recorded, one that started at the recorded time, is the slot's process
// again, as the release the record names. A supervisor that stopped and
// started again thus observes, measures, rolls back to and stops what it
// started before, rather than taking the release that serves for gone. The
// slot keeps the port the configuration gives it. A record whose process is
// gone, or whose pid now names another process, is dropped without a
// signal; one whose process runs on another port than its slot's is
// stopped, once its pid is shown to name it, and dropped. A process found
// again is stopped, like any other, when its slot is staged over or rolled
// back from.
func (b *Backend) reattach(ctx context.Context) error {
	if b.cfg.StateDir == "" {
		return nil
	}
	var failed []error
	for _, s := range b.slot {
		if err := b.reattachSlot(ctx, s); err != nil {
			failed = append(failed, fmt.Errorf("slot %s: %w", s.name, err))
		}
	}
	return errors.Join(failed...)
}

// reattachSlot rebuilds one slot from its record, if it has one.
func (b *Backend) reattachSlot(ctx context.Context, s *slot) error {
	path := b.slotFile(s)
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	var rec slotRecord
	if json.Unmarshal(raw, &rec) != nil || rec.PID <= 1 || rec.Slot != s.name || rec.Start == "" ||
		!unitPattern.MatchString(rec.Unit) || rec.Digest.Validate() != nil {
		b.cfg.Logger.Warn("a slot record that does not hold together is dropped without a signal", "slot", s.name)
		return os.Remove(path)
	}
	if !sameProcess(rec) {
		b.cfg.Logger.Info("a recorded process is gone; its record is dropped", "slot", s.name, "pid", rec.PID)
		return os.Remove(path)
	}
	if rec.Port != s.port {
		// The process is the one recorded, but on a port the slot no longer
		// has: no slot serves from it, so it is stopped as stale.
		b.cfg.Logger.Warn("a recorded process listens on another port than its slot's; it is stopped and its record dropped",
			"slot", s.name, "pid", rec.PID, "recorded_port", rec.Port, "port", s.port)
		if err := stopRecorded(ctx, rec, b.cfg.Clock); err != nil {
			return err
		}
		return os.Remove(path)
	}
	b.cfg.Logger.Info("found again a process an earlier supervisor started", "slot", s.name, "pid", rec.PID,
		"release", string(rec.Digest), "command", rec.Command)
	s.digest, s.proc = rec.Digest, &recordedProcess{rec: rec, clock: b.cfg.Clock}
	return nil
}

// Close stops the processes the backend found again after a restart
// (reattach), as the supervisor that owns it stops. A process the backend
// started itself is a child of its owner, in its owner's process group, and
// ends with it; one an earlier supervisor started is in that supervisor's
// group, which no signal to its owner's group reaches, so it is stopped
// here, only while its pid still names the process recorded. The slots are
// stopped together, so closing waits one grace period, not one per slot.
// The processes the backend started itself are left alone.
func (b *Backend) Close(ctx context.Context) error {
	b.ops.Lock()
	defer b.ops.Unlock()
	failed := make([]error, len(b.slot))
	var wg sync.WaitGroup
	for i, s := range b.slot {
		b.mu.Lock()
		_, found := s.proc.(*recordedProcess)
		b.mu.Unlock()
		if !found {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := b.stopSlot(ctx, s); err != nil {
				failed[i] = fmt.Errorf("slot %s: %w", s.name, err)
			}
		}()
	}
	wg.Wait()
	return errors.Join(failed...)
}

// recordedProcess is a process an earlier backend started and recorded,
// found again after a restart. Its pid is trusted only while it names the
// process recorded: it is alive only then, and each signal that stops it is
// sent only after that is shown again. Its standard error was read by the
// backend that started it, so its critical lines are no longer counted.
type recordedProcess struct {
	rec   slotRecord
	clock core.Clock
}

func (p *recordedProcess) PID() int { return p.rec.PID }

func (p *recordedProcess) Alive() bool { return sameProcess(p.rec) }

// Stop ends the recorded process, if its pid still names it.
func (p *recordedProcess) Stop(ctx context.Context) error {
	if !sameProcess(p.rec) {
		return nil
	}
	return stopRecorded(ctx, p.rec, p.clock)
}

// CriticalSince is zero: the lines the process writes are not read.
func (p *recordedProcess) CriticalSince(time.Time) int64 { return 0 }

// sameProcess reports whether rec's pid still names the process recorded:
// a live process that started at the recorded time, read the way the
// record was written. A process that has exited but not been reaped is
// taken as gone. The spaces of a recorded start are not part of it: an
// earlier build kept ps's start as ps prints it, with the day of the month
// padded to two places, which processstart reads with single spaces.
func sameProcess(rec slotRecord) bool {
	start, err := processstart.ReadLike(rec.Start, rec.PID)
	return err == nil && start != "" && start == strings.Join(strings.Fields(rec.Start), " ")
}

// stopRecorded sends SIGTERM to the recorded process, waits for it to
// leave, and kills it after the grace period. Each signal is sent only
// after the pid is shown to name the recorded process still: the caller
// shows it for the first.
func stopRecorded(ctx context.Context, rec slotRecord, clock core.Clock) error {
	proc, err := os.FindProcess(rec.PID)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal process %d: %w", rec.PID, err)
	}
	deadline, killed := clock.Now().Add(staleStopGrace), false
	for sameProcess(rec) {
		if !clock.Now().Before(deadline) {
			if killed {
				return fmt.Errorf("process %d did not leave after it was killed", rec.PID)
			}
			if err := proc.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
				return fmt.Errorf("kill process %d: %w", rec.PID, err)
			}
			deadline, killed = clock.Now().Add(staleStopGrace), true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-clock.After(stalePoll):
		}
	}
	return nil
}

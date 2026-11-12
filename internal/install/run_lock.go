package install

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/bestagentkits/sisf/pkg/core"
)

// RunLock is the lock file of a Standalone install. The foreground `sisf up`
// holds an exclusive advisory lock (flock) on it from before the build, and
// every unit it starts inherits the open file that holds the lock (see
// RunLockFD), so the lock lasts until the last of them has ended: a
// `sisf up` that was killed leaves the install running for as long as one
// of its units runs. The kernel drops the lock when the last process that
// holds it ends, however it ends, so it survives no reboot, and a pid reused
// by an unrelated process does not hold it.
//
// The holder writes its own pid and start time into the locked file, under
// the run gate, and that record is read only while the lock is seen held;
// the pid file is never trusted to name the holder. The file itself is never
// removed; removing it would let a second `up` lock a new file next to a
// running one.
func (l Layout) RunLock() string { return filepath.Join(l.Dir, "run", "sisf.lock") }

// RunLockFD is the file descriptor on which every unit receives the run
// lock: the first after standard input, output and error. The unit keeps it
// open for its whole life and marks it close-on-exec, so the programs it
// starts in turn (agents, builds, the application) do not hold the
// install's lock. No variable announces it: a unit's environment is its
// zone's alone.
const RunLockFD = 3

// Roles of the command that holds the run lock.
const (
	roleUp   = "up"
	roleDown = "down"
)

// runLock is the lock a running `sisf up` holds.
type runLock struct {
	file    *os.File
	pidFile string
}

// runState is what a look at the run lock found. held says some process
// holds the lock; owner is the `sisf up` or `sisf down` that took it, when
// that process still lives; units are the recorded units of this install
// that still live. A lock held with neither is held by a process this
// install cannot name.
type runState struct {
	held  bool
	owner processRecord
	units []processRecord
}

// running reports whether anything of this install runs.
func (s runState) running() bool { return s.held || len(s.units) > 0 }

// unknownHolder reports a lock that is held by no process this install
// recorded.
func (s runState) unknownHolder() bool {
	return s.held && !s.owner.valid() && len(s.units) == 0
}

// withRunGate runs fn while this process holds the run gate, a second lock
// file every attempt on the run lock is made under: by `sisf up`, by
// `sisf down -v`, and by every command that only looks. A command that
// looks holds the run lock for a moment when it finds it free; under the
// gate no other command sees it held then, so a held run lock always means
// a run. The gate is held for a moment only, so it is waited for.
func (in *Installer) withRunGate(fn func() error) error {
	path := in.Layout.RunGate()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return unavailablef(err, "cannot create the run directory")
	}
	gate, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return unavailablef(err, "cannot open the run gate %s", path)
	}
	defer func() { _ = gate.Close() }() // drops the gate
	for {
		err = syscall.Flock(int(gate.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return unavailablef(err, "cannot lock %s; the install directory must be on a file system that supports flock", path)
	}
	return fn()
}

// openRunLock opens the run lock file for reading and writing, which flock
// needs on some network file systems, and creates it when missing.
func (in *Installer) openRunLock() (*os.File, error) {
	path := in.Layout.RunLock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, unavailablef(err, "cannot create the run directory")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, unavailablef(err, "cannot open the run lock %s", path)
	}
	return f, nil
}

// tryRunLock takes the run lock on f without waiting. It answers false when
// another open file holds it; any other failure, such as a file system
// without flock, is an error and never taken for a free lock.
func (in *Installer) tryRunLock(f *os.File) (bool, error) {
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, unavailablef(err, "cannot lock %s; the install directory must be on a file system that supports flock", in.Layout.RunLock())
	}
}

// maxOwnerRecord bounds the record read from the lock file.
const maxOwnerRecord = 4096

// readOwner reads the record of the process that took the run lock. A file
// without one, such as the lock of an older sisf, answers the zero record.
func readOwner(f *os.File) processRecord {
	buf := make([]byte, maxOwnerRecord)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return processRecord{}
	}
	var rec processRecord
	if json.Unmarshal(buf[:n], &rec) != nil || !rec.valid() {
		return processRecord{}
	}
	return rec
}

// writeOwner replaces the record in the lock file f, which this process
// holds. The units share the file's offset, so it is written at offset 0.
func writeOwner(f *os.File, rec processRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err = f.WriteAt(append(raw, '\n'), 0)
	return err
}

// inspectRun looks at the run lock under the run gate. When the lock is
// free, a pid file and unit records left behind, by a crash or from before
// a reboot, are stale: they are removed while the lock is held here, so
// they cannot race an `up` that is starting, and no process they name is
// ever signalled.
func (in *Installer) inspectRun() (runState, error) {
	if !fileExists(in.Layout.RunLock()) {
		if !fileExists(in.Layout.PIDFile()) && !fileExists(in.Layout.UnitRecords()) {
			return runState{}, nil // never started here: nothing to lock or clean
		}
		// An `up` of an older sisf held no lock; it must not be run beside.
		if err := in.refuseUnlockedLivePID(); err != nil {
			return runState{}, err
		}
	}
	var st runState
	err := in.withRunGate(func() error {
		f, err := in.openRunLock()
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }() // also drops a lock taken here
		locked, err := in.tryRunLock(f)
		if err != nil {
			return err
		}
		st.held = !locked
		if st.held {
			if st.owner, err = in.liveOwner(f); err != nil {
				return err
			}
		}
		if st.units, err = in.liveUnits(locked); err != nil {
			return err
		}
		if locked {
			return in.removeStalePIDFile()
		}
		return nil
	})
	return st, err
}

// liveOwner is the record in the held lock file f when the process it names
// still lives, or the zero record.
func (in *Installer) liveOwner(f *os.File) (processRecord, error) {
	owner := readOwner(f)
	ok, err := in.alive(owner)
	if err != nil || !ok {
		return processRecord{}, err
	}
	return owner, nil
}

// takeRunLock takes the run lock for role and records this process in the
// lock file. It refuses while anything of the install runs: a `sisf up`, a
// `sisf down`, a unit a `sisf up` that has ended left running, or a process
// that holds the lock unnamed. The caller holds the lock until it calls
// dropRunLock.
func (in *Installer) takeRunLock(role string) (*os.File, error) {
	if !fileExists(in.Layout.RunLock()) {
		// An `up` of an older sisf held no lock; it must not be run beside.
		if err := in.refuseUnlockedLivePID(); err != nil {
			return nil, err
		}
	}
	var lock *os.File
	err := in.withRunGate(func() error {
		f, err := in.openRunLock()
		if err != nil {
			return err
		}
		locked, err := in.tryRunLock(f)
		if err != nil || !locked {
			st := runState{held: true}
			if err == nil {
				if st.owner, err = in.liveOwner(f); err == nil {
					st.units, err = in.liveUnits(false)
				}
			}
			_ = f.Close() // nothing was locked
			if err != nil {
				return err
			}
			return in.runningRefusal(st)
		}
		units, err := in.liveUnits(true)
		if err == nil && len(units) > 0 {
			err = in.runningRefusal(runState{units: units})
		}
		if err == nil {
			var self processRecord
			if self, err = in.recordSelf(role); err == nil {
				if err = writeOwner(f, self); err != nil {
					err = unavailablef(err, "cannot record this process in the run lock %s", in.Layout.RunLock())
				}
			}
		}
		if err != nil {
			_ = f.Close() // releases the lock
			return err
		}
		lock = f
		return nil
	})
	return lock, err
}

// dropRunLock clears the record of the holder and drops the lock. Units
// that still hold the file keep the lock; the record they leave is that of
// a process that has ended, which is never trusted.
func dropRunLock(f *os.File) {
	_ = f.Truncate(0) // a record left behind names an ended process
	_ = f.Close()     // closing the file drops this process's hold
}

// runningRefusal is the refusal of a second `up`, or of a removal, while
// something of this install runs.
func (in *Installer) runningRefusal(st runState) error {
	switch {
	case st.owner.Role == roleDown:
		return core.NewError(core.CodeConflict, sprintf("a `sisf down` of this install (process %d) is removing its data; wait for it to finish", st.owner.PID))
	case st.owner.valid():
		return invalidf("this install is already running (process %d); stop it first with `sisf stop --dir %s`", st.owner.PID, in.Layout.Dir)
	case len(st.units) > 0:
		return invalidf("this install is already running: units of a `sisf up` that has ended still run (%s); stop them first with `sisf stop --dir %s`",
			describeUnits(st.units), in.Layout.Dir)
	}
	return in.unknownHolderError()
}

// unknownHolderError reports a run lock that no process this install
// recorded holds. Nothing is signalled and nothing is removed: the holder
// may be a program a unit started that kept the lock, or a `sisf up` of an
// older sisf, which records itself in the pid file only.
func (in *Installer) unknownHolderError() error {
	path := in.Layout.RunLock()
	return core.NewError(core.CodeConflict, sprintf(
		"the run lock %s is held, but by no `sisf up` of this install and no unit it recorded: a program a unit started may have kept it open, or a `sisf up` of an older sisf may run; nothing was signalled or removed", path),
		core.NextAction{Action: "lsof " + path, Description: "see which processes hold the run lock"},
		core.NextAction{Action: "kill <pid>", Description: "stop them, then try again"})
}

// acquireRunLock takes the run lock for a `sisf up`. It also writes this
// process to the pid file, for an operator and for an older sisf, which
// knew no other record; this sisf never reads the pid file to find whom to
// signal.
func (in *Installer) acquireRunLock() (*runLock, error) {
	f, err := in.takeRunLock(roleUp)
	if err != nil {
		return nil, err
	}
	pidFile := in.Layout.PIDFile()
	if err := writeSecret(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n")); err != nil {
		dropRunLock(f)
		return nil, err
	}
	return &runLock{file: f, pidFile: pidFile}, nil
}

// release removes the pid file when it still names this process, and then
// drops the lock. A pid file another process wrote is left alone.
func (l *runLock) release() {
	if readPIDFile(l.pidFile) == os.Getpid() {
		_ = os.Remove(l.pidFile) // a pid file without the lock is stale anyway
	}
	dropRunLock(l.file)
}

// refuseUnlockedLivePID handles a pid file next to which no lock file was
// ever made: a `sisf up` of a release without the run lock may still run
// from it, and so may an unrelated process that was given its pid after a
// reboot. Neither may be signalled, nor may its data be removed, so the
// operator is asked to tell them apart. A pid file of a process that is gone
// is stale either way.
func (in *Installer) refuseUnlockedLivePID() error {
	pid := readPIDFile(in.Layout.PIDFile())
	if pid == 0 {
		return nil
	}
	// EPERM: the process exists but belongs to another user.
	if err := in.signal(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return nil
	}
	return core.NewError(core.CodeConflict, sprintf(
		"%s names process %d, which is alive, but this install has no run lock (%s): it may be a factory started by an older sisf, or an unrelated process that was given the pid after a reboot; nothing was signalled or removed",
		in.Layout.PIDFile(), pid, in.Layout.RunLock()),
		core.NextAction{Action: sprintf("ps -p %d -o command", pid), Description: "see what the process is"},
		core.NextAction{Action: sprintf("kill %d", pid), Description: "if it is this install's `sisf up`, stop it, then start it again with this sisf"},
		core.NextAction{Action: "rm " + in.Layout.PIDFile(), Description: "if it is not `sisf up`, remove the stale pid file"})
}

// removeStalePIDFile removes a pid file no running `up` owns, and says so.
func (in *Installer) removeStalePIDFile() error {
	path := in.Layout.PIDFile()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return unavailablef(err, "cannot read the pid file")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return unavailablef(err, "cannot remove the stale pid file %s", path)
	}
	in.say("removed the stale pid file %s (it named process %s, which is not a `sisf up` of this install)\n",
		path, strings.TrimSpace(string(data)))
	return nil
}

// readPIDFile returns the pid a pid file names, or 0 when it names none.
func readPIDFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 0
	}
	return pid
}

// KeepInheritedRunLockFromChildren is called first by a program `sisf up`
// starts as a unit. The unit goes on holding the run lock it inherited on
// RunLockFD, but marks the descriptor close-on-exec, so no program it starts
// in turn inherits the lock: a lingering agent or application would
// otherwise keep the install locked after its units stopped, or unlock it
// under them. It is harmless in a Go program started any other way: every
// file Go opens is close-on-exec already, and a descriptor that is not open
// is left alone.
func KeepInheritedRunLockFromChildren() {
	syscall.CloseOnExec(RunLockFD)
}

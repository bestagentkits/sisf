package install

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/bestagentkits/sisf/pkg/core"
)

// RunLock is the lock file of a Standalone install. The foreground `sisf up`
// holds an exclusive advisory lock (flock) on it for its whole life, from
// before the build until its units have stopped. The lock, not the pid
// file, says whether the install runs: the kernel drops it when the process
// ends, however it ends, so it survives neither a crash nor a reboot, and a
// pid reused by an unrelated process does not hold it. The file itself is
// never removed; removing it would let a second `up` lock a new file next to
// a running one.
func (l Layout) RunLock() string { return filepath.Join(l.Dir, "run", "sisf.lock") }

// runLock is the lock a running `sisf up` holds.
type runLock struct {
	file    *os.File
	pidFile string
}

// errRunLockHeld is what flock answers when another open file holds the lock.
func errRunLockHeld(err error) bool { return errors.Is(err, syscall.EWOULDBLOCK) }

// lockRun takes the run lock of the install without waiting. A lock that
// another process holds is a running install, reported as such; any other
// failure, such as a file system without flock, is an error and never taken
// for a free lock.
func (in *Installer) lockRun() (*os.File, error) {
	path := in.Layout.RunLock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, unavailablef(err, "cannot create the run directory")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, unavailablef(err, "cannot open the run lock %s", path)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close() // nothing was locked
		if errRunLockHeld(err) {
			return nil, in.alreadyRunning()
		}
		return nil, unavailablef(err, "cannot lock %s; the install directory must be on a file system that supports flock", path)
	}
	return f, nil
}

// alreadyRunning is the refusal of a second `up`, or of a removal, while a
// `sisf up` of this install holds the run lock.
func (in *Installer) alreadyRunning() error {
	if pid := readPIDFile(in.Layout.PIDFile()); pid != 0 {
		return invalidf("this install is already running (process %d); stop it first with `sisf stop --dir %s`", pid, in.Layout.Dir)
	}
	return invalidf("this install is already running; stop it first with `sisf stop --dir %s`", in.Layout.Dir)
}

// acquireRunLock takes the run lock for a `sisf up` and records this
// process in the pid file, so that `sisf stop` can reach it even while it is
// still building. The caller holds the lock until it returns.
func (in *Installer) acquireRunLock() (*runLock, error) {
	if !fileExists(in.Layout.RunLock()) {
		// An `up` of an older sisf held no lock; it must not be run beside.
		if err := in.refuseUnlockedLivePID(); err != nil {
			return nil, err
		}
	}
	f, err := in.lockRun()
	if err != nil {
		return nil, err
	}
	pidFile := in.Layout.PIDFile()
	if err := writeSecret(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n")); err != nil {
		_ = f.Close() // releases the lock
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
	_ = l.file.Close() // closing the file drops the lock
}

// runLockHeld reports whether a `sisf up` of this install holds the run
// lock. When none does, any pid file left behind, by a crash or from before
// a reboot, is stale: it is removed while this check holds the lock, so it
// cannot race an `up` that is starting, and the process it names is never
// signalled.
func (in *Installer) runLockHeld() (bool, error) {
	f, err := os.OpenFile(in.Layout.RunLock(), os.O_RDONLY, 0)
	if os.IsNotExist(err) {
		if !fileExists(in.Layout.PIDFile()) {
			return false, nil // never started here: nothing to lock or clean
		}
		if err := in.refuseUnlockedLivePID(); err != nil {
			return false, err
		}
		// A pid file without a lock file is a leftover. The lock is taken
		// before it is removed, as below, so an `up` starting right now
		// keeps its own pid file.
		f, err = os.OpenFile(in.Layout.RunLock(), os.O_RDONLY|os.O_CREATE, 0o600)
	}
	if err != nil {
		return false, unavailablef(err, "cannot open the run lock %s", in.Layout.RunLock())
	}
	defer func() { _ = f.Close() }() // also drops a lock taken below
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); {
	case err == nil:
		return false, in.removeStalePIDFile()
	case errRunLockHeld(err):
		return true, nil
	default:
		return false, unavailablef(err, "cannot test the run lock %s", in.Layout.RunLock())
	}
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

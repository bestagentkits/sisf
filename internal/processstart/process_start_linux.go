package processstart

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// systemStart returns when pid started, read from /proc.
func systemStart(pid int) (string, error) { return procStart(pid) }

// procStart reads when pid started from /proc, or "" when no such process
// lives.
func procStart(pid int) (string, error) { return procStartIn("/proc", pid) }

// procStartIn reads when pid started from proc/<pid>/stat and the boot it
// started in from proc/sys/kernel/random/boot_id, or "" when no process of
// this user has that pid. A boot id that cannot be read is an error, so a
// live process is never taken for gone.
func procStartIn(proc string, pid int) (string, error) {
	raw, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "stat"))
	switch {
	case procEntryGone(err):
		return "", nil
	case err != nil:
		return "", unavailablef(err, "cannot read when process %d started from /proc", pid)
	}
	bootRaw, err := os.ReadFile(filepath.Join(proc, "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return "", unavailablef(err, "cannot read the boot id from /proc")
	}
	boot, err := parseBootID(bootRaw)
	if err != nil {
		return "", err
	}
	return parseProcStat(string(raw), boot, pid)
}

// procEntryGone reports whether err, from reading a process's /proc
// entry, says no process of this user has that pid: the entry does not
// exist, or it may not be read, as for another user's process under
// hidepid. The processes a caller records run as the user who started
// them, so such a process is not one of them.
func procEntryGone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) ||
		errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

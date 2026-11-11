package install

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// systemStart returns when pid started, read from /proc: the clock ticks
// from boot, which neither a change of time zone nor the ps of BusyBox
// alters, so a live unit never looks gone.
func systemStart(pid int) (string, error) { return procStart(pid) }

// procStart reads when pid started from /proc/<pid>/stat, or "" when no
// such process lives.
func procStart(pid int) (string, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH):
		return "", nil
	case err != nil:
		return "", unavailablef(err, "cannot read when process %d started from /proc", pid)
	}
	return parseProcStat(string(raw), pid)
}

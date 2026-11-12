// Package processstart reads when a process started, so that a record of a
// process can tell it from a later one the system gave the same pid. On
// Linux the start is read from /proc: the boot id and the clock ticks from
// boot to the start, field 22 of /proc/<pid>/stat, labelled ProcMethod
// ("proc:<boot id>:<ticks>"). Neither a step of the clock, a change of time
// zone nor the ps of BusyBox alters it, and the boot id keeps a process of
// a later boot given the same pid at the same tick from matching.
// Elsewhere it is the start as ps prints it in the C locale, with no label,
// to the second. A record is checked by the method that wrote it
// (ReadLike), so a value read one way never matches one read the other,
// and a record written by ps before Linux read /proc is still checked by
// ps.
package processstart

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// ProcMethod labels a start time read from /proc/<pid>/stat.
const ProcMethod = "proc:"

// psTimeout bounds one ps call.
const psTimeout = 5 * time.Second

// Read returns when pid started, by this system's method, or "" when no
// such process lives. A process that has exited but was not reaped yet is
// gone: it holds no file and writes nothing.
func Read(pid int) (string, error) { return systemStart(pid) }

// ReadLike returns when pid started, read by the method that produced
// recorded, or "" when no such process lives.
func ReadLike(recorded string, pid int) (string, error) {
	if strings.HasPrefix(recorded, ProcMethod) {
		return procStart(pid)
	}
	return psStart(pid)
}

// unavailablef is an UNAVAILABLE error: the start could not be read now.
func unavailablef(cause error, format string, args ...any) *core.Error {
	return core.NewError(core.CodeUnavailable, fmt.Sprintf(format, args...)).WithCause(cause)
}

// parseBootID reads the boot id from raw, the content of
// /proc/sys/kernel/random/boot_id: one word of letters, digits and dashes
// on one line.
func parseBootID(raw []byte) (string, error) {
	id := strings.TrimSuffix(string(raw), "\n")
	valid := id != ""
	for _, r := range id {
		valid = valid && (r == '-' || ('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z'))
	}
	if !valid {
		return "", unavailablef(nil, "the boot id %q is not one word of letters, digits and dashes", id)
	}
	return id, nil
}

// parseProcStat reads the start time from content, the text of
// /proc/<pid>/stat in the boot boot, labelled with ProcMethod and the boot,
// or "" for a process that has exited but was not reaped yet (state Z or
// X). The command name, field 2, is in parentheses and may hold spaces and
// parentheses itself, so the fields are counted after the last ")": the
// state, field 3, comes first, and the start time is field 22.
func parseProcStat(content, boot string, pid int) (string, error) {
	end := strings.LastIndexByte(content, ')')
	if end < 0 {
		return "", unavailablef(nil, "/proc/%d/stat names no command, so it gives no start time", pid)
	}
	fields := strings.Fields(content[end+1:])
	const state, start = 0, 22 - 3
	if len(fields) <= start {
		return "", unavailablef(nil, "/proc/%d/stat has %d fields after the command, too few for a start time", pid, len(fields))
	}
	if fields[state] == "Z" || fields[state] == "X" {
		return "", nil
	}
	if _, err := strconv.ParseUint(fields[start], 10, 64); err != nil {
		return "", unavailablef(err, "/proc/%d/stat gives %q as the start time, not a number of clock ticks", pid, fields[start])
	}
	return ProcMethod + boot + ":" + fields[start], nil
}

// psStart asks ps when pid started, in the C locale so the answer is the
// same text every time.
func psStart(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), psTimeout)
	defer cancel()
	return psStartWithin(ctx, pid)
}

// psStartWithin is psStart bounded by ctx.
func psStartWithin(ctx context.Context, pid int) (string, error) {
	cmd := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-o", "stat=", "-p", strconv.Itoa(pid))
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	fields := strings.Fields(string(out))
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		// ps was cut off: it did not say the process is gone.
		return "", unavailablef(ctx.Err(), "ps did not say in time when process %d started", pid)
	case err != nil && errors.As(err, &exit) && exit.Exited() && exit.ExitCode() == 1 && len(fields) == 0 && len(exit.Stderr) == 0:
		// ps found no such process: it says so by exiting with 1 and
		// printing nothing, on macOS and with Linux procps alike. A ps ended
		// by a signal, exiting otherwise, or printing an error (one that
		// rejects its arguments exits with 1 too) said nothing about the
		// process. Output leaves cmd.Stderr unset, so exit.Stderr holds it.
		return "", nil
	case err != nil:
		return "", unavailablef(err, "cannot ask ps when process %d started", pid)
	case len(fields) < 6:
		return "", unavailablef(nil, "ps answered %q for process %d, not a start time and a state", strings.TrimSpace(string(out)), pid)
	}
	// lstart is five words ("Thu Nov 12 02:35:00 2026"); stat follows.
	if strings.HasPrefix(fields[5], "Z") {
		return "", nil
	}
	return strings.Join(fields[:5], " "), nil
}

package install

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// processRecord identifies a process of a Standalone install beyond its
// pid, which the system gives to another process once this one has gone:
// Start is the time it started, labelled with the method that read it. On
// Linux it is procStartMethod and the clock ticks from boot to the start,
// field 22 of /proc/<pid>/stat, which neither a change of time zone nor
// the ps of BusyBox alters; elsewhere, and in a record written before
// Linux read /proc, it is the start as ps prints it in the C locale, with
// no label. A record is checked by the method that wrote it, so a value
// read one way never matches one read the other. Role is "up" or "down"
// for the command that holds the run lock, or the name of a unit.
type processRecord struct {
	Role  string `json:"role"`
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

// valid reports whether the record names a process at all.
func (r processRecord) valid() bool { return r.Role != "" && r.PID > 1 && r.Start != "" }

// processStartTimeout bounds one ps call.
const processStartTimeout = 5 * time.Second

// procStartMethod labels a start time read from /proc/<pid>/stat.
const procStartMethod = "proc:"

// processStart returns when pid started, by this system's method
// (systemStart), or "" when no such process lives.
func (in *Installer) processStart(pid int) (string, error) {
	if in.ProcessStart != nil {
		return in.ProcessStart(pid)
	}
	return systemStart(pid)
}

// processStartLike returns when pid started, read by the method that
// produced recorded, or "" when no such process lives.
func (in *Installer) processStartLike(recorded string, pid int) (string, error) {
	switch {
	case in.ProcessStart != nil:
		return in.ProcessStart(pid)
	case strings.HasPrefix(recorded, procStartMethod):
		return procStart(pid)
	}
	return psStart(pid)
}

// parseProcStat reads the start time from content, the text of
// /proc/<pid>/stat, labelled with procStartMethod, or "" for a process that
// has exited but was not reaped yet (state Z or X): it holds no file and
// writes nothing. The command name, field 2, is in parentheses and may
// hold spaces and parentheses itself, so the fields are counted after the
// last ")": the state, field 3, comes first, and the start time is field
// 22.
func parseProcStat(content string, pid int) (string, error) {
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
	return procStartMethod + fields[start], nil
}

// psStart asks ps when pid started, in the C locale so the answer is the
// same text every time. A process that has exited but was not reaped yet is
// reported as gone: it holds no file and writes nothing.
func psStart(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), processStartTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-o", "stat=", "-p", strconv.Itoa(pid))
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	fields := strings.Fields(string(out))
	var exit *exec.ExitError
	switch {
	case err != nil && errors.As(err, &exit) && len(fields) == 0:
		return "", nil // ps found no such process
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

// alive reports whether rec still names the process recorded: a live
// process that started at the recorded time.
func (in *Installer) alive(rec processRecord) (bool, error) {
	if !rec.valid() {
		return false, nil
	}
	start, err := in.processStartLike(rec.Start, rec.PID)
	if err != nil {
		return false, err
	}
	return start == rec.Start, nil
}

// recordSelf is the record of this process in role.
func (in *Installer) recordSelf(role string) (processRecord, error) {
	pid := os.Getpid()
	start, err := in.processStart(pid)
	if err != nil {
		return processRecord{}, err
	}
	if start == "" {
		return processRecord{}, unavailablef(nil, "cannot tell when this process (%d) started", pid)
	}
	return processRecord{Role: role, PID: pid, Start: start}, nil
}

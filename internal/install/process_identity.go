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
// Start is the time it started, as ps prints it in the C locale. Role is
// "up" or "down" for the command that holds the run lock, or the name of a
// unit.
type processRecord struct {
	Role  string `json:"role"`
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

// valid reports whether the record names a process at all.
func (r processRecord) valid() bool { return r.Role != "" && r.PID > 1 && r.Start != "" }

// processStartTimeout bounds one ps call.
const processStartTimeout = 5 * time.Second

// processStart returns when pid started, or "" when no such process lives.
func (in *Installer) processStart(pid int) (string, error) {
	if in.ProcessStart != nil {
		return in.ProcessStart(pid)
	}
	return psStart(pid)
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
	start, err := in.processStart(rec.PID)
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

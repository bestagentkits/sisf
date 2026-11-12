package install

import (
	"os"

	"github.com/bestagentkits/sisf/internal/processstart"
)

// processRecord identifies a process of a Standalone install beyond its
// pid, which the system gives to another process once this one has gone:
// Start is the time it started, labelled with the method that read it
// (internal/processstart). On Linux it is processstart.ProcMethod, the
// boot id and the clock ticks from boot to the start, field 22 of
// /proc/<pid>/stat, which neither a change of time zone nor the ps of
// BusyBox alters, and the boot id keeps a process of a later boot given
// the same pid at the same tick from matching; elsewhere, and in a record
// written before Linux read /proc, it is the start as ps prints it in the
// C locale, with no label. A record is checked by the method that wrote
// it, so a value read one way never matches one read the other. A record
// labelled processstart.ProcMethod with no boot id, as written before the
// boot was recorded, never matches what /proc gives now, so it names no
// live process: it is stale, and its process group is never signalled,
// since the boot it was read in cannot be proven. A unit such a record
// names that still runs holds the run lock it inherited, so up and down
// refuse while it runs rather than start beside it or signal it. Role is
// "up" or "down" for the command that holds the run lock, or the name of a
// unit.
type processRecord struct {
	Role  string `json:"role"`
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

// valid reports whether the record names a process at all.
func (r processRecord) valid() bool { return r.Role != "" && r.PID > 1 && r.Start != "" }

// processStart returns when pid started, by this system's method
// (processstart.Read), or "" when no such process lives.
func (in *Installer) processStart(pid int) (string, error) {
	if in.ProcessStart != nil {
		return in.ProcessStart(pid)
	}
	return processstart.Read(pid)
}

// processStartLike returns when pid started, read by the method that
// produced recorded (processstart.ReadLike), or "" when no such process
// lives.
func (in *Installer) processStartLike(recorded string, pid int) (string, error) {
	if in.ProcessStart != nil {
		return in.ProcessStart(pid)
	}
	return processstart.ReadLike(recorded, pid)
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

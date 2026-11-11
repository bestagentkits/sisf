package install

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// identifiedProcess is a Process that can say which operating-system
// process it is, so it can be recorded and found again.
type identifiedProcess interface {
	PID() int
}

// unitRecordFile is where the record of the unit name is kept.
func (in *Installer) unitRecordFile(name string) string {
	return filepath.Join(in.Layout.UnitRecords(), name+".json")
}

// recordUnit records the process of a unit `sisf up` started, with the time
// it started, while that `sisf up` holds the run lock. A unit whose process
// the record cannot identify could not be stopped once this process ended,
// so a record that cannot be written is an error. The returned function
// forgets the record once the unit has stopped.
func (in *Installer) recordUnit(name string, p Process) (func(), error) {
	identified, ok := p.(identifiedProcess)
	if !ok {
		return func() {}, nil // not an operating-system process: nothing outlives this one
	}
	pid := identified.PID()
	start, err := in.processStart(pid)
	if err != nil {
		return nil, err
	}
	if start == "" {
		return func() {}, nil // it has already ended
	}
	raw, err := json.Marshal(processRecord{Role: name, PID: pid, Start: start})
	if err != nil {
		return nil, err
	}
	path := in.unitRecordFile(name)
	if err := writeSecret(path+".tmp", raw); err != nil {
		return nil, err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return nil, unavailablef(err, "cannot record the process of %s", name)
	}
	return func() {
		// A record a newer start of the unit wrote is not this one's.
		if rec, ok := readUnitRecord(path); ok && rec.PID == pid {
			_ = os.Remove(path) // a record left behind is checked by start time anyway
		}
	}, nil
}

// readUnitRecord reads one unit record.
func readUnitRecord(path string) (processRecord, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return processRecord{}, false
	}
	var rec processRecord
	if json.Unmarshal(raw, &rec) != nil || !rec.valid() {
		return processRecord{}, false
	}
	return rec, true
}

// liveUnits returns the recorded units that still live: whose pid names a
// live process that started at the recorded time. A record whose process is
// gone is stale; it is removed when clean is set, which the caller may only
// ask while it holds the run lock.
func (in *Installer) liveUnits(clean bool) ([]processRecord, error) {
	dir := in.Layout.UnitRecords()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, unavailablef(err, "cannot read the unit records in %s", dir)
	}
	var live []processRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		rec, ok := readUnitRecord(path)
		alive := false
		if ok {
			if alive, err = in.alive(rec); err != nil {
				return nil, err
			}
		}
		switch {
		case alive:
			live = append(live, rec)
		case clean:
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, unavailablef(err, "cannot remove the stale unit record %s", path)
			}
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Role < live[j].Role })
	return live, nil
}

// signalUnits sends sig to the process group of each unit, which `sisf up`
// started as the leader of a group of its own, so helpers it started go too.
// Each unit was shown to be the recorded process just before; one that has
// ended since is skipped.
func (in *Installer) signalUnits(units []processRecord, sig syscall.Signal) error {
	for _, u := range units {
		if err := in.signal(-u.PID, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			return unavailablef(err, "cannot signal %s (process %d)", u.Role, u.PID)
		}
	}
	return nil
}

// describeUnits names the units and their processes for a message.
func describeUnits(units []processRecord) string {
	parts := make([]string, len(units))
	for i, u := range units {
		parts[i] = sprintf("%s: process %d", u.Role, u.PID)
	}
	return strings.Join(parts, ", ")
}

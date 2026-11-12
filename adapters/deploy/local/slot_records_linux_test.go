package local_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/processstart"
)

// On Linux the process of a slot is recorded by its start read from /proc,
// its boot and clock ticks from boot, which a step of the clock does not
// change, rather than by ps's start to the second; a restarted backend
// finds it again by that start.
func TestOnLinuxASlotProcessIsRecordedByItsStartInProc(t *testing.T) {
	t.Parallel()
	app := startSleeper(t)
	_, dir := stagedWithRecord(t, app)
	rec, _ := readSlotRecord(t, filepath.Join(dir, "blue.json"))
	if !strings.HasPrefix(rec.Start, processstart.ProcMethod) {
		t.Fatalf("the slot's process is recorded as started at %q, want a start read from /proc", rec.Start)
	}
	restarted(t, dir)
	if again, ok := readSlotRecord(t, filepath.Join(dir, "blue.json")); !ok || again != rec {
		t.Fatalf("a restarted backend dropped or changed the record %+v: %+v", rec, again)
	}
}

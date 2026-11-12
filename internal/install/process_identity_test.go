package install

import (
	"os"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/processstart"
)

// A record names the method that read its start time, so a value read one
// way never matches one read the other way: this process is alive by its
// own record, and not by the same value under the other method's label.
func TestARecordMatchesOnlyAStartTimeReadTheSameWay(t *testing.T) {
	t.Parallel()
	in := &Installer{}
	self, err := in.recordSelf(roleUp)
	if err != nil {
		t.Fatal(err)
	}
	if alive, err := in.alive(self); err != nil || !alive {
		t.Fatalf("this process by its own record %+v: alive %v, %v", self, alive, err)
	}
	forged := self
	if raw, ok := strings.CutPrefix(self.Start, processstart.ProcMethod); ok {
		forged.Start = raw
	} else {
		forged.Start = processstart.ProcMethod + self.Start
	}
	if alive, err := in.alive(forged); err != nil || alive {
		t.Fatalf("this process by a record of the other method %+v: alive %v, %v; want no match", forged, alive, err)
	}
	if forged.PID != os.Getpid() {
		t.Fatal("the forged record names another process")
	}
}

package install

import (
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/processstart"
)

// On Linux a process of the install is recorded by its start read from
// /proc, which names the boot it was read in (internal/processstart). A
// record without a boot, as written before the boot was recorded, never
// matches at all, nor does one of another boot: it is stale, so its
// process group is never signalled. A live unit such a record names still
// holds the run lock it inherited, so up and down refuse while it runs
// instead of starting a second set or signalling it.
func TestAProcStartNamesTheBootItWasReadIn(t *testing.T) {
	t.Parallel()
	in := &Installer{}
	self, err := in.recordSelf(roleUp)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(self.Start, ":")
	if len(parts) != 3 || parts[0]+":" != processstart.ProcMethod || parts[1] == "" || parts[2] == "" {
		t.Fatalf("this process is recorded as %q, want proc:<boot id>:<ticks>", self.Start)
	}
	if alive, err := in.alive(self); err != nil || !alive {
		t.Fatalf("this process by its own record: alive %v, %v", alive, err)
	}
	withoutBoot := self
	withoutBoot.Start = processstart.ProcMethod + parts[2]
	if alive, err := in.alive(withoutBoot); err != nil || alive {
		t.Fatalf("this process by a record without its boot %q: alive %v, %v; want stale", withoutBoot.Start, alive, err)
	}
	otherBoot := self
	otherBoot.Start = processstart.ProcMethod + "0a1b2c3d-0000-4000-8000-000000000000:" + parts[2]
	if alive, err := in.alive(otherBoot); err != nil || alive {
		t.Fatalf("this process by a record of another boot: alive %v, %v; want stale", alive, err)
	}
}

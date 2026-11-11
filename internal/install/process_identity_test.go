package install

import (
	"os"
	"strings"
	"testing"
)

// procStatLine is a line of /proc/<pid>/stat for a process named comm, in
// state, started at start clock ticks after boot (field 22).
func procStatLine(pid, comm, state, start string) string {
	fields := []string{pid, "(" + comm + ")", state}
	for i := 4; i <= 52; i++ {
		switch i {
		case 22:
			fields = append(fields, start)
		default:
			fields = append(fields, "0")
		}
	}
	return strings.Join(fields, " ") + "\n"
}

// bootSample is a boot id as /proc/sys/kernel/random/boot_id gives it.
const bootSample = "6f1c2b9e-3a4d-4e5f-8a7b-9c0d1e2f3a4b"

// The start time is field 22 of /proc/<pid>/stat, counted after the last
// ")" so a command name holding spaces or parentheses cannot shift it; it
// is labelled with the method that read it and the boot it was read in. A
// zombie or dead process is gone, and a line that is not a stat line is
// refused.
func TestTheProcStatParserReadsTheStartTimeAfterTheCommandName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, line, want string
	}{
		{"plain", procStatLine("4242", "factoryd", "S", "987654"), "proc:" + bootSample + ":987654"},
		{"spaces", procStatLine("4242", "my unit name", "R", "12"), "proc:" + bootSample + ":12"},
		{"parentheses", procStatLine("4242", "a) S 1 2 3 (b", "S", "777"), "proc:" + bootSample + ":777"},
		{"closing parenthesis last", procStatLine("4242", "evil)", "D", "31337"), "proc:" + bootSample + ":31337"},
		{"zombie", procStatLine("4242", "factoryd", "Z", "5"), ""},
		{"dead", procStatLine("4242", "factoryd", "X", "5"), ""},
	} {
		got, err := parseProcStat(tc.line, bootSample, 4242)
		if err != nil || got != tc.want {
			t.Errorf("%s: parseProcStat = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	for name, line := range map[string]string{
		"no command name":   "4242 factoryd S 1 2 3",
		"too few fields":    "4242 (factoryd) S 1 2 3",
		"start not numeric": procStatLine("4242", "factoryd", "S", "soon"),
		"empty":             "",
	} {
		if got, err := parseProcStat(line, bootSample, 4242); err == nil {
			t.Errorf("%s: parseProcStat = %q, want an error", name, got)
		}
	}
}

// The boot id is read as the kernel writes it, one line; anything that is
// not one word of letters, digits and dashes is refused, so a start time
// can never be labelled with an empty or ambiguous boot.
func TestTheBootIDIsOneWordOrRefused(t *testing.T) {
	t.Parallel()
	if got, err := parseBootID([]byte(bootSample + "\n")); err != nil || got != bootSample {
		t.Fatalf("parseBootID = %q, %v; want %q", got, err, bootSample)
	}
	for _, raw := range []string{"", "\n", "a:b\n", "two words\n", "6f1c\n2b9e\n"} {
		if got, err := parseBootID([]byte(raw)); err == nil {
			t.Errorf("parseBootID(%q) = %q, want an error", raw, got)
		}
	}
}

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
	if raw, ok := strings.CutPrefix(self.Start, procStartMethod); ok {
		forged.Start = raw
	} else {
		forged.Start = procStartMethod + self.Start
	}
	if alive, err := in.alive(forged); err != nil || alive {
		t.Fatalf("this process by a record of the other method %+v: alive %v, %v; want no match", forged, alive, err)
	}
	if forged.PID != os.Getpid() {
		t.Fatal("the forged record names another process")
	}
}

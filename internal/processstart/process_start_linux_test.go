package processstart

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// procTree is a /proc tree holding the boot id boot and, for pid, a stat
// line of a process started at start clock ticks after boot.
func procTree(t *testing.T, boot string, pid int, start string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "sys", "kernel", "random")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "boot_id"), []byte(boot+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, strconv.Itoa(pid)), 0o755); err != nil {
		t.Fatal(err)
	}
	line := procStatLine(strconv.Itoa(pid), "factoryd", "S", start)
	if err := os.WriteFile(filepath.Join(root, strconv.Itoa(pid), "stat"), []byte(line), 0o444); err != nil {
		t.Fatal(err)
	}
	return root
}

// A start read from /proc names the boot it was read in, so a record
// written before a reboot never matches a process given the same pid and
// clock ticks after it. On Linux this system's method is /proc.
func TestAProcStartNamesTheBootItWasReadIn(t *testing.T) {
	t.Parallel()
	root := procTree(t, bootSample, 4242, "987654")
	got, err := procStartIn(root, 4242)
	if want := "proc:" + bootSample + ":987654"; err != nil || got != want {
		t.Fatalf("procStartIn = %q, %v; want %q", got, err, want)
	}
	rebooted := procTree(t, "0a1b2c3d-0000-4000-8000-000000000000", 4242, "987654")
	if again, err := procStartIn(rebooted, 4242); err != nil || again == got {
		t.Fatalf("the same pid and ticks after a reboot read %q, %v; want another start than %q", again, err, got)
	}
	self, err := Read(os.Getpid())
	if parts := strings.Split(self, ":"); err != nil || len(parts) != 3 || parts[0]+":" != ProcMethod || parts[1] == "" || parts[2] == "" {
		t.Fatalf("this process reads as %q, %v; want proc:<boot id>:<ticks>", self, err)
	}
}

// A boot id that cannot be read fails the read of a live process's start,
// so nothing is taken for gone, and nothing signalled, on a guess.
func TestAnUnreadableBootIDIsAnError(t *testing.T) {
	t.Parallel()
	root := procTree(t, bootSample, 4242, "987654")
	if err := os.Remove(filepath.Join(root, "sys", "kernel", "random", "boot_id")); err != nil {
		t.Fatal(err)
	}
	if got, err := procStartIn(root, 4242); err == nil {
		t.Fatalf("procStartIn without a boot id = %q, want an error", got)
	}
}

// A process whose /proc entry may not be read, as for another user's
// process under hidepid, is not one the caller recorded, which run as the
// user who started them: it is gone to the caller, not an error.
func TestAProcEntryThatMayNotBeReadIsAnotherUsersProcess(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads every /proc entry")
	}
	root := procTree(t, bootSample, 4242, "987654")
	if err := os.Chmod(filepath.Join(root, "4242", "stat"), 0); err != nil {
		t.Fatal(err)
	}
	if got, err := procStartIn(root, 4242); err != nil || got != "" {
		t.Fatalf("procStartIn of an unreadable entry = %q, %v; want gone", got, err)
	}
	for _, errno := range []syscall.Errno{syscall.EACCES, syscall.EPERM, syscall.ENOENT, syscall.ESRCH} {
		if !procEntryGone(&fs.PathError{Op: "open", Path: "/proc/4242/stat", Err: errno}) {
			t.Errorf("%v: not taken for a process that is not the caller's", errno)
		}
	}
	if procEntryGone(&fs.PathError{Op: "read", Path: "/proc/4242/stat", Err: syscall.EIO}) || procEntryGone(errors.New("other")) {
		t.Error("an input/output error was taken for a gone process")
	}
}

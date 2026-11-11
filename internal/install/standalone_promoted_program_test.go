package install

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/pkg/core"
)

// storeProgram keeps data in the program store of the trust volume dir.
func storeProgram(t *testing.T, dir, data string) core.Digest {
	t.Helper()
	store, err := programstore.Open(dir)
	must(t, err)
	in, err := store.Receive()
	must(t, err)
	defer in.Abort()
	_, err = in.Write([]byte(data))
	must(t, err)
	digest, err := in.Commit()
	must(t, err)
	return digest
}

func promotedEntry(unit string, digest core.Digest) []byte {
	return []byte(`{"unit":"` + unit + `","digest":"` + string(digest) + `","image":"smoke.local/app@` + string(digest) +
		`","source":"promoted","verified_at":"2026-11-11T08:00:00Z","recorded_at":"2026-11-11T08:00:00Z"}`)
}

// The launcher starts the installed application for a release that names
// no program, and exactly the program it is given for one that does, with
// the address the backend chose and the application's data directory.
func TestTheLauncherStartsTheProgramItIsGivenOrTheInstalledApplication(t *testing.T) {
	t.Parallel()
	in, _, _ := newTestInstaller(t)
	must(t, os.MkdirAll(filepath.Dir(in.Layout.Bin("x")), 0o755))
	script := func(path, who string) {
		must(t, os.WriteFile(path, []byte("#!/bin/sh\necho \""+who+" on $HELLO_ADDR with $HELLO_DATA_DIR\"\n"), 0o755))
	}
	script(in.Layout.Bin("hello-app"), "installed")
	candidate := filepath.Join(t.TempDir(), "sha256-candidate")
	script(candidate, "candidate")
	must(t, in.writeAppLauncher())

	run := func(args ...string) string {
		cmd := exec.Command(in.Layout.Bin(appLauncher), args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "SISF_LISTEN=127.0.0.1:7462"}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("the launcher with %v: %v\n%s", args, err, out.String())
		}
		return strings.TrimSpace(out.String())
	}
	suffix := " on 127.0.0.1:7462 with " + in.Layout.Data("app")
	if got := run("local/f/app@sha256:aa"); got != "installed"+suffix {
		t.Errorf("a release with no program started %q", got)
	}
	if got := run("smoke.local/app@sha256:bb", candidate); got != "candidate"+suffix {
		t.Errorf("a release with a program started %q, want that program", got)
	}
}

// `sisf up` of a Standalone install keeps the entry of a promoted release
// whose program the supervisor's store still holds, so the factory comes
// back serving what it promoted. An entry whose program is gone or changed
// is removed, as before, and the operator is told the application starts
// from the installed release again.
func TestUpKeepsAPromotedReleaseWhoseProgramIsInTheStore(t *testing.T) {
	t.Parallel()
	_, files := issuedTrust(t)
	in, _, out := newTestInstaller(t)
	dir := trustDir(t)
	kept := storeProgram(t, dir, "the promoted candidate")
	entry := filepath.Join(dir, promotedLKGPrefix+"app.json")
	must(t, os.WriteFile(entry, promotedEntry("app", kept), 0o600))

	must(t, in.writeStandaloneTrust(dir, files, false))
	if raw, err := os.ReadFile(entry); err != nil || !bytes.Equal(raw, promotedEntry("app", kept)) {
		t.Fatalf("the promoted release whose program is in the store was not kept: %q, %v", raw, err)
	}
	if out.Len() != 0 {
		t.Errorf("a kept promotion was announced as dropped: %s", out.String())
	}

	// The program changed after it was promoted: it will not be run, so
	// the entry is not kept either.
	store, err := programstore.Open(dir)
	must(t, err)
	path, err := store.Path(kept)
	must(t, err)
	must(t, os.Chmod(path, 0o700))
	must(t, os.WriteFile(path, []byte("swapped"), 0o700))
	must(t, in.writeStandaloneTrust(dir, files, false))
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Error("a promoted release whose program changed was kept")
	}
	if !strings.Contains(out.String(), string(kept)) || !strings.Contains(out.String(), "installed release") {
		t.Errorf("the operator was not told the promotion is dropped: %q", out.String())
	}
}

// An entry that names no program of the store, as every promotion of the
// simulation does, or that is not an entry at all, is removed; the
// simulation is not told about it, since it never ran a promoted program.
func TestUpRemovesAPromotedReleaseWithoutAProgram(t *testing.T) {
	t.Parallel()
	_, files := issuedTrust(t)
	for name, tc := range map[string]struct {
		entry []byte
		sim   bool
		told  bool
	}{
		"a promotion with no program":      {entry: promotedEntry("app", "sha256:"+core.Digest(strings.Repeat("c", 64))), told: true},
		"a promotion of the simulation":    {entry: promotedEntry("app", "sha256:"+core.Digest(strings.Repeat("c", 64))), sim: true},
		"an entry that cannot be read":     {entry: []byte("{}"), told: true},
		"an entry that names another unit": {entry: promotedEntry("control", "sha256:"+core.Digest(strings.Repeat("c", 64))), told: true},
	} {
		in, _, out := newTestInstaller(t)
		dir := trustDir(t)
		entry := filepath.Join(dir, promotedLKGPrefix+"app.json")
		must(t, os.WriteFile(entry, tc.entry, 0o600))
		must(t, in.writeStandaloneTrust(dir, files, tc.sim))
		if _, err := os.Stat(entry); !os.IsNotExist(err) {
			t.Errorf("%s: the entry was kept", name)
		}
		if told := strings.Contains(out.String(), "installed release"); told != tc.told {
			t.Errorf("%s: the operator was told %q", name, out.String())
		}
	}
}

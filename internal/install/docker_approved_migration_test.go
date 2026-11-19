package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/manifest"
)

// upDockerApproving installs a Docker factory, then runs `up` again with an
// approval of migration 12, the dump of the state container answering dump.
func upDockerApproving(t *testing.T, dump func(Cmd) ([]byte, error)) (*Installer, *fakeExec, error) {
	t.Helper()
	in, exec := upDocker(t)
	before := len(exec.lines())
	exec.answer = func(c Cmd) ([]byte, error) {
		if strings.Contains(strings.Join(c.Args, " "), "pg_dump") {
			return dump(c)
		}
		return buildAnswers(c)
	}
	err := in.Up(t.Context(), UpOptions{Init: initOptions(in, manifest.ModeDocker), ApproveMigrations: []int{12}})
	exec.mu.Lock()
	exec.runs = exec.runs[before:]
	exec.mu.Unlock()
	return in, exec, err
}

// The Owner's approval of a migration in Docker mode dumps the state
// database through its container, to a file of the install's backups only
// its owner can read, after the store is healthy and before the state step
// runs; the state step is given the approval and the dump's name.
func TestAnApprovedDockerMigrationDumpsTheStoreBeforeTheStateStep(t *testing.T) {
	t.Parallel()
	in, exec, err := upDockerApproving(t, func(Cmd) ([]byte, error) { return []byte("PGDMP dump"), nil })
	if err != nil {
		t.Fatalf("Up with an approval: %v", err)
	}
	var order []string
	for _, l := range exec.lines() {
		for _, step := range []string{"up -d --wait state", "exec -T state pg_dump", "run --rm --no-deps state-bootstrap"} {
			if strings.Contains(l, step) {
				order = append(order, step)
			}
		}
	}
	if strings.Join(order, "|") != "up -d --wait state|exec -T state pg_dump|run --rm --no-deps state-bootstrap" {
		t.Fatalf("the steps ran in the order %v", order)
	}
	entries, err := os.ReadDir(in.Layout.Backups())
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".dump") {
		t.Fatalf("backups = %v, %v", entries, err)
	}
	path := filepath.Join(in.Layout.Backups(), entries[0].Name())
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the dump's mode is %v, %v", info.Mode().Perm(), err)
	}
	if dir, err := os.Stat(in.Layout.Backups()); err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("the backups directory's mode is %v, %v", dir.Mode().Perm(), err)
	}
	if got, _ := os.ReadFile(path); string(got) != "PGDMP dump" {
		t.Fatalf("the dump holds %q", got)
	}
	compose, err := os.ReadFile(in.Layout.Compose())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"--approve-migrations=12"`, `"--snapshot=` + entries[0].Name() + `"`} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("the state step is not given %s", want)
		}
	}
}

// A dump that fails stops the upgrade before the state step runs.
func TestAFailedDumpStopsTheDockerUpgradeBeforeTheStateStep(t *testing.T) {
	t.Parallel()
	_, exec, err := upDockerApproving(t, func(Cmd) ([]byte, error) { return nil, errors.New("pg_dump: connection refused") })
	if err == nil {
		t.Fatal("the upgrade went on without its dump")
	}
	for _, l := range exec.lines() {
		if strings.Contains(l, "state-bootstrap") {
			t.Fatalf("the state step ran without a dump: %s", l)
		}
	}
}

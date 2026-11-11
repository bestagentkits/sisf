package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runWithStdin runs one command line with stdin as its standard input.
func runWithStdin(t *testing.T, stdin *os.File, args ...string) (status int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	status = commands.run(context.Background(), &Env{Stdin: stdin, Stdout: &out, Stderr: &errOut}, args)
	return status, out.String(), errOut.String()
}

func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestInitNeverPromptsWithoutATerminalAndRefusesBeforeAsking(t *testing.T) {
	t.Parallel()
	// /dev/null is a character device, yet nobody can answer from it.
	dir := filepath.Join(t.TempDir(), "factory")
	status, stdout, stderr := runWithStdin(t, devNull(t), "init", "--dir", dir, "--profile", "sim", "--repo", "acme/hello-app")
	if status != exitOK || strings.Contains(stdout, `Type "unlimited"`) || !strings.Contains(stdout, "not confirmed") {
		t.Fatalf("init from /dev/null = %d\nstdout: %s\nstderr: %s", status, stdout, stderr)
	}
	// A refused invocation exits 2 with the reason in words, before any prompt.
	for name, args := range map[string][]string{
		"unknown mode":         {"--mode", "k8s", "--profile", "sim", "--repo", "acme/app"},
		"real without the app": {"--repo", "acme/app", "--github-token-env", "PATH"},
	} {
		dir := filepath.Join(t.TempDir(), "factory")
		status, stdout, stderr := runWithStdin(t, devNull(t), append([]string{"init", "--dir", dir}, args...)...)
		if status != exitUsage || stdout != "" || strings.Contains(stderr, "INVALID_ARGUMENT") || !strings.Contains(stderr, "Usage: sisf init") {
			t.Errorf("%s: status %d\nstdout: %q\nstderr: %q", name, status, stdout, stderr)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%s: a refused init created %s", name, dir)
		}
	}
}

func TestFlagMistakesAreSaidInTheCLIsOwnWords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"up", "--nope"}, "unknown flag --nope"},
		{[]string{"init", "--dir"}, "--dir needs a value"},
		{[]string{"init", "--github-installation-id", "seven"}, `--github-installation-id: "seven" is not a valid value`},
		{[]string{"runs", "explain"}, "missing an issue (owner/name#12) or a run id"},
		{[]string{"call"}, "missing the action name"},
		{[]string{"runs", "explian", "x"}, `"explian" is not a command of runs; did you mean explain?`},
	} {
		status, stdout, stderr := runWithOutput(t, tc.args...)
		if status != exitUsage || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v = %d %q, want %q", tc.args, status, stderr, tc.want)
		}
		// The flag package's own report never doubles the CLI's.
		if strings.Contains(stdout+stderr, "flag provided but not defined") || strings.Count(stderr, "Usage") > 1 {
			t.Errorf("%v printed the error twice:\n%s", tc.args, stderr)
		}
	}
}

func TestCallListsTheActionsItAcceptsWithoutAFactory(t *testing.T) {
	t.Parallel()
	status, stdout, stderr := runWithOutput(t, "call", "--list")
	if status != exitOK || !strings.Contains(stdout, "runs.explain") || !strings.Contains(stdout, "approvals.decide") {
		t.Fatalf("call --list = %d\nstdout: %s\nstderr: %s", status, stdout, stderr)
	}
	status, stdout, _ = runWithOutput(t, "call", "--list", "--json")
	var listed []listedAction
	if status != exitOK || json.Unmarshal([]byte(stdout), &listed) != nil || len(listed) == 0 {
		t.Fatalf("call --list --json = %d %q", status, stdout)
	}
	for _, a := range listed {
		if a.Name == "approvals.decide" && !a.StepUp {
			t.Error("approvals.decide is listed without its step-up")
		}
	}
}

func TestAStoppedInstallGoesDownAndLosesItsDataOnRequest(t *testing.T) {
	t.Parallel()
	dir := initSim(t, "standalone")
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := runWithOutput(t, "down", "--dir", dir)
	if status != exitOK || !strings.Contains(stdout, "was not running") {
		t.Fatalf("down on a stopped install = %d %q %q", status, stdout, stderr)
	}
	status, stdout, stderr = runWithOutput(t, "down", "-v", "--dir", dir)
	if status != exitOK || !strings.Contains(stdout, "its data is removed") {
		t.Fatalf("down -v on a stopped install = %d %q %q", status, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); err == nil {
		t.Error("down -v kept the data of a stopped install")
	}
}

func TestServiceUnitsAreWrittenAgainForAStandaloneInstallOnly(t *testing.T) {
	t.Parallel()
	dir := initSim(t, "standalone")
	if err := os.RemoveAll(filepath.Join(dir, "service")); err != nil {
		t.Fatal(err)
	}
	status, stdout, stderr := runWithOutput(t, "service", "units", "--dir", dir)
	if status != exitOK || !strings.Contains(stdout, "service units written") {
		t.Fatalf("service units = %d %q %q", status, stdout, stderr)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "service")); len(entries) == 0 {
		t.Fatal("no unit was written")
	}
	docker := initSim(t, "docker")
	if status, _, stderr := runWithOutput(t, "service", "units", "--dir", docker); status != exitUsage || !strings.Contains(stderr, "docker mode") {
		t.Fatalf("service units on a Docker install = %d %q", status, stderr)
	}
}

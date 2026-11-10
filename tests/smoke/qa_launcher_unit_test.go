//go:build smoke

package smoke

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The QA launcher runs codex in a held-down container that sees the
// workspace and the codex login read-only and nothing of the install; a
// stand-in docker CLI records the command line it would run.
func TestTheQALauncherRunsCodexInAHeldDownContainer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	record := filepath.Join(dir, "docker-args")
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '"+record+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	workspace, auth := filepath.Join(dir, "qa", "workspaces", "run-1"), filepath.Join(dir, "auth.json")
	launcher := filepath.Join(repoRoot(), "tests", "smoke", "sandbox", "qa-codex-launcher.sh")
	run := func(args ...string) ([]string, error) {
		cmd := exec.Command(launcher, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "SISF_SMOKE_DOCKER=" + docker, "SISF_SMOKE_QA_IMAGE=sisf-smoke-x-qa-codex:run",
			"SISF_SMOKE_QA_LABEL=sisf-smoke-x", "SISF_SMOKE_CODEX_AUTH=" + auth, "OPENAI_API_KEY=sk-never-on-a-command-line"}
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, &commandError{line: launcher, err: err, output: string(out)}
		}
		raw, err := os.ReadFile(record)
		return strings.Split(strings.TrimRight(string(raw), "\n"), "\n"), err
	}
	if err := os.WriteFile(auth, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, err := run("exec", "--json", "-C", workspace, "-s", "read-only", "-")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	for _, want := range []string{"--cap-drop ALL", "--security-opt no-new-privileges", "--read-only", "--user 1000:1000",
		"-v " + workspace + ":" + workspace + ":ro", "-v " + auth + ":/run/sisf/codex-auth.json:ro", "--label sisf.smoke=sisf-smoke-x",
		"-e OPENAI_API_KEY", "sisf-smoke-x-qa-codex:run", "codex exec --json -C " + workspace + " -s read-only -"} {
		if !strings.Contains(line, want) {
			t.Errorf("the container lacks %q: %s", want, line)
		}
	}
	if strings.Contains(line, "sk-never-on-a-command-line") || slices.Contains(args, "--privileged") {
		t.Errorf("the container command line carries a secret or privilege: %s", line)
	}
	if mounts := strings.Count(line, " -v "); mounts != 2 {
		t.Errorf("the container mounts %d paths, want the workspace and the login only: %s", mounts, line)
	}
	if _, err := run("exec", "-C", "relative/ws", "-"); err == nil {
		t.Error("a relative workspace was mounted")
	}
	if _, err := run("exec", "-C", "/tmp/ws with space", "-"); err == nil {
		t.Error("a workspace with a space was mounted")
	}
}

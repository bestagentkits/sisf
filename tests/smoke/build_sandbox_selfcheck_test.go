//go:build smoke

package smoke

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// sandboxCheckEnv runs TestBuildSandboxSelfCheck, which needs Docker but
// no GitHub, no model and no factory: it spends nothing.
const sandboxCheckEnv = "SISF_SMOKE_SANDBOX_CHECK"

// TestBuildSandboxSelfCheck provisions the run's build sandbox against a
// local repository and builds its head through the real runner filter and
// docker backend, then starts codex through the QA launcher. It is the
// part of the real run that can be proven without spending; the real run
// repeats the build check against the sandbox's head before it opens the
// issue.
func TestBuildSandboxSelfCheck(t *testing.T) {
	if os.Getenv(sandboxCheckEnv) != "1" {
		t.Skipf("set %s=1 to provision a build sandbox with Docker", sandboxCheckEnv)
	}
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &commandRunner{t: t, dir: repoRoot(), scrub: newSecretScrubber(os.Environ()), report: &smokeReport{}}
	origin, head := fixtureRepository(ctx, r, filepath.Join(root, "origin"))
	sisf := filepath.Join(root, "sisf")
	r.buildBinary(ctx, sisf, "./cmd/sisf")

	sb := newBuildSandbox(r, root)
	sb.provision(ctx, r, origin, codexVersion(ctx, r))
	const factory = core.FactoryID("sisf-sandbox-check")
	sb.startFilter(ctx, r, sisf, factory, filepath.Join(root, "docker-filter.log"))
	sb.checkIsolation(ctx, r)
	result := sb.selfBuild(ctx, r, factory, head)
	if !result.TestsPassed || !strings.HasPrefix(result.Image, "smoke.local/") {
		t.Fatalf("the sandboxed build = %+v", result)
	}
	if out, err := r.run(ctx, 2*time.Minute, []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(root, "qa-home")}, sb.launcher, "--version"); err != nil ||
		!strings.Contains(out, "codex") {
		t.Fatalf("codex through the QA launcher: %q %v", out, err)
	}
	for _, c := range r.report.Checks {
		t.Logf("%s: %s (%s)", c.Name, c.Status, c.Evidence)
	}
}

// fixtureRepository makes a Go module with a test at dir and returns its
// path and head commit.
func fixtureRepository(ctx context.Context, r *commandRunner, dir string) (string, core.CommitSHA) {
	r.t.Helper()
	files := map[string]string{
		"go.mod":       "module example.com/hello\n\ngo 1.25\n",
		"main.go":      "package main\n\nimport \"fmt\"\n\nfunc greeting() string { return \"hello\" }\n\nfunc main() { fmt.Println(greeting()) }\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestGreeting(t *testing.T) {\n\tif greeting() != \"hello\" {\n\t\tt.Fatal(greeting())\n\t}\n}\n",
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.fatalf("%v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			r.fatalf("%v", err)
		}
	}
	git := func(args ...string) string {
		out, err := r.run(ctx, time.Minute, nil, "git", append([]string{"-C", dir, "-c", "user.name=smoke", "-c", "user.email=smoke@example.test"}, args...)...)
		if err != nil {
			r.fatalf("fixture: %v", err)
		}
		return strings.TrimSpace(out)
	}
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "fixture")
	return dir, core.CommitSHA(git("rev-parse", "HEAD"))
}

// codexVersion is the version of the operator's codex CLI, which the QA
// image installs.
func codexVersion(ctx context.Context, r *commandRunner) string {
	r.t.Helper()
	out, err := r.run(ctx, time.Minute, nil, "codex", "--version")
	if err != nil {
		r.fatalf("codex --version: %v", err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		r.fatalf("codex --version printed nothing")
	}
	return fields[len(fields)-1]
}

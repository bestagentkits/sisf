//go:build smoke

package smoke

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// The smoke run's build script reports everything the supervisor's
// observed-build hard gate needs: its tests, its image and digest, and
// where the SBOM and provenance of the build are. A build without the last
// two fails that gate on every attempt, whatever the judge scores. The
// script runs here on the host against a local mirror, without Docker.
func TestTheBuildScriptReportsTheSBOMAndProvenanceTheObservedBuildGateNeeds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	commit := sandboxCommit(t, filepath.Join(dir, "repo"), filepath.Join(dir, "mirror.git"))
	script := filepath.Join(repoRoot(), "tests", "smoke", "sandbox", "build-candidate.sh")
	images := stages.SandboxImages{Exec: scriptBuild{t: t, script: script, env: []string{
		"SISF_SMOKE_MIRROR=" + filepath.Join(dir, "mirror.git"), "GOFLAGS=-mod=readonly"}, work: filepath.Join(dir, "work")},
		Argv: []string{"build"}, Timeout: 5 * time.Minute}
	built, err := images.BuildImage(context.Background(), stages.ImageRequest{Commit: core.CommitSHA(commit), Unit: "app", RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !built.TestsPassed || built.SBOMRef == "" || built.ProvenanceRef == "" {
		t.Fatalf("the build reported tests passed %t, SBOM %q and provenance %q", built.TestsPassed, built.SBOMRef, built.ProvenanceRef)
	}
	for _, ref := range []string{built.SBOMRef, built.ProvenanceRef} {
		if !strings.Contains(ref, ":sha256:") {
			t.Errorf("the reference %q names no digest of a document the build wrote", ref)
		}
	}
}

// sandboxCommit commits a small Go program with a test to a repository and
// mirrors it to a bare repository, as the smoke run's mirror holds the
// sandbox. It returns the commit.
func sandboxCommit(t *testing.T, repo, mirror string) string {
	t.Helper()
	files := map[string]string{
		"go.mod":       "module example.com/sandbox\n\ngo 1.22\n",
		"main.go":      "package main\n\nfunc main() { println(greeting()) }\n\nfunc greeting() string { return \"hello\" }\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestGreeting(t *testing.T) {\n\tif greeting() != \"hello\" {\n\t\tt.Fatal(greeting())\n\t}\n}\n",
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q")
	git(repo, "add", ".")
	git(repo, "commit", "-q", "-m", "sandbox")
	git(repo, "clone", "-q", "--bare", repo, mirror)
	git(mirror, "config", "uploadpack.allowAnySHA1InWant", "true")
	return git(repo, "rev-parse", "HEAD")
}

// scriptBuild is a sandbox that runs the build script on the host, each
// phase in a work directory of its own as each runs in a fresh container.
type scriptBuild struct {
	execution.Backend
	t      *testing.T
	script string
	env    []string
	work   string
}

func (scriptBuild) Name() string { return "docker" }

func (scriptBuild) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{core.CapExecIsolation: core.SupportSupported, core.CapExecEgressEnforcement: core.SupportSupported}, nil
}

func (scriptBuild) CreateSandbox(context.Context, execution.SandboxSpec) (execution.SandboxInfo, error) {
	return execution.SandboxInfo{ID: "build-1", State: execution.SandboxRunning}, nil
}

func (b scriptBuild) Exec(ctx context.Context, _ execution.SandboxID, c execution.Command) (execution.ExecResult, error) {
	cmd := exec.CommandContext(ctx, b.script)
	cmd.Env = append(os.Environ(), b.env...)
	cmd.Env = append(cmd.Env, "SISF_SMOKE_WORKDIR="+filepath.Join(b.work, c.Env["SISF_PHASE"]))
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	b.t.Logf("phase %s: %v\n%s", c.Env["SISF_PHASE"], err, stderr.Bytes())
	res := execution.ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		res.ExitCode = exit.ExitCode()
		return res, nil
	}
	return res, err
}

func (scriptBuild) Destroy(context.Context, execution.SandboxID) error { return nil }

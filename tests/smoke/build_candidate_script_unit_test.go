//go:build smoke

package smoke

import (
	"context"
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
	cmd := exec.Command(script)
	cmd.Env = append(os.Environ(), "SISF_COMMIT="+commit, "SISF_SMOKE_MIRROR="+filepath.Join(dir, "mirror.git"),
		"SISF_SMOKE_WORKDIR="+filepath.Join(dir, "work"), "GOFLAGS=-mod=readonly", "CGO_ENABLED=0")
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("the build script failed: %v\n%s", err, stdout)
	}

	// The supervisor reads the lines as it reads a sandboxed build's.
	images := stages.SandboxImages{Exec: printedBuild{stdout: stdout}, Argv: []string{"build"}, Timeout: time.Minute}
	built, err := images.BuildImage(context.Background(), stages.ImageRequest{Commit: core.CommitSHA(commit), Unit: "app", RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !built.TestsPassed || built.SBOMRef == "" || built.ProvenanceRef == "" {
		t.Fatalf("the build reported tests passed %t, SBOM %q and provenance %q:\n%s", built.TestsPassed, built.SBOMRef, built.ProvenanceRef, stdout)
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

// printedBuild is a sandbox whose build command printed stdout.
type printedBuild struct {
	execution.Backend
	stdout []byte
}

func (printedBuild) Name() string { return "docker" }

func (printedBuild) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{core.CapExecIsolation: core.SupportSupported, core.CapExecEgressEnforcement: core.SupportSupported}, nil
}

func (printedBuild) CreateSandbox(context.Context, execution.SandboxSpec) (execution.SandboxInfo, error) {
	return execution.SandboxInfo{ID: "build-1", State: execution.SandboxRunning}, nil
}

func (p printedBuild) Exec(context.Context, execution.SandboxID, execution.Command) (execution.ExecResult, error) {
	return execution.ExecResult{Stdout: p.stdout}, nil
}

func (printedBuild) Destroy(context.Context, execution.SandboxID) error { return nil }

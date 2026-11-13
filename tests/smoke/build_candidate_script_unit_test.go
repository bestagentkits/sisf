//go:build smoke

package smoke

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/supervisor/programstore"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// The smoke run's build script reports what the supervisor's observed-build
// hard gate needs from it: its tests, its image and digest, and a program
// whose Go build information the supervisor reads to make the build's SBOM
// and provenance itself. The script runs here on the host against a local
// mirror, without Docker.
func TestTheBuildScriptExportsAProgramTheSupervisorCanDescribe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	commit := sandboxCommit(t, filepath.Join(dir, "repo"), filepath.Join(dir, "mirror.git"))
	script := filepath.Join(repoRoot(), "tests", "smoke", "sandbox", "build-candidate.sh")
	images := stages.SandboxImages{Exec: &scriptBuild{t: t, script: script, env: []string{
		"SISF_SMOKE_MIRROR=" + filepath.Join(dir, "mirror.git"), "GOFLAGS=-mod=readonly"}, work: filepath.Join(dir, "work")},
		Argv: []string{"build"}, Timeout: 5 * time.Minute, Programs: scriptStore(t, dir)}
	built, err := images.BuildImage(context.Background(), stages.ImageRequest{Commit: core.CommitSHA(commit), Unit: "app", RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !built.TestsPassed || built.Program == nil || built.Program.Main.Path == "" {
		t.Fatalf("the build reported tests passed %t and a program with build information %+v (%q)",
			built.TestsPassed, built.Program, built.ProgramUnread)
	}
}

// On a Standalone factory the script exports the unit's one program, built
// for the operating system and architecture the supervisor names, and the
// image digest it reports is the sha256 of that program: the supervisor
// keeps exactly those bytes, and they run on the supervisor's host.
func TestTheBuildScriptExportsTheProgramItsDigestNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	commit := sandboxCommit(t, filepath.Join(dir, "repo"), filepath.Join(dir, "mirror.git"))
	store := scriptStore(t, dir)
	build := &scriptBuild{t: t, script: filepath.Join(repoRoot(), "tests", "smoke", "sandbox", "build-candidate.sh"), env: []string{
		"SISF_SMOKE_MIRROR=" + filepath.Join(dir, "mirror.git"), "GOFLAGS=-mod=readonly"}, work: filepath.Join(dir, "work")}
	images := stages.SandboxImages{Exec: build, Argv: []string{"build"}, Timeout: 5 * time.Minute, Programs: store}
	built, err := images.BuildImage(context.Background(), stages.ImageRequest{Commit: core.CommitSHA(commit), Unit: "app", RunID: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !built.TestsPassed || built.Image != "smoke.local/sisf-sandbox@"+string(built.Digest) {
		t.Fatalf("the build reported tests passed %t and image %q for %s", built.TestsPassed, built.Image, built.Digest)
	}
	program, err := store.Path(built.Digest)
	if err != nil {
		t.Fatalf("the store does not hold the program the digest names: %v", err)
	}
	raw, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(raw); "sha256:"+hex.EncodeToString(sum[:]) != string(built.Digest) {
		t.Errorf("the image digest %s is not the sha256 of the exported program", built.Digest)
	}
	// Built for this host, it runs here.
	out, err := exec.Command(program).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("the exported program does not run on this host: %q, %v", out, err)
	}
	if got := build.reportEnv["SISF_REPORT_TARGET_OS"] + "/" + build.reportEnv["SISF_REPORT_TARGET_ARCH"]; got != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("the report phase was told the target %s", got)
	}
}

// A candidate with more than one program is refused where its code is
// built: a unit is one program, and the script will not pick one.
func TestTheBuildScriptRefusesACandidateWithTwoPrograms(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "cmd", "other", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit := sandboxCommit(t, repo, filepath.Join(dir, "mirror.git"))
	build := &scriptBuild{t: t, script: filepath.Join(repoRoot(), "tests", "smoke", "sandbox", "build-candidate.sh"), env: []string{
		"SISF_SMOKE_MIRROR=" + filepath.Join(dir, "mirror.git"), "GOFLAGS=-mod=readonly"}, work: filepath.Join(dir, "work")}
	images := stages.SandboxImages{Exec: build, Argv: []string{"build"}, Timeout: 5 * time.Minute}
	_, err := images.BuildImage(context.Background(), stages.ImageRequest{Commit: core.CommitSHA(commit), Unit: "app", RunID: "run-1"})
	if r, refused := stages.RefusalOf(err); !refused || r.Reason != stages.RefusalBuildFailed {
		t.Fatalf("a candidate with two programs: %v, want its build refused", err)
	}
}

// scriptStore is a program store on a new trust volume below dir.
func scriptStore(t *testing.T, dir string) *programstore.Store {
	t.Helper()
	trust := filepath.Join(dir, "trust")
	if err := os.Mkdir(trust, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := programstore.Open(trust)
	if err != nil {
		t.Fatal(err)
	}
	return store
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
	// reportEnv is what the report phase was given.
	reportEnv map[string]string
}

func (*scriptBuild) Name() string { return "docker" }

func (*scriptBuild) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{core.CapExecIsolation: core.SupportSupported, core.CapExecEgressEnforcement: core.SupportSupported}, nil
}

func (*scriptBuild) CreateSandbox(context.Context, execution.SandboxSpec) (execution.SandboxInfo, error) {
	return execution.SandboxInfo{ID: "build-1", State: execution.SandboxRunning}, nil
}

func (b *scriptBuild) Exec(ctx context.Context, _ execution.SandboxID, c execution.Command) (execution.ExecResult, error) {
	cmd := exec.CommandContext(ctx, b.script)
	cmd.Env = append(os.Environ(), b.env...)
	cmd.Env = append(cmd.Env, "SISF_SMOKE_WORKDIR="+filepath.Join(b.work, c.Env["SISF_PHASE"]))
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if c.Env["SISF_PHASE"] == "report" {
		b.reportEnv = c.Env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if c.Stdout != nil {
		cmd.Stdout = c.Stdout // streamed, not captured
	}
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

func (*scriptBuild) Destroy(context.Context, execution.SandboxID) error { return nil }

// A report phase whose build wrote no program fails, saying so, instead of
// reporting the digest of nothing.
func TestTheBuildScriptFailsWhenTheBuildProducedNoProgram(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	commit := sandboxCommit(t, filepath.Join(dir, "repo"), filepath.Join(dir, "mirror.git"))
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	// A go that does everything but write the program.
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nfor a in \"$@\"; do case $a in */out/program) exit 0 ;; esac; done\nexec '" + realGo + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(repoRoot(), "tests", "smoke", "sandbox", "build-candidate.sh"))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SISF_PHASE=report", "SISF_REPORT_COMMIT="+commit, "SISF_REPORT_NONCE=n0nce",
		"SISF_REPORT_TARGET_OS="+runtime.GOOS, "SISF_REPORT_TARGET_ARCH="+runtime.GOARCH,
		"SISF_SMOKE_MIRROR="+filepath.Join(dir, "mirror.git"), "SISF_SMOKE_WORKDIR="+filepath.Join(dir, "work"), "GOFLAGS=-mod=readonly")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if err == nil || strings.Contains(stdout.String(), "image-digest") || !strings.Contains(stderr.String(), "no program") {
		t.Fatalf("a build that wrote no program: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
}

//go:build smoke

package smoke

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	execdocker "github.com/bestagentkits/sisf/adapters/exec/docker"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

// mirrorSyncInterval is how often the mirror follows GitHub; the build
// script waits up to five minutes for a commit to appear.
const mirrorSyncInterval = 5 * time.Second

func lookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// startMirror makes the bare repository the git mirror serves and keeps it
// in step with origin until the run ends. A GitHub origin is fetched with
// the operator's gh login as git's credential helper: no token is written
// anywhere or put on a command line, and none reaches the sandbox.
func (s *buildSandbox) startMirror(ctx context.Context, r *commandRunner, origin string) {
	r.t.Helper()
	repo := filepath.Join(s.mirror, "sandbox.git")
	for _, args := range [][]string{{"init", "-q", "--bare", repo}, {"-C", repo, "config", "uploadpack.allowAnySHA1InWant", "true"}} {
		if _, err := r.run(ctx, time.Minute, nil, "git", args...); err != nil {
			r.fatalf("build sandbox: the mirror: %v", err)
		}
	}
	// The mirror container's user must read it; it holds only the sandbox's code.
	if err := os.Chmod(s.mirror, 0o755); err != nil {
		r.fatalf("build sandbox: the mirror: %v", err)
	}
	sync := func(ctx context.Context) error {
		_, err := r.run(ctx, 2*time.Minute, nil, "git", "-C", repo, "-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential",
			"fetch", "-q", "--prune", origin, "+refs/heads/*:refs/heads/*")
		return err
	}
	if err := sync(ctx); err != nil {
		r.fatalf("build sandbox: the mirror cannot fetch the sandbox: %v", err)
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(mirrorSyncInterval):
				_ = sync(ctx) // a missed round is caught up by the next; the build waits for its commit
			}
		}
	}()
	r.t.Cleanup(func() { cancel(); <-done })
}

// writeLauncher writes the QA agent's launcher: a wrapper that fixes the
// run's settings and runs sandbox/qa-codex-launcher.sh. The codex login is
// the operator's file, mounted read-only into the container; it is never
// read here.
func (s *buildSandbox) writeLauncher(r *commandRunner) {
	r.t.Helper()
	script := filepath.Join(repoRoot(), "tests", "smoke", "sandbox", "qa-codex-launcher.sh")
	auth := filepath.Join(os.Getenv("HOME"), ".codex", "auth.json")
	for _, v := range []string{s.docker, s.qaImage, s.name, auth, s.dockerHost, script} {
		if strings.ContainsAny(v, "'\n") {
			r.fatalf("build sandbox: %q cannot be written into the launcher", v)
		}
	}
	body := "#!/bin/sh\n# The QA agent's launcher of this smoke run; see " + script + ".\n" +
		"export SISF_SMOKE_DOCKER='" + s.docker + "' SISF_SMOKE_QA_IMAGE='" + s.qaImage + "' SISF_SMOKE_QA_LABEL='" + s.name + "'\n" +
		"export SISF_SMOKE_CODEX_AUTH='" + auth + "' DOCKER_HOST='" + s.dockerHost + "'\n" +
		"exec '" + script + "' \"$@\"\n"
	if err := os.MkdirAll(filepath.Dir(s.launcher), 0o700); err != nil {
		r.fatalf("build sandbox: %v", err)
	}
	if err := os.WriteFile(s.launcher, []byte(body), 0o700); err != nil {
		r.fatalf("build sandbox: the launcher: %v", err)
	}
}

// startFilter runs the runner Docker request filter of the factory on the
// endpoint until the run ends. It needs the factory's ID, so it starts
// after `sisf init`.
func (s *buildSandbox) startFilter(ctx context.Context, r *commandRunner, sisf string, factory core.FactoryID, log string) {
	r.t.Helper()
	f, err := os.Create(log)
	if err != nil {
		r.fatalf("build sandbox: %v", err)
	}
	args := []string{"docker-filter", "--role", "runner", "--listen", strings.TrimPrefix(s.endpoint, "http://"),
		"--factory-id", string(factory), "--network", s.network, "--image", s.runnerImage, "--socket", s.socket}
	r.report.Commands = addUnique(r.report.Commands, sisf+" "+strings.Join(args, " "))
	cmd := exec.Command(sisf, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = r.dir, minimalEnv(), f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		r.fatalf("build sandbox: start the runner filter: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); _ = f.Close(); close(exited) }()
	r.t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	})
	s.waitForHTTP(ctx, r, s.endpoint+"/_ping")
}

// waitForHTTP waits until url answers at all.
func (s *buildSandbox) waitForHTTP(ctx context.Context, r *commandRunner, url string) {
	r.t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			r.fatalf("build sandbox: %v", err)
		}
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
			return
		}
	}
	r.fatalf("build sandbox: %s does not answer", url)
}

// checkIsolation proves the claim the docker backend makes of the sandbox
// network: a runner reaches nothing directly, only through the proxy.
func (s *buildSandbox) checkIsolation(ctx context.Context, r *commandRunner) {
	r.t.Helper()
	_, err := r.run(ctx, time.Minute, nil, s.docker, "run", "--rm", "--name", s.name+"-isolation", "--network", s.network,
		"--read-only", "--cap-drop", "ALL", s.runnerImage, "wget", "-q", "-T", "5", "-O", "/dev/null", "http://1.1.1.1/")
	if err == nil {
		r.fatalf("build sandbox: a runner on %s reached the internet directly; the network is not isolated", s.network)
	}
	r.report.check("sandbox network has no direct way out", statusPass, "a direct connection from "+s.network+" failed")
}

// selfBuild builds commit through the real filter and docker backend, as
// the supervisor will, before anything spends: a sandbox that cannot build
// the sandbox's head fails the run here.
func (s *buildSandbox) selfBuild(ctx context.Context, r *commandRunner, factory core.FactoryID, commit core.CommitSHA) stages.ImageResult {
	r.t.Helper()
	backend, err := execdocker.New(execdocker.Config{Endpoint: s.endpoint, FactoryID: factory, Network: s.network, EgressProxy: egressProxyURL})
	if err != nil {
		r.fatalf("build sandbox: %v", err)
	}
	images, err := stages.SelectImageBuilder(ctx, stages.ExecutionChoice{
		Profile: stages.ProfileProduction, Agent: "claude", Docker: backend,
		Specs:     stages.SandboxSpecs{Image: s.runnerImage, Egress: core.EgressPolicy{Mode: core.EgressAllowlist}, Limits: execution.Limits{Timeout: 15 * time.Minute}},
		BuildArgv: []string{sandboxBuildCommand}, BuildTimeout: 15 * time.Minute,
	})
	if err != nil {
		r.fatalf("build sandbox: the build backend selection refused it: %v", err)
	}
	result, err := images.BuildImage(ctx, stages.ImageRequest{FactoryID: factory, RunID: "run-selfcheck", Unit: "app", Commit: commit, Untrusted: true})
	if err != nil {
		r.fatalf("build sandbox: the sandboxed build of %s failed: %v", commit, err)
	}
	r.report.check("sandboxed build of the sandbox's head", statusPass, string(result.Digest)+", tests "+map[bool]string{true: "passed", false: "failed"}[result.TestsPassed])
	return result
}

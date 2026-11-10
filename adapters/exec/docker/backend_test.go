package docker_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/adapters/exec/docker"
	"github.com/bestagentkits/sisf/internal/dockerfilter/dockerfiltertest"
	"github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
)

func TestACommandRunsInAHardenedRunnerUnderGVisor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, options{})
	e.daemon.OnStart(func(dockerfiltertest.Container) dockerfiltertest.Outcome {
		return dockerfiltertest.Outcome{ExitCode: 3, Stdout: "built\n", Stderr: "one test failed\n"}
	})
	caps, err := e.backend.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !execution.UntrustedCodeAllowed(caps) || !caps.Supports(docker.CapGVisor) || caps.Supports(core.CapExecSnapshot) {
		t.Fatalf("unexpected capabilities %v", caps)
	}
	sb := e.create(t, spec("run-0001", execution.TrustUntrusted))
	res, err := e.backend.Exec(ctx, sb.ID, execution.Command{Argv: []string{"go", "test", "./..."}, Dir: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || string(res.Stdout) != "built\n" || string(res.Stderr) != "one test failed\n" || res.TimedOut {
		t.Fatalf("unexpected result %+v", res)
	}
	bodies := e.createdBodies(t)
	if len(bodies) != 1 {
		t.Fatalf("%d containers were created", len(bodies))
	}
	b := bodies[0]
	h := b.HostConfig
	if h.Runtime != "runsc" || !h.ReadonlyRootfs || h.Privileged || h.NetworkMode != network || h.Memory != 1<<30 ||
		h.NanoCpus != 1_000_000_000 || h.PidsLimit != 256 || !slices.Contains(h.CapDrop, "ALL") || b.User != docker.DefaultUser ||
		b.WorkingDir != "/workspace/app" || b.Labels["sisf.run_id"] != "run-0001" {
		t.Fatalf("the runner was not hardened as required: %+v", b)
	}
	if !slices.Contains(b.Env, "HTTPS_PROXY="+proxy) || !slices.Contains(b.Env, "SISF_RUN=run-0001") {
		t.Fatalf("the runner's environment is wrong: %v", b.Env)
	}
	if len(e.daemon.Containers()) != 0 {
		t.Fatal("the command's container was not removed")
	}
}

func TestWithoutGVisorRuncIsUsedAndRequiringGVisorRefusesUntrustedCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, options{runtimes: []string{"runc"}})
	caps, err := e.backend.Capabilities(ctx)
	if err != nil || caps.Supports(docker.CapGVisor) || !caps.Supports(core.CapExecIsolation) {
		t.Fatalf("runc host: %v %v", caps, err)
	}
	sb := e.create(t, spec("run-0001", execution.TrustUntrusted))
	if _, err := e.backend.Exec(ctx, sb.ID, execution.Command{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if rt := e.createdBodies(t)[0].HostConfig.Runtime; rt != "" {
		t.Fatalf("runtime %q was asked for on a host without gVisor", rt)
	}

	strict := newEnv(t, options{runtimes: []string{"runc"}, cfg: func(c *docker.Config) { c.RequireGVisor = true }})
	_, err = strict.backend.CreateSandbox(ctx, spec("run-0002", execution.TrustUntrusted))
	wantCode(t, err, core.CodeNotSupported, "untrusted code where gVisor is required but missing")
	strict.create(t, spec("run-0003", execution.TrustTrusted))
}

func TestUntrustedCodeNeedsTheEgressProxy(t *testing.T) {
	t.Parallel()
	e := newEnv(t, options{cfg: func(c *docker.Config) { c.EgressProxy = "" }})
	caps, err := e.backend.Capabilities(context.Background())
	if err != nil || caps.Supports(core.CapExecEgressEnforcement) {
		t.Fatalf("egress enforcement declared without a proxy: %v %v", caps, err)
	}
	_, err = e.backend.CreateSandbox(context.Background(), spec("run-0001", execution.TrustUntrusted))
	wantCode(t, err, core.CodeNotSupported, "untrusted code without the egress proxy")
}

func TestTheTimeLimitStopsACommandAndEndsTheSandbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, options{})
	e.daemon.OnStart(func(dockerfiltertest.Container) dockerfiltertest.Outcome { return dockerfiltertest.Outcome{Hang: true} })
	sb := e.create(t, spec("run-0001", execution.TrustUntrusted))
	type outcome struct {
		res execution.ExecResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := e.backend.Exec(ctx, sb.ID, execution.Command{Argv: []string{"sleep", "infinity"}, Timeout: time.Minute})
		done <- outcome{res, err}
	}()
	if d := <-e.clock.timers; d != time.Minute {
		t.Fatalf("the command waits %s, want its own timeout", d)
	}
	e.clock.Advance(time.Minute)
	got := <-done
	if got.err != nil || !got.res.TimedOut || got.res.ExitCode != 137 || got.res.Duration != time.Minute {
		t.Fatalf("a command past its time limit: %+v %v", got.res, got.err)
	}
	if len(e.daemon.Containers()) != 0 {
		t.Fatal("the stopped container was not removed")
	}
	e.clock.Advance(30 * time.Minute)
	_, err := e.backend.Exec(ctx, sb.ID, execution.Command{Argv: []string{"true"}})
	wantCode(t, err, core.CodeLimitExceeded, "a command after the sandbox's time")
}

func TestSpecsAskingForTheHostAreRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t, options{})
	for name, mutate := range map[string]func(*execution.SandboxSpec){
		"host mount":         func(s *execution.SandboxSpec) { s.Mounts = []execution.Mount{{Source: "/", Target: "/host"}} },
		"image by tag":       func(s *execution.SandboxSpec) { s.Image = "ghcr.io/acme/sisf-runner:latest" },
		"another unit":       func(s *execution.SandboxSpec) { s.Unit = "control" },
		"proxy override":     func(s *execution.SandboxSpec) { s.Env["HTTPS_PROXY"] = "http://attacker.example:8080" },
		"routing label":      func(s *execution.SandboxSpec) { s.Labels = map[string]string{"traefik.enable": "true"} },
		"runner label":       func(s *execution.SandboxSpec) { s.Labels = map[string]string{"sisf.runner": "other"} },
		"grants without one": func(s *execution.SandboxSpec) { s.SecretGrantIDs = []string{"grant-1"} },
	} {
		s := spec("run-0001", execution.TrustUntrusted)
		mutate(&s)
		if _, err := e.backend.CreateSandbox(context.Background(), s); err == nil {
			t.Errorf("%s: the sandbox was created", name)
		}
	}
	sb := e.create(t, spec("run-0001", execution.TrustUntrusted))
	for name, cmd := range map[string]execution.Command{
		"escaping dir": {Argv: []string{"true"}, Dir: "../../etc"},
		"proxy in env": {Argv: []string{"true"}, Env: map[string]string{"no_proxy": "*"}},
		"standard in":  {Argv: []string{"cat"}, Stdin: strings.NewReader("x")},
		"no arguments": {},
		"unclean abs":  {Argv: []string{"true"}, Dir: "/workspace/../etc"},
	} {
		if _, err := e.backend.Exec(context.Background(), sb.ID, cmd); err == nil {
			t.Errorf("%s: the command ran", name)
		}
	}
	if n := len(e.daemon.Created()); n != 0 {
		t.Fatalf("%d containers were created for refused requests", n)
	}
}

// A daemon that reports a container without the hardening asked for (as a
// filter bypass or a daemon ignoring a field would) never gets it started.
func TestAContainerWithoutTheHardeningIsNeverStarted(t *testing.T) {
	t.Parallel()
	for name, rewrite := range map[string]func(map[string]any){
		"writable root": func(v map[string]any) { v["HostConfig"].(map[string]any)["ReadonlyRootfs"] = false },
		"privileged":    func(v map[string]any) { v["HostConfig"].(map[string]any)["Privileged"] = true },
		"bind mount":    func(v map[string]any) { v["HostConfig"].(map[string]any)["Binds"] = []string{"/:/host"} },
		"host network":  func(v map[string]any) { v["HostConfig"].(map[string]any)["NetworkMode"] = "host" },
		"no pids limit": func(v map[string]any) { delete(v["HostConfig"].(map[string]any), "PidsLimit") },
		"other runtime": func(v map[string]any) { v["HostConfig"].(map[string]any)["Runtime"] = "runc" },
	} {
		e := newEnv(t, options{rewrite: rewrite})
		sb := e.create(t, spec("run-0001", execution.TrustUntrusted))
		_, err := e.backend.Exec(context.Background(), sb.ID, execution.Command{Argv: []string{"true"}})
		wantCode(t, err, core.CodeInternal, name)
		for _, r := range e.daemon.Requests() {
			if strings.HasSuffix(r.Path, "/start") {
				t.Fatalf("%s: the container was started", name)
			}
		}
		if len(e.daemon.Containers()) != 0 {
			t.Fatalf("%s: the unhardened container was left behind", name)
		}
	}
}

func TestTheBackendPassesTheExecutionConformanceKit(t *testing.T) {
	t.Parallel()
	e := newEnv(t, options{})
	row, err := conformance.GradeExecution(context.Background(), e.backend, conformance.ExecutionProbe{
		Spec: spec("run-0001", execution.TrustUntrusted), Command: execution.Command{Argv: []string{"true"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.Verdict != conformance.VerdictSupported {
		t.Fatalf("graded %s: %v", row.Verdict, row.Notes)
	}
	_, err = e.backend.Files(context.Background(), "x")
	wantCode(t, err, core.CodeNotSupported, "files")
	_, err = e.backend.Snapshot(context.Background(), "x")
	wantCode(t, err, core.CodeNotSupported, "snapshot")
}

func TestAFilterThatRefusesIsReportedAsPermissionDenied(t *testing.T) {
	t.Parallel()
	e := newEnv(t, options{cfg: func(c *docker.Config) { c.FactoryID = "another-factory" }})
	sb := e.create(t, spec("run-0001", execution.TrustUntrusted))
	_, err := e.backend.Exec(context.Background(), sb.ID, execution.Command{Argv: []string{"true"}})
	wantCode(t, err, core.CodePermissionDenied, "a runner labelled for another factory")
}

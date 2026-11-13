//go:build unix

package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/dockerfilter"
	"github.com/bestagentkits/sisf/internal/dockerfilter/dockerfiltertest"
	sisfinstall "github.com/bestagentkits/sisf/internal/install"
	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent/clitest"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// sandboxRoute is a Standalone install with real agents and a Docker build
// sandbox, and the runner filter of that sandbox in front of a fake daemon.
type sandboxRoute struct {
	manifest *manifest.Manifest
	daemon   *dockerfiltertest.Daemon
	args     []string
}

const (
	routeFactory = "sisf-sisf-sandbox"
	routeNetwork = "sisf-smoke-sandbox"
)

var routeImage = "127.0.0.1:17481/sisf-smoke-runner@sha256:" + strings.Repeat("e", 64)

func newSandboxRoute(t *testing.T) sandboxRoute {
	t.Helper()
	daemon := dockerfiltertest.New("runc")
	daemon.AddNetwork(routeNetwork, true)
	upstream := httptest.NewServer(daemon)
	t.Cleanup(upstream.Close)
	filter, err := dockerfilter.New(dockerfilter.Config{Role: dockerfilter.RoleRunner, Upstream: upstream.Client().Transport,
		UpstreamURL: upstream.URL, RunnerValue: routeFactory, SandboxNetwork: routeNetwork, Images: []string{routeImage},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(filter)
	t.Cleanup(front.Close)

	launcher := filepath.Join(clitest.Install(t, "codex", clitest.Script{}), "codex")
	m := &manifest.Manifest{
		FactoryID: routeFactory, Mode: manifest.ModeStandalone, TrustedCodeOnly: true,
		Repository: manifest.Repository{Owner: "bestagentkits", Repo: "sisf-sandbox"}, Egress: core.EgressPolicy{Mode: core.EgressAllowlist},
		Extensions: map[string]any{
			"profile": "default", "coder_agent": "claude", "qa_agent": "codex", "qa_agent_binary": launcher,
			"build_command": "/usr/local/bin/sisf-smoke-build", "build_docker_endpoint": front.URL,
			"build_docker_network": routeNetwork, "build_egress_proxy": "http://egress:3128", "build_runner_image": routeImage,
		},
	}
	args, err := sisfinstall.New(t.TempDir(), io.Discard).StandaloneSupervisorArgs(m)
	if err != nil {
		t.Fatalf("the installer's supervisor command line: %v", err)
	}
	return sandboxRoute{manifest: m, daemon: daemon, args: args}
}

// parseRunFlags parses a supervisor run command line with the real
// lifecycle flags; the flags runSupervisor itself registers are accepted
// and ignored here.
func parseRunFlags(t *testing.T, args []string) *lifecycleFlags {
	t.Helper()
	if len(args) == 0 || args[0] != "run" {
		t.Fatalf("not a run command line: %v", args)
	}
	var lf lifecycleFlags
	fs := flag.NewFlagSet("supervisor run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	lf.register(fs)
	for _, name := range []string{"factory-id", "manifest", "root-key-ref", "trust-dir", "lkg", "admin", "state-dsn-env"} {
		fs.String(name, "", "")
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		t.Fatalf("the supervisor does not accept the installer's command line %v: %v", args, err)
	}
	return &lf
}

// The supervisor of a Standalone install with a build sandbox, started on
// the command line the installer writes, gets past every start check that
// stops one today: the build backend selection and the QA agent's start.
// Nothing is weakened: the same install with the local backend is still
// refused.
func TestAStandaloneInstallWithABuildSandboxGetsPastTheSupervisorsStartChecks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	route := newSandboxRoute(t)
	lf := parseRunFlags(t, route.args)
	clock := core.SystemClock{}

	docker, local, err := lf.execBackends(route.manifest, clock)
	if err != nil || docker == nil || local != nil {
		t.Fatalf("execution backends: docker %v, local %v, %v", docker, local, err)
	}
	// The supervisor opens its program store before it selects the build
	// backend, as a factory that deploys locally does.
	store, _ := trustStore(t)
	lf.programs = &localPrograms{launcher: launcherPath, store: store}
	images, err := lf.imageBuilder(ctx, route.manifest, false, nil, docker, local)
	if err != nil {
		t.Fatalf("the build backend selection refused the sandbox: %v", err)
	}
	if images.Name() != "sandbox:docker" {
		t.Fatalf("candidates are built with %s, want the docker sandbox", images.Name())
	}

	// A build runs through the real filter and backend; the fake daemon
	// stands in for the runner image's build script.
	program := []byte("the candidate's program")
	sum := sha256.Sum256(program)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	route.daemon.OnStart(func(c dockerfiltertest.Container) dockerfiltertest.Outcome {
		if c.Image != routeImage || strings.Join(c.Cmd, " ") != "/usr/local/bin/sisf-smoke-build" {
			return dockerfiltertest.Outcome{ExitCode: 97}
		}
		env := map[string]string{}
		for _, kv := range c.Env {
			if k, v, ok := strings.Cut(kv, "="); ok {
				env[k] = v
			}
		}
		if env["SISF_PHASE"] != "report" {
			return dockerfiltertest.Outcome{}
		}
		report := ""
		for key, value := range map[string]string{"image-digest": digest, "image": "local/hello-app@" + digest,
			"sbom": "sbom:local", "provenance": "provenance:local"} {
			report += "sisf-report " + env["SISF_REPORT_NONCE"] + " " + key + ": " + value + "\n"
		}
		report += "sisf-report " + env["SISF_REPORT_NONCE"] + " program: " + base64.StdEncoding.EncodeToString(program) + "\n"
		return dockerfiltertest.Outcome{Stdout: report}
	})
	built, err := images.BuildImage(ctx, stages.ImageRequest{FactoryID: routeFactory, RunID: "run-0001", Unit: "app",
		Commit: "0123456789abcdef0123456789abcdef01234567", Untrusted: true})
	if err != nil || string(built.Digest) != digest || !built.TestsPassed {
		t.Fatalf("the sandboxed build = %+v, %v", built, err)
	}

	judge, err := lf.judge(ctx, judgeDeps{
		manifest: route.manifest, docker: docker, protected: []string{t.TempDir()}, dsnEnv: "SISF_STATE_DSN",
		lookupEnv: func(string) (string, bool) { return "", false }, clock: clock,
		materials: agentjudge.MaterialsFunc(func(context.Context, qa.Candidate) (agentjudge.Materials, error) {
			return agentjudge.Materials{}, nil
		}),
	})
	if err != nil {
		t.Fatalf("the QA agent was refused its start: %v", err)
	}
	if _, ok := judge.(*agentjudge.Judge); !ok {
		t.Fatalf("judge is %T, want the agent judge", judge)
	}

	// The refusal the route goes around still stands.
	lf.exec = "local"
	docker, local, err = lf.execBackends(route.manifest, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lf.imageBuilder(ctx, route.manifest, false, nil, docker, local); !core.HasCode(err, core.CodePermissionDenied) {
		t.Fatalf("the local backend outside the simulation: %v, want PERMISSION_DENIED", err)
	}
}

// The supervisor of a Standalone install whose build sandbox network is an
// ordinary bridge refuses to select the sandbox at start, before any
// candidate is built: a runner there would reach the internet around the
// egress proxy.
func TestTheSupervisorRefusesABuildSandboxOnANetworkThatIsNotInternal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	route := newSandboxRoute(t)
	route.daemon.AddNetwork(routeNetwork, false)
	lf := parseRunFlags(t, route.args)
	docker, local, err := lf.execBackends(route.manifest, core.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lf.imageBuilder(ctx, route.manifest, false, nil, docker, local); !core.HasCode(err, core.CodeNotSupported) ||
		!strings.Contains(err.Error(), "not internal") {
		t.Fatalf("the build backend selection on a non-internal network: %v", err)
	}
	if created := route.daemon.Created(); len(created) != 0 {
		t.Fatalf("a container was created: %d", len(created))
	}
}

// The image builder of a factory that keeps programs is given the store,
// so its builds export; one that keeps none, whose deploy backend runs
// images, builds no candidate at all: the supervisor would observe nothing
// of what a build made.
func TestTheSandboxedBuilderExportsOnlyWhereProgramsAreKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	route := newSandboxRoute(t)
	lf := parseRunFlags(t, route.args)
	docker, local, err := lf.execBackends(route.manifest, core.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	if images, err := lf.imageBuilder(ctx, route.manifest, false, nil, docker, local); !core.HasCode(err, core.CodeNotSupported) {
		t.Fatalf("a factory that keeps no programs builds with %+v, %v; want NOT_SUPPORTED", images, err)
	}
	store, _ := trustStore(t)
	lf.programs = &localPrograms{launcher: launcherPath, store: store}
	images, err := lf.imageBuilder(ctx, route.manifest, false, nil, docker, local)
	if err != nil {
		t.Fatal(err)
	}
	if sandbox, ok := images.(stages.SandboxImages); !ok || sandbox.Programs != store {
		t.Fatalf("a factory that keeps programs builds with %+v", images)
	}
}

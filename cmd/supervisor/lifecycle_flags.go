package main

import (
	"context"
	"flag"
	"strings"
	"time"

	execdocker "github.com/bestagentkits/sisf/adapters/exec/docker"
	execlocal "github.com/bestagentkits/sisf/adapters/exec/local"
	"github.com/bestagentkits/sisf/cmd/supervisor/runcontrol"
	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/intake/prgate"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Environment variables the lifecycle reads. Each holds a secret or a key
// and is read only from the environment, never from a flag.
const (
	envGitHubToken       = "SISF_GITHUB_TOKEN"
	envGitHubAPIURL      = "SISF_GITHUB_API_URL"
	envEdgeAdminToken    = "EDGE_ADMIN_TOKEN"
	defaultApprovalKeyRf = "env:SISF_APPROVAL_PUBLIC_KEY"
)

// lifecycleFlags configure the stages the supervisor runs after
// development: build, independent QA and release. Without
// --factory-manifest the supervisor runs none of them, as before.
type lifecycleFlags struct {
	factoryManifest, approvalKeyRef, rootPrivateKeyEnv string
	coderAgent, visibility                             string
	exec, execEndpoint, execNetwork, egressProxy       string
	runnerImage, buildCommand                          string
	buildTimeout, interval                             time.Duration
	deploy, edgeAdminURL, localProgram                 string
	syntheticProgram                                   string
	deployEndpoint, deployNetwork                      string
	appPort, buildOutageLimit                          int
	dispatcherAdminURL                                 string
	qa                                                 qaFlags
	tuning                                             releaseTuning
	// adopter starts the LKG under the local deploy backend; set when the
	// lifecycle is wired.
	adopter *lkgAdopter
	// programs is what the local deploy backend starts for a release; set
	// when the lifecycle is wired.
	programs *localPrograms
}

func (l *lifecycleFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&l.factoryManifest, "factory-manifest", "", "the factory's .sisf/factory.yaml; when set the supervisor builds, evaluates and releases candidates")
	fs.StringVar(&l.approvalKeyRef, "approval-key-ref", defaultApprovalKeyRf, "public key of the approval service, env:NAME")
	fs.StringVar(&l.rootPrivateKeyEnv, "root-private-key-env", "", "local native profile only: variable holding the root private key, to issue the supervisor's delegated keys")
	fs.StringVar(&l.coderAgent, "coder-agent", "fake", "coding agent the dispatcher runs: fake, claude, codex or pi")
	fs.StringVar(&l.visibility, "repo-visibility", "private", "visibility of the repository for the fork pull-request gate: public or private")
	fs.StringVar(&l.exec, "exec", "none", "execution backend of candidate builds: none, docker or local")
	fs.StringVar(&l.execEndpoint, "exec-docker-endpoint", "", "URL of the Docker request filter candidate builds run behind")
	fs.StringVar(&l.execNetwork, "exec-docker-network", "", "sandbox network of candidate builds")
	fs.StringVar(&l.egressProxy, "egress-proxy", "", "egress proxy URL on the sandbox network")
	fs.StringVar(&l.runnerImage, "runner-image", "", "pinned image candidate builds run in")
	fs.StringVar(&l.buildCommand, "build-command", "", "command, split on spaces, that builds a candidate in its sandbox")
	fs.DurationVar(&l.buildTimeout, "build-timeout", 20*time.Minute, "time limit of one candidate build")
	fs.IntVar(&l.buildOutageLimit, "build-outage-limit", stages.DefaultBuildRetry.HoldAfter, "outages in a row (GitHub, Docker, configuration) after which a candidate build is held until the Owner retries the issue")
	fs.DurationVar(&l.interval, "lifecycle-interval", 5*time.Second, "pause between rounds of the stages")
	fs.StringVar(&l.deploy, "deploy", "local", "deploy backend of releases: local or compose")
	fs.StringVar(&l.edgeAdminURL, "edge-admin-url", "", "admin URL of the edge; its token is read from "+envEdgeAdminToken)
	fs.StringVar(&l.localProgram, "deploy-local-program", "", "deploy local: launcher started with a release's image reference and, for a release whose program a candidate build exported, that program's file")
	fs.StringVar(&l.syntheticProgram, "synthetic-program", "", "absolute path of the application's synthetic suite, run with --url and --revision against a release whose real traffic is too low to judge it; without it such a release holds")
	fs.StringVar(&l.deployEndpoint, "deploy-docker-endpoint", "", "deploy compose: the supervisor-only Docker endpoint")
	fs.StringVar(&l.deployNetwork, "deploy-network", "", "deploy compose: the Compose network of the units")
	fs.IntVar(&l.appPort, "deploy-app-port", 8080, "deploy compose: port the application listens on in its container")
	fs.StringVar(&l.dispatcherAdminURL, "dispatcher-admin-url", "", "run control URL of the dispatcher the emergency stop cancels runs and revokes secrets through; token in "+runcontrol.EnvToken)
	l.qa.register(fs)
	l.tuning.register(fs)
}

// enabled reports whether the lifecycle stages run.
func (l *lifecycleFlags) enabled() bool { return l.factoryManifest != "" }

// execBackends builds the execution backend --exec names: at most one of
// docker and local is set.
func (l *lifecycleFlags) execBackends(m *manifest.Manifest, clock core.Clock) (docker, local execution.Backend, err error) {
	switch l.exec {
	case "none", "":
	case "docker":
		docker, err = execdocker.New(execdocker.Config{Endpoint: l.execEndpoint, FactoryID: m.FactoryID, Network: l.execNetwork, EgressProxy: l.egressProxy})
	case "local":
		local, err = execlocal.New(execlocal.Config{Clock: clock})
	default:
		err = core.NewError(core.CodeInvalidArgument, "--exec must be none, docker or local")
	}
	if err != nil {
		return nil, nil, err
	}
	return docker, local, nil
}

// imageBuilder picks how candidates are built. Untrusted code goes only to
// an isolating backend; the local one is refused except in the trusted
// simulation with the scripted agent.
func (l *lifecycleFlags) imageBuilder(ctx context.Context, m *manifest.Manifest, sim bool, gate *prgate.Gate, docker, local execution.Backend) (stages.ImageBuilder, error) {
	profile := stages.ProfileProduction
	if sim {
		profile = stages.ProfileSimulation
	}
	return stages.SelectImageBuilder(ctx, stages.ExecutionChoice{
		Profile: profile, Agent: l.coderAgent, Docker: docker, Local: local,
		Specs:     stages.SandboxSpecs{Image: l.runnerImage, Egress: m.Egress, Limits: execution.Limits{Timeout: l.buildTimeout}, Gate: gate},
		BuildArgv: strings.Fields(l.buildCommand), BuildTimeout: l.buildTimeout,
		Programs: l.programs.buildStore(),
	})
}

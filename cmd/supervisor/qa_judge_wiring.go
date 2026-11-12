package main

import (
	"context"
	"errors"
	"flag"
	"path/filepath"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/claude"
	"github.com/bestagentkits/sisf/adapters/agent/codex"
	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/adapters/agent/pi"
	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/agentenv"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state"
)

// Judges and QA agents the supervisor can run.
const (
	qaJudgeAgent = "agent"
	qaJudgeFake  = "fake"
)

// qaFlags configure the judge that scores candidates.
type qaFlags struct {
	judge, agent, model string
	binary, env         string
	workspaceRoot, home string
	timeout             time.Duration
}

func (q *qaFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&q.judge, "qa-judge", qaJudgeAgent, "judge that scores candidates: agent, a QA agent run through its adapter, or fake, the scripted judge of the trusted simulation")
	fs.StringVar(&q.agent, "qa-agent", "codex", "QA agent of the agent judge: claude, codex or pi")
	fs.StringVar(&q.model, "qa-model", "", "model the QA agent runs in place of the model of the QA route; never the coder's")
	fs.StringVar(&q.binary, "qa-agent-binary", "", "absolute path of a launcher that runs the QA agent in a sandbox and keeps its standard input and output; required with --exec docker. With --exec local under trusted_code_only, the agent program itself")
	fs.StringVar(&q.env, "qa-agent-env", "", "comma-separated names of variables passed to the QA agent besides its default credential variables")
	fs.StringVar(&q.workspaceRoot, "qa-workspace-root", "", "absolute directory of the QA agent's workspaces, evidence and home (default <temp>/sisf-qa-<factory id>)")
	fs.StringVar(&q.home, "qa-home", "", "absolute HOME, such as the operator's own, the QA agent runs with to find the login kept there (default the private <qa-workspace-root>/home); only with --exec local under trusted_code_only, and apart from the factory's secrets, state and trust; TMPDIR stays private")
	fs.DurationVar(&q.timeout, "qa-timeout", agentjudge.DefaultTimeout, "time limit of one judgment")
}

// judgeDeps are what the judge is built from besides the flags.
type judgeDeps struct {
	manifest *manifest.Manifest
	// sim is the trusted simulation profile, as wiring.SimulationProfile decides.
	sim           bool
	docker, local execution.Backend
	// protected are the factory's secrets, state and trust directories the
	// QA agent's directories must lie apart from.
	protected []string
	// dsnEnv and rootKeyEnv name variables the QA agent must never see.
	dsnEnv     string
	lookupEnv  func(string) (string, bool)
	clock      core.Clock
	store      state.Store
	materials  agentjudge.MaterialsSource
	rootKeyEnv string
}

// judge is the evaluator's model. The agent judge runs a real QA agent;
// the scripted one is accepted only in the trusted simulation with the
// scripted coding agent, so a production factory never skips QA.
func (l *lifecycleFlags) judge(ctx context.Context, d judgeDeps) (qa.Judge, error) {
	switch l.qa.judge {
	case qaJudgeFake:
		if !d.sim || l.coderAgent != "fake" {
			return nil, core.NewError(core.CodePermissionDenied, "the scripted judge is accepted only in the trusted simulation with the scripted coding agent")
		}
		return fake.New(fake.Config{Name: "fake-qa", Clock: d.clock, Roles: []agent.Role{agent.RoleQA}})
	case qaJudgeAgent:
		if err := qaSandboxAllowed(ctx, l.qa.agent, l.exec, d.docker, d.manifest.TrustedCodeOnly, l.qa.binary); err != nil {
			return nil, err
		}
		return l.qa.agentJudge(ctx, d, l.exec)
	}
	return nil, core.NewError(core.CodeInvalidArgument, "--qa-judge must be agent or fake")
}

// qaSandboxAllowed decides from the execution backend selection whether
// the QA agent may run. The QA agent reads untrusted candidates, so it is
// held to the rule untrusted code is.
//
// Under --exec docker the deployment promises isolation, but the agent
// adapters start the agent as a child of the supervisor, the same OS user
// that reads the trust directory. So every QA agent there needs
// --qa-agent-binary to name a launcher that runs it in a sandbox and keeps
// its standard input and output; without one the start is refused.
// Under --exec local the Owner stated, with trusted_code_only, that this
// machine runs only code the Owner trusts, and the direct child process is
// that declared trust model; Pi, which has no permission system of its
// own, is never run there.
func qaSandboxAllowed(ctx context.Context, agentName, exec string, docker execution.Backend, trustedCodeOnly bool, launcher string) error {
	if _, known := agentenv.Credentials(agentName); !known {
		return core.NewError(core.CodeInvalidArgument, "--qa-agent must be claude, codex or pi")
	}
	if launcher != "" && !filepath.IsAbs(launcher) {
		return core.NewError(core.CodeInvalidArgument, "--qa-agent-binary must be an absolute path")
	}
	switch exec {
	case "docker":
		if docker == nil {
			return core.NewError(core.CodeNotSupported, "no docker execution backend is configured")
		}
		caps, err := docker.Capabilities(ctx)
		if err != nil {
			return err
		}
		if !execution.UntrustedCodeAllowed(caps) {
			return core.NewError(core.CodePermissionDenied, "the docker backend proves neither isolation nor egress enforcement, so no QA agent may read untrusted candidates under it")
		}
		if launcher == "" {
			return core.NewError(core.CodePermissionDenied,
				"under --exec docker the QA agent would run as the supervisor's own OS user, which can read the trust directory: give --qa-agent-binary a launcher that runs it in a sandbox")
		}
	case "local":
		if !trustedCodeOnly || agentName == "pi" {
			return core.NewError(core.CodePermissionDenied,
				"a QA agent runs with --exec local only for claude or codex, in a deployment whose manifest states trusted_code_only")
		}
	default:
		return core.NewError(core.CodeNotSupported, "the QA agent needs --exec docker, or --exec local under trusted_code_only")
	}
	return nil
}

// agentJudge builds the QA agent's adapter and the judge around it.
func (q *qaFlags) agentJudge(ctx context.Context, d judgeDeps, exec string) (*agentjudge.Judge, error) {
	root, home, tmp, err := q.paths(d, exec)
	if err != nil {
		return nil, err
	}
	env, err := q.environment(d, home, tmp)
	if err != nil {
		return nil, err
	}
	artifacts := filepath.Join(root, "artifacts")
	var backend agentjudge.Runner
	switch q.agent {
	case "claude":
		backend, err = claude.New(claude.Config{Binary: q.binary, Env: env, Clock: d.clock, ArtifactDir: artifacts})
	case "codex":
		backend, err = codex.New(codex.Config{Binary: q.binary, Env: env, Clock: d.clock, ArtifactDir: artifacts})
	case "pi":
		backend, err = pi.New(pi.Config{Binary: q.binary, Env: env, Clock: d.clock, ArtifactDir: artifacts})
	}
	if err != nil {
		return nil, err
	}
	// A QA agent that is not installed refuses the start, not the first judgment.
	if _, err := backend.Capabilities(ctx); err != nil {
		var typed *core.Error
		if !errors.As(err, &typed) {
			err = core.NewError(core.CodeUnavailable, "the QA agent program cannot be run").WithCause(err)
		}
		return nil, err
	}
	return agentjudge.New(agentjudge.Config{
		Agent: backend, Materials: d.materials, WorkspaceRoot: filepath.Join(root, "workspaces"),
		EvidenceDir: filepath.Join(root, "evidence"), Model: q.model, Timeout: q.timeout,
		Usage: storeUsage{store: d.store}, FactoryID: d.manifest.FactoryID, Clock: d.clock,
	})
}

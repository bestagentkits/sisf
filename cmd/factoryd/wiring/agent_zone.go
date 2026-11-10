package wiring

import (
	"context"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/bestagentkits/sisf/adapters/agent/claude"
	"github.com/bestagentkits/sisf/adapters/agent/codex"
	"github.com/bestagentkits/sisf/adapters/agent/fake"
	"github.com/bestagentkits/sisf/adapters/agent/pi"
	"github.com/bestagentkits/sisf/cmd/factoryd/lifecycle"
	"github.com/bestagentkits/sisf/internal/secretbroker"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
	"github.com/bestagentkits/sisf/pkg/secrets"
	"github.com/bestagentkits/sisf/pkg/state"
)

// AgentZoneConfig is how the agent zone runs its coding agent. The agent
// zone holds no GitHub credential and no container runtime: it only hands
// each patch to the supervisor, which builds it.
type AgentZoneConfig struct {
	Store state.Store
	Clock core.Clock
	// Agent is fake, claude, codex or pi.
	Agent string
	// FakeWork and FakePatch are what the scripted agent does per run.
	FakeWork  time.Duration
	FakePatch string
	// WorkspaceRoot holds a fresh workspace per coding attempt, filled from
	// WorkspaceSource (a repository URL or path without credentials) at the
	// run's base commit. ArtifactDir is where CLI agents write artifacts.
	WorkspaceRoot   string
	WorkspaceSource string
	ArtifactDir     string
	// Sandboxed declares that this process runs inside a sandbox; Pi, which
	// has no permission system of its own, needs it.
	Sandboxed bool
	// Binary is the absolute path of a launcher that runs the agent in a
	// sandbox and keeps its standard input and output; empty runs the
	// agent's own program from PATH.
	Binary string
	// Manifest is the factory's; its execution adapter and trust model
	// decide whether a real agent may run as a child of this process.
	Manifest *manifest.Manifest
	// LookupEnv reads this process's environment. A real agent gets only
	// its allow-list from it (see package agentenv), with the names in
	// AgentEnv added.
	LookupEnv func(string) (string, bool)
	AgentEnv  []string
	// Home, when set, is the operator's own absolute HOME the agent runs
	// with in place of its private one, so the login the operator keeps
	// there is found. It is accepted only under trusted_code_only, never
	// with Sandboxed or a Binary launcher, and must lie apart from every
	// directory in ProtectedDirs. TMPDIR stays private either way.
	Home string
	// ProtectedDirs are the factory's secrets, state and trust directories
	// this process knows of.
	ProtectedDirs []string
	// Model, when set, is the model a real coding agent runs in place of
	// the model of its route; the run records the route it ran on.
	Model string
	// Owner names the exporter in leases.
	Owner string
}

// AgentZone is the coding agent, the patch exporter and the secret broker
// of the agent zone.
type AgentZone struct {
	Agent    agent.Backend
	Exporter *lifecycle.Exporter
	Broker   *secretbroker.Broker
}

// NewAgentZone builds the agent zone.
func NewAgentZone(cfg AgentZoneConfig) (*AgentZone, error) {
	if cfg.Store == nil || cfg.Clock == nil || cfg.Owner == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "the agent zone needs the store, a clock and an owner")
	}
	backend, artifacts, err := newCodingAgent(cfg)
	if err != nil {
		return nil, err
	}
	broker, err := secretbroker.New(secretbroker.Config{Provider: noSecrets{}, Clock: cfg.Clock})
	if err != nil {
		return nil, err
	}
	return &AgentZone{Agent: backend, Broker: broker, Exporter: &lifecycle.Exporter{
		Owner: cfg.Owner, State: cfg.Store, Artifacts: artifacts, Clock: cfg.Clock,
	}}, nil
}

// newCodingAgent builds the coding agent and where its artifacts are read.
func newCodingAgent(cfg AgentZoneConfig) (agent.Backend, lifecycle.ArtifactSource, error) {
	if cfg.Agent == "fake" || cfg.Agent == "" {
		if cfg.Model != "" {
			return nil, nil, core.NewError(core.CodeInvalidArgument, "--agent-model is for a real coding agent; the scripted agent runs no model")
		}
		if cfg.Home != "" {
			return nil, nil, core.NewError(core.CodeInvalidArgument, "--agent-home is for a real coding agent; the scripted agent runs no program")
		}
		memory := lifecycle.NewMemoryArtifacts()
		memory.Put([]byte(cfg.FakePatch))
		backend, err := fake.New(fake.Config{Clock: cfg.Clock, Default: fake.Patch(cfg.FakeWork, cfg.FakePatch)})
		return backend, memory, err
	}
	if err := lifecycle.SandboxedAgent(cfg.Agent, cfg.Sandboxed, cfg.WorkspaceRoot); err != nil {
		return nil, nil, err
	}
	if err := childAgentAllowed(cfg); err != nil {
		return nil, nil, err
	}
	if cfg.WorkspaceRoot == "" || !filepath.IsAbs(cfg.WorkspaceRoot) || cfg.ArtifactDir == "" || !filepath.IsAbs(cfg.ArtifactDir) {
		return nil, nil, core.NewError(core.CodeInvalidArgument, "a real coding agent needs absolute workspace and artifact directories")
	}
	env, err := agentEnvironment(cfg)
	if err != nil {
		return nil, nil, err
	}
	var inner agent.Backend
	switch cfg.Agent {
	case "claude":
		inner, err = claude.New(claude.Config{Binary: cfg.Binary, Env: env, Clock: cfg.Clock, ArtifactDir: cfg.ArtifactDir})
	case "codex":
		inner, err = codex.New(codex.Config{Binary: cfg.Binary, Env: env, Clock: cfg.Clock, ArtifactDir: cfg.ArtifactDir})
	case "pi":
		inner, err = pi.New(pi.Config{Binary: cfg.Binary, Env: env, Clock: cfg.Clock, ArtifactDir: cfg.ArtifactDir})
	default:
		return nil, nil, core.NewError(core.CodeInvalidArgument, "unknown coding agent "+cfg.Agent)
	}
	if err != nil {
		return nil, nil, err
	}
	if cfg.Model != "" {
		if inner, err = withModel(inner, cfg.Model); err != nil {
			return nil, nil, err
		}
	}
	fresh := &lifecycle.FreshWorkspaces{Backend: inner, Root: cfg.WorkspaceRoot, Provision: gitCheckout(cfg.WorkspaceSource)}
	return fresh, lifecycle.FileArtifacts{Root: cfg.ArtifactDir}, nil
}

// gitCheckout fills a new workspace with source at the base commit. No
// credential is ever part of source.
func gitCheckout(source string) func(ctx context.Context, dir string, base core.CommitSHA) error {
	return func(ctx context.Context, dir string, base core.CommitSHA) error {
		if source == "" {
			return core.NewError(core.CodeInvalidArgument, "the workspace source is not configured")
		}
		if err := base.Validate(); err != nil {
			return err
		}
		for _, args := range [][]string{{"init", "-q"}, {"fetch", "-q", "--depth=1", source, string(base)}, {"checkout", "-q", "FETCH_HEAD"}} {
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Dir = dir
			if err := cmd.Run(); err != nil {
				return core.NewError(core.CodeUnavailable, "the workspace could not be checked out").WithCause(err)
			}
		}
		return nil
	}
}

// noSecrets is the provider of an agent zone that holds no secret of its
// own to grant: the broker then issues nothing, and revoking a run's grants
// is always complete.
type noSecrets struct{}

func (noSecrets) Resolve(context.Context, secrets.SecretRef) (core.SecretValue, error) {
	return core.SecretValue{}, core.NewError(core.CodeNotFound, "the agent zone holds no grantable secret")
}

func (noSecrets) ListRefs(context.Context) ([]secrets.SecretRef, error) { return nil, nil }

// Package codex runs the local Codex CLI in exec mode with JSON-lines output
// as an agent.Backend.
//
// The prompt goes to the CLI on stdin, never as an argument. Provider
// credentials reach the CLI only through the environment given in Config and
// never appear in events, artifacts, errors or logs. The run is ephemeral: no
// session is stored, so a later run starts only from a handoff.
//
// Exec mode takes all its input at the start, so the backend does not
// support human input; app-server mode would be needed for that and is not
// implemented.
package codex

import (
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// Name is the backend name in run references.
	Name = "codex"
	// Provider is the only model provider the backend serves.
	Provider = "openai"
)

// Config builds a Backend. Every field is optional.
type Config struct {
	// Binary is the program to run; "codex" on the PATH of Env when empty.
	Binary string
	// Env is the full environment of the CLI, KEY=VALUE, and the only way
	// credentials reach it. This process's environment when nil.
	Env []string
	// Clock drives the time limit; the wall clock when nil.
	Clock core.Clock
	// ArtifactDir is where artifacts are written; a private temporary
	// directory when empty.
	ArtifactDir string
	// Secrets are extra exact values to keep out of everything recorded.
	Secrets []string
	// Unrestricted runs the CLI without its own sandbox. Set it only when
	// the workspace is an isolated sandbox that is proven to be one.
	Unrestricted bool
}

// Backend is the Codex CLI backend. It implements agent.Backend,
// agent.Resumer, agent.UsageReporter and agent.RunInfoProvider; the other
// optional abilities answer NOT_SUPPORTED.
type Backend struct{ *cliagent.Backend }

// New builds a backend for the codex program.
func New(cfg Config) (*Backend, error) {
	inner, err := cliagent.New(cliagent.Config{
		Protocol: protocol{unrestricted: cfg.Unrestricted},
		Name:     Name, Binary: cfg.Binary, Env: cfg.Env, Clock: cfg.Clock,
		ArtifactDir: cfg.ArtifactDir, Secrets: cfg.Secrets,
	})
	if err != nil {
		return nil, err
	}
	return &Backend{inner}, nil
}

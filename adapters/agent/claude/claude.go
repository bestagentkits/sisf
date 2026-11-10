// Package claude runs the local Claude Code CLI in print mode with
// stream-JSON output as an agent.Backend.
//
// The prompt goes to the CLI on stdin, never as an argument. Provider
// credentials reach the CLI only through the environment given in Config and
// never appear in events, artifacts, errors or logs. The CLI's own
// permission system is not a sandbox: unless Config.Unrestricted is set the
// agent may only edit files and use the allowed tools, and nothing it would
// have to ask about is allowed.
package claude

import (
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
	"github.com/bestagentkits/sisf/pkg/core"
)

const (
	// Name is the backend name in run references.
	Name = "claude"
	// Provider is the only model provider the backend serves.
	Provider = "anthropic"
)

// Config builds a Backend. Every field is optional.
type Config struct {
	// Binary is the program to run; "claude" on the PATH of Env when empty.
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
	// Unrestricted lets the agent use every tool without asking. Set it only
	// when the workspace is an isolated sandbox that is proven to be one.
	Unrestricted bool
	// AllowedTools are the tools a restricted coder may use besides file
	// reads and edits, in the CLI's rule syntax, for example "Bash(go test:*)".
	AllowedTools []string
}

// Backend is the Claude CLI backend. It implements agent.Backend,
// agent.Resumer, agent.UsageReporter and agent.RunInfoProvider; the other
// optional abilities answer NOT_SUPPORTED.
type Backend struct{ *cliagent.Backend }

// New builds a backend for the claude program.
func New(cfg Config) (*Backend, error) {
	inner, err := cliagent.New(cliagent.Config{
		Protocol: protocol{unrestricted: cfg.Unrestricted, allowed: cfg.AllowedTools},
		Name:     Name, Binary: cfg.Binary, Env: cfg.Env, Clock: cfg.Clock,
		ArtifactDir: cfg.ArtifactDir, Secrets: cfg.Secrets,
	})
	if err != nil {
		return nil, err
	}
	return &Backend{inner}, nil
}

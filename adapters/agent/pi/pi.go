// Package pi runs the Pi coding agent over its RPC mode as an agent.Backend.
//
// Pi is started with "--mode rpc --no-session". The task is sent as a prompt
// command on stdin, events are read from stdout as JSON lines, and when the
// agent has settled the backend asks for session statistics, then closes
// stdin so Pi exits. Pi has no permission system of its own: it runs with the
// rights of the process, so the workspace must be an isolated sandbox. A
// dialog Pi's extensions open is answered as cancelled, never approved.
//
// Provider credentials reach Pi only through the environment given in Config
// and never appear in events, artifacts, errors or logs.
package pi

import (
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Name is the backend name in run references.
const Name = "pi"

// Config builds a Backend. Every field is optional.
type Config struct {
	// Binary is the program to run; "pi" on the PATH of Env when empty.
	Binary string
	// Env is the full environment of Pi, KEY=VALUE, and the only way
	// credentials reach it. Nil is an empty environment:
	// this process's own is never inherited.
	Env []string
	// Clock drives the time limit; the wall clock when nil.
	Clock core.Clock
	// ArtifactDir is where artifacts are written; a private temporary
	// directory when empty.
	ArtifactDir string
	// Secrets are extra exact values to keep out of everything recorded.
	Secrets []string
}

// Backend is the Pi RPC backend. It implements agent.Backend, agent.Resumer,
// agent.UsageReporter and agent.RunInfoProvider; the other optional
// abilities answer NOT_SUPPORTED. It serves any provider Pi has a model for.
type Backend struct{ *cliagent.Backend }

// New builds a backend for the pi program.
func New(cfg Config) (*Backend, error) {
	inner, err := cliagent.New(cliagent.Config{
		Protocol: protocol{}, Name: Name, Binary: cfg.Binary, Env: cfg.Env, Clock: cfg.Clock,
		ArtifactDir: cfg.ArtifactDir, Secrets: cfg.Secrets,
	})
	if err != nil {
		return nil, err
	}
	return &Backend{inner}, nil
}

// Package cliagent runs a local coding-agent CLI as an agent.Backend. It owns
// everything the built-in adapters share: starting the process in its own
// process group, a time limit, cancellation by killing the group, reading the
// output stream, redacting credentials, keeping events, collecting artifacts
// and reporting the tool version. What differs per tool lives behind Protocol.
package cliagent

import (
	"encoding/json"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// Input is everything a protocol may use to build an invocation.
type Input struct {
	Task      agent.Task
	Workspace agent.WorkspaceRef
	Scopes    []string
	// Prompt is the task rendered for the agent. It is delivered on stdin,
	// never as an argument, so it cannot be seen in a process listing.
	Prompt string
}

// Invocation is how to start the tool.
type Invocation struct {
	Args []string
	// Env entries (KEY=VALUE) added to the base environment.
	Env []string
}

// Emit is one normalised event a protocol wants recorded.
type Emit struct {
	Kind    agent.EventKind
	Payload any
}

// Usage is what the tool reported about consumption. Cost is the tool's own
// estimate, never a provider statement, so it is reported unverified.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	Cost         core.Micros
}

// Outcome is the tool's own verdict on the run, from its final report.
type Outcome struct {
	OK     bool
	Detail string
}

// Update is what a protocol session returns for one input line.
type Update struct {
	Events []Emit
	// Stdin is written to the process, in order.
	Stdin []byte
	// CloseStdin closes the process input after Stdin is written.
	CloseStdin bool
	Usage      *Usage
	// Turn reports that the tool started one more assistant turn. The
	// backend enforces the task's turn limit from it.
	Turn bool
	// Final is set once the tool has reported the end of the run.
	Final *Outcome
}

// Session follows one run's output stream. Line is called once per stdout
// line, in order, from a single goroutine. A line that is not valid JSON, or
// whose type the session does not know, is ignored, not an error.
type Session interface {
	// Begin returns what to write to the process before any output.
	Begin() Update
	Line(line []byte) Update
}

// Protocol is the part of an adapter that depends on the tool.
type Protocol interface {
	// Tool is the program name, for example "claude".
	Tool() string
	// Invocation validates the input and returns how to start the tool.
	Invocation(in Input) (Invocation, error)
	NewSession(in Input) Session
}

// head is the part of a JSON stream line every protocol reads first.
type head struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
}

// Decode reads the type and subtype of a JSON stream line. It reports false
// for anything that is not a JSON object with a type, so the caller skips
// malformed and unknown lines.
func Decode(line []byte) (kind, subtype string, ok bool) {
	var h head
	if err := json.Unmarshal(line, &h); err != nil || h.Type == "" {
		return "", "", false
	}
	return h.Type, h.Subtype, true
}

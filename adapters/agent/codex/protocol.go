package codex

import (
	"encoding/json"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
)

// efforts are the reasoning effort levels the CLI accepts.
var efforts = []string{"minimal", "low", "medium", "high", "xhigh"}

type protocol struct{ unrestricted bool }

func (protocol) Tool() string { return "codex" }

// Invocation builds the exec-mode command. The workspace is the working
// root, the session is ephemeral, and the prompt is read from stdin ("-").
func (p protocol) Invocation(in cliagent.Input) (cliagent.Invocation, error) {
	route := in.Task.Policy.ModelRoute
	if err := cliagent.RequireProvider(route, "openai", "codex"); err != nil {
		return cliagent.Invocation{}, err
	}
	if err := cliagent.ValidateModel(route.Model); err != nil {
		return cliagent.Invocation{}, err
	}
	if err := cliagent.ValidateEffort(route.Effort, efforts...); err != nil {
		return cliagent.Invocation{}, err
	}
	sandbox := "workspace-write"
	switch {
	case in.Task.Role == agent.RoleQA:
		// An evaluator never writes, not even in a proven sandbox.
		sandbox = "read-only"
	case p.unrestricted:
		sandbox = "danger-full-access"
	}
	args := []string{
		"exec", "--json", "--ephemeral", "--skip-git-repo-check",
		"-C", in.Workspace.Path, "-s", sandbox,
		"-c", `approval_policy="never"`,
		"-m", route.Model,
	}
	if route.Effort != "" {
		args = append(args, "-c", `model_reasoning_effort="`+route.Effort+`"`)
	}
	return cliagent.Invocation{Args: append(args, "-")}, nil
}

func (protocol) NewSession(in cliagent.Input) cliagent.Session { return &session{prompt: in.Prompt} }

// session reads the JSON-lines events by type. Fields and event types it
// does not know are ignored.
type session struct{ prompt string }

func (s *session) Begin() cliagent.Update {
	return cliagent.Update{Stdin: []byte(s.prompt), CloseStdin: true}
}

type item struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Text     string `json:"text"`
	Command  string `json:"command"`
	Output   string `json:"aggregated_output"`
	ExitCode *int   `json:"exit_code"`
	Status   string `json:"status"`
	Changes  []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
	Message string `json:"message"`
}

const clip = 8 << 10

func (s *session) Line(line []byte) cliagent.Update {
	kind, _, ok := cliagent.Decode(line)
	if !ok {
		return cliagent.Update{}
	}
	switch kind {
	case "thread.started":
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventStatus, Payload: map[string]any{"detail": "thread started"}}}}
	case "turn.started":
		return cliagent.Update{Turn: true}
	case "item.started":
		var e struct{ Item item }
		if json.Unmarshal(line, &e) != nil || e.Item.Type != "command_execution" {
			return cliagent.Update{}
		}
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventToolCall, Payload: map[string]any{
			"id": e.Item.ID, "name": "command", "input": cliagent.Clip(e.Item.Command, clip)}}}}
	case "item.completed":
		var e struct{ Item item }
		if json.Unmarshal(line, &e) != nil {
			return cliagent.Update{}
		}
		return itemCompleted(e.Item)
	case "turn.completed":
		var e struct {
			Usage struct {
				Input  int64 `json:"input_tokens"`
				Cached int64 `json:"cached_input_tokens"`
				Output int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(line, &e)
		// The CLI reports tokens only, never a cost, so the cost stays zero.
		usage := &cliagent.Usage{InputTokens: e.Usage.Input, OutputTokens: e.Usage.Output}
		return cliagent.Update{
			Usage: usage, Final: &cliagent.Outcome{OK: true},
			Events: []cliagent.Emit{{Kind: agent.EventUsage, Payload: map[string]any{
				"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "verified": false}}},
		}
	case "turn.failed":
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(line, &e)
		return cliagent.Update{Final: &cliagent.Outcome{Detail: "the CLI reported a failed turn: " + cliagent.Clip(e.Error.Message, 512)}}
	case "error":
		// A stream error can be followed by a retry, so it is not final.
		var e item
		_ = json.Unmarshal(line, &e)
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventError, Payload: map[string]any{"detail": cliagent.Clip(e.Message, 512)}}}}
	}
	return cliagent.Update{}
}

func itemCompleted(it item) cliagent.Update {
	switch it.Type {
	case "agent_message":
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventMessage, Payload: map[string]any{"text": cliagent.Clip(it.Text, clip)}}}}
	case "command_execution":
		payload := map[string]any{"id": it.ID, "status": it.Status, "content": cliagent.Clip(it.Output, clip)}
		if it.ExitCode != nil {
			payload["exit_code"] = *it.ExitCode
			payload["is_error"] = *it.ExitCode != 0
		}
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventToolResult, Payload: payload}}}
	case "file_change":
		paths := make([]string, 0, len(it.Changes))
		for _, c := range it.Changes {
			paths = append(paths, c.Kind+" "+c.Path)
		}
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventToolResult, Payload: map[string]any{
			"id": it.ID, "status": it.Status, "name": "file_change", "paths": paths}}}}
	}
	return cliagent.Update{}
}

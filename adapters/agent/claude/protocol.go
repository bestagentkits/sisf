package claude

import (
	"encoding/json"
	"strings"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
)

// efforts are the effort levels the CLI accepts.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// readOnlyTools are the tools an evaluator may not use.
const readOnlyTools = "Edit,Write,NotebookEdit,MultiEdit"

// restrictedBase are the tools a restricted agent may always use.
const restrictedBase = "Read,Glob,Grep,Edit,Write,MultiEdit"

type protocol struct {
	unrestricted bool
	allowed      []string
}

func (protocol) Tool() string { return "claude" }

// Invocation builds the print-mode command. The session is not persisted, so
// a later run can only start from a handoff, never from this conversation.
func (p protocol) Invocation(in cliagent.Input) (cliagent.Invocation, error) {
	route := in.Task.Policy.ModelRoute
	if err := cliagent.RequireProvider(route, "anthropic", "claude"); err != nil {
		return cliagent.Invocation{}, err
	}
	if err := cliagent.ValidateModel(route.Model); err != nil {
		return cliagent.Invocation{}, err
	}
	if err := cliagent.ValidateEffort(route.Effort, efforts...); err != nil {
		return cliagent.Invocation{}, err
	}
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--no-session-persistence",
		"--model", route.Model,
	}
	if route.Effort != "" {
		args = append(args, "--effort", route.Effort)
	}
	args = append(args, "--permission-prompts", "none")
	if p.unrestricted {
		args = append(args, "--permission-mode", "bypassPermissions")
	} else {
		tools := restrictedBase
		if len(p.allowed) > 0 {
			tools += "," + strings.Join(p.allowed, ",")
		}
		args = append(args, "--permission-mode", "acceptEdits", "--allowedTools", tools)
	}
	if in.Task.Role == agent.RoleQA {
		args = append(args, "--disallowedTools", readOnlyTools)
	}
	return cliagent.Invocation{Args: args}, nil
}

func (protocol) NewSession(in cliagent.Input) cliagent.Session { return &session{prompt: in.Prompt} }

// session reads the stream-JSON output by event type. Fields and event types
// it does not know are ignored.
type session struct{ prompt string }

func (s *session) Begin() cliagent.Update {
	return cliagent.Update{Stdin: []byte(s.prompt), CloseStdin: true}
}

type content struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

type message struct {
	Message struct {
		Content []content `json:"content"`
	} `json:"message"`
}

type result struct {
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	NumTurns     int     `json:"num_turns"`
	Usage        struct {
		Input        int64 `json:"input_tokens"`
		Output       int64 `json:"output_tokens"`
		CacheRead    int64 `json:"cache_read_input_tokens"`
		CacheCreated int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

const clip = 8 << 10

func (s *session) Line(line []byte) cliagent.Update {
	kind, subtype, ok := cliagent.Decode(line)
	if !ok {
		return cliagent.Update{}
	}
	switch kind {
	case "system":
		if subtype != "init" {
			return cliagent.Update{}
		}
		var init struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(line, &init)
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventStatus, Payload: map[string]any{"detail": "session started", "model": init.Model}}}}
	case "assistant":
		var m message
		if json.Unmarshal(line, &m) != nil {
			return cliagent.Update{}
		}
		u := cliagent.Update{Turn: true}
		for _, c := range m.Message.Content {
			switch c.Type {
			case "text":
				u.Events = append(u.Events, cliagent.Emit{Kind: agent.EventMessage, Payload: map[string]any{"text": cliagent.Clip(c.Text, clip)}})
			case "tool_use":
				u.Events = append(u.Events, cliagent.Emit{Kind: agent.EventToolCall, Payload: map[string]any{
					"id": c.ID, "name": c.Name, "input": cliagent.Clip(string(c.Input), clip)}})
			}
		}
		return u
	case "user":
		var m message
		if json.Unmarshal(line, &m) != nil {
			return cliagent.Update{}
		}
		var u cliagent.Update
		for _, c := range m.Message.Content {
			if c.Type == "tool_result" {
				u.Events = append(u.Events, cliagent.Emit{Kind: agent.EventToolResult, Payload: map[string]any{
					"id": c.ToolUseID, "is_error": c.IsError, "content": cliagent.Clip(cliagent.Text(c.Content), clip)}})
			}
		}
		return u
	case "result":
		var r result
		if json.Unmarshal(line, &r) != nil {
			return cliagent.Update{}
		}
		usage := &cliagent.Usage{
			InputTokens:  r.Usage.Input + r.Usage.CacheRead + r.Usage.CacheCreated,
			OutputTokens: r.Usage.Output, Cost: cliagent.Micros(r.TotalCostUSD),
		}
		u := cliagent.Update{
			Usage: usage,
			Events: []cliagent.Emit{{Kind: agent.EventUsage, Payload: map[string]any{
				"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "cost_micros": int64(usage.Cost), "verified": false}}},
		}
		if r.IsError || subtype != "success" {
			u.Final = &cliagent.Outcome{Detail: "the CLI reported " + subtype + ": " + cliagent.Clip(r.Result, 512)}
		} else {
			u.Final = &cliagent.Outcome{OK: true}
			u.Events = append(u.Events, cliagent.Emit{Kind: agent.EventMessage, Payload: map[string]any{"text": cliagent.Clip(r.Result, clip), "final": true}})
		}
		return u
	}
	return cliagent.Update{}
}

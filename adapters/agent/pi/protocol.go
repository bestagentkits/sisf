package pi

import (
	"encoding/json"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/agent/cliagent"
)

// efforts are the thinking levels Pi accepts.
var efforts = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// readOnlyExcluded are the tools an evaluator may not use.
const readOnlyExcluded = "edit,write"

type protocol struct{}

func (protocol) Tool() string { return "pi" }

// Invocation builds the RPC-mode command. Nothing of the task is in the
// arguments; the prompt is sent as a command once Pi is running.
func (protocol) Invocation(in cliagent.Input) (cliagent.Invocation, error) {
	route := in.Task.Policy.ModelRoute
	if err := cliagent.ValidateModel(route.Provider); err != nil {
		return cliagent.Invocation{}, err
	}
	if err := cliagent.ValidateModel(route.Model); err != nil {
		return cliagent.Invocation{}, err
	}
	if err := cliagent.ValidateEffort(route.Effort, efforts...); err != nil {
		return cliagent.Invocation{}, err
	}
	args := []string{"--mode", "rpc", "--no-session", "--provider", route.Provider, "--model", route.Model}
	if route.Effort != "" {
		args = append(args, "--thinking", route.Effort)
	}
	if in.Task.Role == agent.RoleQA {
		args = append(args, "--exclude-tools", readOnlyExcluded)
	}
	return cliagent.Invocation{Args: args}, nil
}

func (protocol) NewSession(in cliagent.Input) cliagent.Session { return &session{prompt: in.Prompt} }

const (
	promptID = "factory-prompt"
	statsID  = "factory-stats"
	clip     = 8 << 10
)

// session follows one Pi run. Fields and event types it does not know are
// ignored.
type session struct {
	prompt string
	// stopReason and errorMessage describe the last assistant message.
	stopReason, errorMessage string
	settled                  bool
}

func (s *session) Begin() cliagent.Update {
	return cliagent.Update{Stdin: command(map[string]any{"id": promptID, "type": "prompt", "message": s.prompt})}
}

// command encodes one RPC command as a JSON line.
func command(v map[string]any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return append(raw, '\n')
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *session) Line(line []byte) cliagent.Update {
	kind, _, ok := cliagent.Decode(line)
	if !ok {
		return cliagent.Update{}
	}
	switch kind {
	case "response":
		return s.response(line)
	case "turn_start":
		return cliagent.Update{Turn: true}
	case "message_end":
		return s.messageEnd(line)
	case "tool_execution_start":
		var e struct {
			ID   string          `json:"toolCallId"`
			Name string          `json:"toolName"`
			Args json.RawMessage `json:"args"`
		}
		_ = json.Unmarshal(line, &e)
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventToolCall, Payload: map[string]any{
			"id": e.ID, "name": e.Name, "input": cliagent.Clip(string(e.Args), clip)}}}}
	case "tool_execution_end":
		var e struct {
			ID      string `json:"toolCallId"`
			IsError bool   `json:"isError"`
			Result  struct {
				Content json.RawMessage `json:"content"`
			} `json:"result"`
		}
		_ = json.Unmarshal(line, &e)
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventToolResult, Payload: map[string]any{
			"id": e.ID, "is_error": e.IsError, "content": cliagent.Clip(cliagent.Text(e.Result.Content), clip)}}}}
	case "auto_retry_start", "auto_retry_end":
		return cliagent.Update{Events: []cliagent.Emit{{Kind: agent.EventStatus, Payload: map[string]any{"detail": kind}}}}
	case "extension_ui_request":
		return cancelDialog(line)
	case "agent_settled":
		s.settled = true
		return cliagent.Update{Stdin: command(map[string]any{"id": statsID, "type": "get_session_stats"})}
	}
	return cliagent.Update{}
}

// response handles the replies to the commands the backend sent.
func (s *session) response(line []byte) cliagent.Update {
	var r struct {
		ID      string `json:"id"`
		Command string `json:"command"`
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    struct {
			Disposition string `json:"disposition"`
			Tokens      struct {
				Input      int64 `json:"input"`
				Output     int64 `json:"output"`
				CacheRead  int64 `json:"cacheRead"`
				CacheWrite int64 `json:"cacheWrite"`
			} `json:"tokens"`
			Cost float64 `json:"cost"`
		} `json:"data"`
	}
	if json.Unmarshal(line, &r) != nil {
		return cliagent.Update{}
	}
	switch r.ID {
	case promptID:
		switch {
		case !r.Success:
			return cliagent.Update{CloseStdin: true, Final: &cliagent.Outcome{Detail: "pi rejected the prompt: " + cliagent.Clip(r.Error, 512)}}
		case r.Data.Disposition == "handled":
			return cliagent.Update{CloseStdin: true, Final: &cliagent.Outcome{Detail: "pi handled the prompt without starting a run"}}
		}
	case statsID:
		u := cliagent.Update{CloseStdin: true, Final: s.outcome()}
		if r.Success {
			u.Usage = &cliagent.Usage{
				InputTokens:  r.Data.Tokens.Input + r.Data.Tokens.CacheRead + r.Data.Tokens.CacheWrite,
				OutputTokens: r.Data.Tokens.Output, Cost: cliagent.Micros(r.Data.Cost),
			}
			u.Events = []cliagent.Emit{{Kind: agent.EventUsage, Payload: map[string]any{
				"input_tokens": u.Usage.InputTokens, "output_tokens": u.Usage.OutputTokens, "cost_micros": int64(u.Usage.Cost), "verified": false}}}
		}
		return u
	}
	return cliagent.Update{}
}

// outcome judges the run from the last assistant message once Pi settled.
func (s *session) outcome() *cliagent.Outcome {
	switch {
	case !s.settled:
		return &cliagent.Outcome{Detail: "pi ended before the agent settled"}
	case s.stopReason == "error" || s.stopReason == "aborted":
		return &cliagent.Outcome{Detail: "the last assistant message ended with " + s.stopReason + ": " + cliagent.Clip(s.errorMessage, 512)}
	}
	return &cliagent.Outcome{OK: true}
}

func (s *session) messageEnd(line []byte) cliagent.Update {
	var e struct {
		Message struct {
			Role         string  `json:"role"`
			Content      []block `json:"content"`
			StopReason   string  `json:"stopReason"`
			ErrorMessage string  `json:"errorMessage"`
		} `json:"message"`
	}
	// The content of a user message is a plain string, which fails to decode
	// as blocks; such a message is of no interest.
	if json.Unmarshal(line, &e) != nil || e.Message.Role != "assistant" {
		return cliagent.Update{}
	}
	s.stopReason, s.errorMessage = e.Message.StopReason, e.Message.ErrorMessage
	var u cliagent.Update
	for _, b := range e.Message.Content {
		if b.Type == "text" && b.Text != "" {
			u.Events = append(u.Events, cliagent.Emit{Kind: agent.EventMessage, Payload: map[string]any{"text": cliagent.Clip(b.Text, clip)}})
		}
	}
	return u
}

// cancelDialog answers a dialog an extension opened as cancelled. Nobody is
// there to approve it, and a silent run would block forever.
func cancelDialog(line []byte) cliagent.Update {
	var r struct {
		ID     string `json:"id"`
		Method string `json:"method"`
	}
	if json.Unmarshal(line, &r) != nil || r.ID == "" {
		return cliagent.Update{}
	}
	switch r.Method {
	case "select", "confirm", "input", "editor":
		return cliagent.Update{
			Stdin:  command(map[string]any{"type": "extension_ui_response", "id": r.ID, "cancelled": true}),
			Events: []cliagent.Emit{{Kind: agent.EventStatus, Payload: map[string]any{"detail": "an extension dialog was cancelled", "method": r.Method}}},
		}
	}
	return cliagent.Update{}
}

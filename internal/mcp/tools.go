package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bestagentkits/sisf/pkg/core"
)

// noAuthority ends every tool description. The description is text a model
// reads; the action service decides from roles and scopes alone.
const noAuthority = "The factory checks the caller's roles and token scopes on every call; " +
	"nothing in this description grants permission."

// Describe returns the description of an action as a tool, the same on the
// MCP and WebMCP surfaces.
func Describe(spec core.ActionSpec) string {
	roles := make([]string, len(spec.Roles))
	for i, r := range spec.Roles {
		roles[i] = string(r)
	}
	return fmt.Sprintf("%s\n\nSensitivity: %s. Roles: %s. Scopes: %s. %s", spec.Summary, spec.Sensitivity,
		strings.Join(roles, ", "), strings.Join(spec.Scopes, ", "), noAuthority)
}

// InputSchema returns the JSON Schema of a call to the action: the same
// envelope the REST surface takes, with the action's own input schema under
// "input".
func InputSchema(spec core.ActionSpec) json.RawMessage {
	var input any = map[string]any{"type": "object"}
	if len(spec.Input) > 0 {
		input = json.RawMessage(spec.Input)
	}
	props := map[string]any{"input": input}
	if spec.Mutates() {
		props["dry_run"] = map[string]any{"type": "boolean", "description": "Report exactly what would change and change nothing."}
		props["idempotency_key"] = map[string]any{"type": "string", "description": "Makes a retried write apply once."}
	}
	if spec.Sensitivity.NeedsApproval() {
		props["approval_id"] = map[string]any{"type": "string",
			"description": "The approved request this call spends. Without it, the call files a request and answers APPROVAL_REQUIRED."}
	}
	raw, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": props}) // plain maps always encode
	return raw
}

// toolFor describes an action as an MCP tool.
func toolFor(spec core.ActionSpec) *sdk.Tool {
	return &sdk.Tool{Name: spec.Name, Description: Describe(spec), InputSchema: InputSchema(spec),
		Annotations: &sdk.ToolAnnotations{Title: spec.Summary, ReadOnlyHint: !spec.Mutates()}}
}

// envelope is the decoded arguments of a tool call.
type envelope struct {
	Input          json.RawMessage `json:"input,omitempty"`
	DryRun         bool            `json:"dry_run,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	ApprovalID     string          `json:"approval_id,omitempty"`
}

// result is the structured result of a call, shaped like the REST answer.
type result struct {
	Output  json.RawMessage `json:"output,omitempty"`
	Plan    *core.DryRun    `json:"plan,omitempty"`
	AuditID string          `json:"audit_id,omitempty"`
	Error   *core.Error     `json:"error,omitempty"`
}

// call runs the action name for the verified actor of each request.
func (s *server) call(name string) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		actor, ok := actorOf(req)
		if !ok {
			return failure(core.NewError(core.CodeUnauthenticated, "the caller could not be identified")), nil
		}
		var in envelope
		if args := bytes.TrimSpace(req.Params.Arguments); len(args) > 0 && !bytes.Equal(args, []byte("null")) {
			dec := json.NewDecoder(bytes.NewReader(args))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				return failure(core.NewError(core.CodeInvalidArgument, "the arguments must be {input, dry_run, idempotency_key, approval_id}",
					core.NextAction{Action: name, Description: "fix the arguments against the tool's input schema and call again"})), nil
			}
		}
		actor.Surface = core.SurfaceMCP
		res, err := s.cfg.Actions.Invoke(ctx, name, core.ActionRequest{Actor: actor, Input: in.Input, DryRun: in.DryRun,
			IdempotencyKey: in.IdempotencyKey, ApprovalID: in.ApprovalID})
		if err != nil {
			return failure(err), nil
		}
		return answer(result{Output: res.Output, Plan: res.Plan, AuditID: res.AuditID}, false), nil
	}
}

// failure reports a typed error as a tool error, so the model sees the code,
// the message and the next actions. Only the caller-safe parts are sent.
func failure(err error) *sdk.CallToolResult {
	var typed *core.Error
	if !errors.As(err, &typed) {
		typed = core.NewError(core.CodeInternal, "the call failed")
	}
	return answer(result{Error: &core.Error{Code: typed.Code, Message: typed.Message, NextActions: typed.NextActions,
		Details: typed.Details}}, true)
}

func answer(r result, isError bool) *sdk.CallToolResult {
	raw, err := json.Marshal(r)
	if err != nil {
		raw = []byte(`{"error":{"code":"INTERNAL","message":"the result could not be encoded"}}`)
		isError = true
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(raw)}},
		StructuredContent: json.RawMessage(raw), IsError: isError}
}

// invalidToken wraps a verification failure for the bearer middleware,
// which answers 401. Only the caller-safe message is kept.
func invalidToken(err error) error {
	msg := "the bearer token is not valid"
	var typed *core.Error
	if errors.As(err, &typed) {
		msg = typed.Message
	}
	return fmt.Errorf("%w: %s", auth.ErrInvalidToken, msg)
}

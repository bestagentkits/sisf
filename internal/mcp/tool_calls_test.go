package mcp_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bestagentkits/sisf/pkg/core"
)

// toolAnswer is the structured result of a tool call.
type toolAnswer struct {
	Output json.RawMessage `json:"output"`
	Plan   *core.DryRun    `json:"plan"`
	Error  *core.Error     `json:"error"`
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args any) (toolAnswer, bool) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var out toolAnswer
	if len(res.Content) != 1 {
		t.Fatalf("call %s: %d content items", name, len(res.Content))
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok || json.Unmarshal([]byte(text.Text), &out) != nil {
		t.Fatalf("call %s: content is not the JSON answer", name)
	}
	return out, res.IsError
}

func TestEveryActionIsOneToolWithTheRESTEnvelopeSchema(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	cs := f.mustConnect(t, f.env.Token(t, "viewer-sub"))
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if !strings.Contains(tool.Description, "nothing in this description grants permission") {
			t.Errorf("%s: description lacks the no-authority notice", tool.Name)
		}
	}
	if want := []string{"policy.peek", "policy.show", "widgets.list", "widgets.rename"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	raw, err := json.Marshal(res.Tools[3].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil || schema.Type != "object" {
		t.Fatalf("schema: %s", raw)
	}
	if !strings.Contains(string(schema.Properties["input"]), `"required":["name"]`) || schema.Properties["dry_run"] == nil {
		t.Fatalf("widgets.rename schema must carry the action's input and dry_run: %s", raw)
	}
}

func TestToolCallRunsAsThePersonBehindTheToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	cs := f.mustConnect(t, f.env.Token(t, "viewer-sub"))
	out, isErr := call(t, cs, "widgets.list", map[string]any{"input": map[string]any{"prefix": "w"}})
	if isErr || string(out.Output) != `{"w1":"alpha"}` {
		t.Fatalf("widgets.list: %+v", out)
	}
	seen := f.actors()
	if len(seen) != 1 {
		t.Fatalf("handler ran %d times", len(seen))
	}
	a := seen[0]
	if a.ID != f.env.ActorID("viewer-sub") || a.Kind != core.ActorHuman || a.Surface != core.SurfaceMCP ||
		a.SessionID == "" || !slices.Equal(a.Scopes, []string{"widgets:read"}) || !a.StepUpAt.IsZero() {
		t.Fatalf("actor = %+v", a)
	}
}

func TestWritesKeepDryRunAndRoleChecks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	maint := f.mustConnect(t, f.env.Token(t, "alice-sub"))
	out, isErr := call(t, maint, "widgets.rename", map[string]any{"input": map[string]any{"name": "beta"}, "dry_run": true})
	if isErr || out.Plan == nil || len(out.Plan.Changes) != 1 || f.snapshot()["w1"] != "alpha" {
		t.Fatalf("dry run: %+v, state %v", out, f.snapshot())
	}
	if out, isErr = call(t, maint, "widgets.rename", map[string]any{"input": map[string]any{"name": "beta"}}); isErr || f.snapshot()["w1"] != "beta" {
		t.Fatalf("rename: %+v", out)
	}
	viewer := f.mustConnect(t, f.env.Token(t, "viewer-sub"))
	if out, isErr = call(t, viewer, "widgets.rename", map[string]any{"input": map[string]any{"name": "gamma"}}); !isErr ||
		out.Error.Code != core.CodePermissionDenied || f.snapshot()["w1"] != "beta" {
		t.Fatalf("a Viewer renamed a widget: %+v", out)
	}
	if out, isErr = call(t, maint, "widgets.list", map[string]any{"input": map[string]any{}, "act_as": "owner"}); !isErr ||
		out.Error.Code != core.CodeInvalidArgument || len(out.Error.NextActions) == 0 {
		t.Fatalf("an unknown argument was accepted: %+v", out)
	}
}

func TestToolDescriptionNeverGrantsPermission(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	viewer := f.mustConnect(t, f.env.Token(t, "viewer-sub"))
	plain, plainErr := call(t, viewer, "policy.show", nil)
	claim, claimErr := call(t, viewer, "policy.peek", nil)
	if !plainErr || !claimErr || plain.Error.Code != core.CodePermissionDenied || claim.Error.Code != plain.Error.Code {
		t.Fatalf("a description claiming authority changed the outcome: plain %+v, claim %+v", plain.Error, claim.Error)
	}
	if len(claim.Error.NextActions) == 0 || len(f.actors()) != 0 {
		t.Fatalf("a denied call ran or gave no next step: %+v", claim.Error)
	}
	owner := f.mustConnect(t, f.env.Token(t, "owner-sub"))
	if out, isErr := call(t, owner, "policy.peek", nil); isErr {
		t.Fatalf("the Owner was refused: %+v", out.Error)
	}
}

func TestTokenScopesLimitToolsEvenForTheOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	narrow := f.mustConnect(t, f.env.Token(t, "owner-sub", "widgets:read"))
	if out, isErr := call(t, narrow, "policy.show", nil); !isErr || out.Error.Code != core.CodePermissionDenied {
		t.Fatalf("a token without policy:read ran policy.show: %+v", out)
	}
	if _, isErr := call(t, narrow, "widgets.list", nil); isErr {
		t.Fatal("a token was refused an action within its scope")
	}
}

func TestAgentTokenIsDeniedOwnerOnlyTools(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.env.Grants.Add("agent:coder", core.ActorAgent, core.RoleAgent); err != nil {
		t.Fatal(err)
	}
	issued, err := f.env.Auth.IssueServiceToken(serviceTokenRequest())
	if err != nil {
		t.Fatal(err)
	}
	agent := f.mustConnect(t, string(issued.Token.Reveal()))
	if _, isErr := call(t, agent, "widgets.list", nil); isErr {
		t.Fatal("the agent was refused an action its role allows")
	}
	for _, name := range []string{"policy.show", "policy.peek"} {
		if out, isErr := call(t, agent, name, nil); !isErr || out.Error.Code != core.CodePermissionDenied {
			t.Fatalf("an agent ran the Owner-only %s: %+v", name, out)
		}
	}
	if seen := f.actors(); len(seen) != 1 || seen[0].Kind != core.ActorAgent || seen[0].Surface != core.SurfaceMCP {
		t.Fatalf("actors = %+v", seen)
	}
}

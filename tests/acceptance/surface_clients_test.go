package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
)

// The five surfaces a person reaches the action service through.
const (
	surfaceCLI       = "cli"
	surfaceREST      = "rest"
	surfaceMCP       = "mcp"
	surfaceWebMCP    = "webmcp"
	surfaceDashboard = "dashboard"
)

// auditSurface is the surface each one records in the audit log: the CLI
// and the WebMCP script call REST.
var auditSurface = map[string]core.Surface{surfaceCLI: core.SurfaceAPI, surfaceREST: core.SurfaceAPI,
	surfaceWebMCP: core.SurfaceAPI, surfaceMCP: core.SurfaceMCP, surfaceDashboard: core.SurfaceDashboard}

// dashboardForms are the actions the dashboard posts; it reads the others
// through its pages, which are no request a caller can repeat elsewhere.
var dashboardForms = []string{"factory.emergency_stop", "deployments.rollback", "approvals.decide", "setup.sample_issue"}

// call is one request, as every surface carries it.
type call struct {
	Input      json.RawMessage
	DryRun     bool
	ApprovalID string
}

// outcome is what a caller learns from one call: the typed error code, or
// the output and the plan.
type outcome struct {
	Code    core.ErrorCode
	Message string
	Next    []core.NextAction
	Details map[string]string
	Output  json.RawMessage
	Plan    json.RawMessage
}

// clients reach the factory as one person on every surface.
type clients struct {
	f      *harness.Factory
	person *harness.Person
	pat    string
	mcp    *sdk.ClientSession
	tools  map[string]string // WebMCP tool name -> REST path
}

// sisfBinary is the CLI built once for the package.
var sisfBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sisf-cli-")
	if err != nil {
		panic(err)
	}
	sisfBinary = filepath.Join(dir, "sisf")
	build := exec.Command("go", "build", "-o", sisfBinary, "./cmd/sisf")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build the sisf CLI: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// allScopes are the scopes of every registered action.
func allScopes(f *harness.Factory) []string {
	var out []string
	for _, spec := range f.Control.Actions.Specs() {
		for _, sc := range spec.Scopes {
			if !slices.Contains(out, sc) {
				out = append(out, sc)
			}
		}
	}
	slices.Sort(out)
	return out
}

// newClients gives person a personal access token, an OAuth access token
// for MCP and the WebMCP tool list. The person must have just stepped up,
// which minting a token needs.
func newClients(t *testing.T, f *harness.Factory, person *harness.Person) *clients {
	t.Helper()
	c := &clients{f: f, person: person, pat: person.PAT(allScopes(f)...), tools: map[string]string{}}
	access := person.AccessToken(allScopes(f)...)
	client := sdk.NewClient(&sdk.Implementation{Name: "acceptance", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{Endpoint: f.Base() + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{access}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect to MCP: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	c.mcp = cs
	var list struct {
		Tools []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"tools"`
	}
	decode(t, person.Do(http.MethodGet, "/webmcp/actions.json", nil, nil), &list)
	for _, tool := range list.Tools {
		c.tools[tool.Name] = tool.Path
	}
	return c
}

// bearer adds a bearer token to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// on calls action through surface.
func (c *clients) on(t *testing.T, surface, action string, req call) outcome {
	t.Helper()
	switch surface {
	case surfaceCLI:
		return c.cli(t, action, req)
	case surfaceREST:
		return c.rest(t, action, req, http.Header{"Authorization": {"Bearer " + c.pat}}, "/api/v1/actions/"+action)
	case surfaceMCP:
		return c.mcpCall(t, action, req)
	case surfaceWebMCP:
		path, ok := c.tools[action]
		if !ok {
			t.Fatalf("the WebMCP tool list has no %s", action)
		}
		return c.rest(t, action, req, http.Header{auth.CSRFHeader: {c.person.CSRF()}}, path)
	case surfaceDashboard:
		return c.dashboard(t, action, req)
	}
	t.Fatalf("no surface %s", surface)
	return outcome{}
}

func body(req call) []byte {
	m := map[string]any{"input": req.Input}
	if req.DryRun {
		m["dry_run"] = true
	}
	if req.ApprovalID != "" {
		m["approval_id"] = req.ApprovalID
	}
	raw, _ := json.Marshal(m)
	return raw
}

// rest posts to path, with a bearer token or with the person's session.
func (c *clients) rest(t *testing.T, action string, req call, header http.Header, path string) outcome {
	t.Helper()
	header = header.Clone()
	header.Set("Content-Type", "application/json")
	var resp *http.Response
	if header.Get("Authorization") != "" {
		r, err := http.NewRequest(http.MethodPost, c.f.Base()+path, bytes.NewReader(body(req)))
		if err != nil {
			t.Fatal(err)
		}
		r.Header = header
		if resp, err = http.DefaultClient.Do(r); err != nil {
			t.Fatalf("REST %s: %v", action, err)
		}
		defer func() { _ = resp.Body.Close() }()
	} else {
		resp = c.person.Do(http.MethodPost, path, bytes.NewReader(body(req)), header)
	}
	raw, _ := io.ReadAll(resp.Body)
	return parseResult(t, raw)
}

// parseResult reads an action result or a typed error envelope.
func parseResult(t *testing.T, raw []byte) outcome {
	t.Helper()
	var r struct {
		Output json.RawMessage `json:"output"`
		Plan   json.RawMessage `json:"plan"`
		Error  *core.Error     `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("not a result: %s", raw)
	}
	if r.Error != nil {
		return outcome{Code: r.Error.Code, Message: r.Error.Message, Next: r.Error.NextActions, Details: r.Error.Details}
	}
	return outcome{Output: canonical(r.Output), Plan: canonical(r.Plan)}
}

// mcpCall calls the action's tool with the OAuth access token.
func (c *clients) mcpCall(t *testing.T, action string, req call) outcome {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal(body(req), &args); err != nil {
		t.Fatal(err)
	}
	res, err := c.mcp.CallTool(context.Background(), &sdk.CallToolParams{Name: action, Arguments: args})
	if err != nil {
		t.Fatalf("MCP %s: %v", action, err)
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("MCP %s answered no text", action)
	}
	return parseResult(t, []byte(text.Text))
}

// canonical compacts JSON so equal values compare equal.
func canonical(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	out, _ := json.Marshal(v)
	return out
}

func decode(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode an answer of %d: %v", resp.StatusCode, err)
	}
}

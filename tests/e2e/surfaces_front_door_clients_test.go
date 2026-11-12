//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
)

// restCall posts an action to the REST API with a bearer token, or none.
func restCall(t *testing.T, token, action, input string) actionResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, controlURL+"/api/v1/actions/"+action, strings.NewReader(`{"input":`+input+`}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("REST %s: %v", action, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out actionResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("REST %s answered %d with no result: %v", action, resp.StatusCode, err)
	}
	return out
}

// decodeOutput fails on a typed error and decodes the output.
func decodeOutput(t *testing.T, what string, r actionResult, into any) {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("%s: %s %s", what, r.Error.Code, r.Error.Message)
	}
	if err := json.Unmarshal(r.Output, into); err != nil {
		t.Fatalf("%s: the output cannot be read: %v", what, err)
	}
}

// mcpCall calls one tool on the control plane's MCP endpoint.
func mcpCall(t *testing.T, access, tool string, args map[string]any) actionResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "e2e", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: controlURL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{access}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connect to MCP: %v", err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil || len(res.Content) == 0 {
		t.Fatalf("MCP %s: %v", tool, err)
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("MCP %s answered no text", tool)
	}
	var out actionResult
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("MCP %s answered %q", tool, text.Text)
	}
	return out
}

// bearer adds a bearer token to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// confirmedForm posts a dashboard form as the person; when the answer asks
// for a step-up it confirms exactly the call the page names, as the page
// offers, and posts again. It returns the error code shown, or "" when done.
func confirmedForm(t *testing.T, b *harness.Browser, action string, fields url.Values) core.ErrorCode {
	t.Helper()
	code, stepUp := postForm(t, b, action, fields)
	if code != core.CodeStepUpRequired {
		return code
	}
	if stepUp[0] != action || stepUp[1] == "" {
		t.Fatalf("the dashboard asks for a step-up of %v for %s", stepUp, action)
	}
	b.StepUpFor(stepUp[0], stepUp[1])
	code, _ = postForm(t, b, action, fields)
	return code
}

// postForm posts one dashboard form and reads the result page.
func postForm(t *testing.T, b *harness.Browser, action string, fields url.Values) (core.ErrorCode, [2]string) {
	t.Helper()
	form := url.Values{auth.CSRFFormField: {b.CSRF()}, "return_to": {"/"}}
	for k, v := range fields {
		form[k] = v
	}
	resp := b.Do(http.MethodPost, "/actions/"+action, strings.NewReader(form.Encode()),
		harness.NavigatedForm())
	raw, _ := io.ReadAll(resp.Body)
	if m := pageCode.FindSubmatch(raw); m != nil {
		var stepUp [2]string
		if a, g := pageStepUpAction.FindSubmatch(raw), pageStepUpTarget.FindSubmatch(raw); a != nil && g != nil {
			stepUp = [2]string{html.UnescapeString(string(a[1])), html.UnescapeString(string(g[1]))}
		}
		return core.ErrorCode(m[1]), stepUp
	}
	if !pageDone.Match(raw) {
		t.Fatalf("the dashboard answered %s with %d and neither a result nor an error", action, resp.StatusCode)
	}
	return "", [2]string{}
}

// approvalRequest is the part of a pending request a decision names.
type approvalRequest struct {
	ID             string `json:"id"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	Nonce          string `json:"nonce"`
	SubjectDigest  string `json:"subject_digest"`
	PolicyRevision string `json:"policy_revision"`
}

// pendingApproval finds the newest pending request for action.
func pendingApproval(t *testing.T, pat, action string) approvalRequest {
	t.Helper()
	var list struct {
		Requests []approvalRequest `json:"requests"`
	}
	decodeOutput(t, "approvals.list", restCall(t, pat, "approvals.list", `{}`), &list)
	for i := len(list.Requests) - 1; i >= 0; i-- {
		if r := list.Requests[i]; r.Action == action && r.Status == "pending" {
			return r
		}
	}
	raw, _ := json.Marshal(list.Requests)
	t.Fatalf("no pending request for %s: %s", action, bytes.TrimSpace(raw))
	return approvalRequest{}
}

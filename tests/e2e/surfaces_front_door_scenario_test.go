//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// The result page of a dashboard form: an error with its step-up form, or
// done.
var (
	pageCode         = regexp.MustCompile(`data-error-code="([A-Z_]+)"`)
	pageDone         = regexp.MustCompile(`data-state="done"`)
	pageStepUpAction = regexp.MustCompile(`<input type="hidden" name="action" value="([^"]+)">`)
	pageStepUpTarget = regexp.MustCompile(`<input type="hidden" name="target" value="([^"]+)">`)
)

// actionResult is an action's answer on the REST API: its output or a
// typed error.
type actionResult struct {
	Output json.RawMessage `json:"output"`
	Error  *core.Error     `json:"error"`
}

// runSurfaces proves the surfaces of a running factory to a person who
// signed in at the identity provider: the dashboard, the REST API with a
// personal access token, the MCP endpoint with an OAuth access token from
// the factory's authorization server (PKCE), the WebMCP tool list, and an
// approval decided on the dashboard after a step-up for that one request.
func runSurfaces(t *testing.T, d deployment) {
	t.Helper()
	owner := harness.NewBrowser(t, controlURL, fakeIdentityProvider)
	owner.SignIn(wiring.ProviderOIDC, ownerSubject)
	if page := owner.Do(http.MethodGet, "/", nil, nil); page.StatusCode != http.StatusOK {
		t.Fatalf("the dashboard answered the signed-in Owner %d", page.StatusCode)
	}

	// REST with a personal access token minted after a step-up.
	pat := owner.PAT("issues:read", "approvals:read")
	var issues struct {
		Issues []json.RawMessage `json:"issues"`
	}
	decodeOutput(t, "issues.list", restCall(t, pat, "issues.list", `{"limit":10}`), &issues)
	if unauth := restCall(t, "", "issues.list", `{}`); unauth.Error == nil || unauth.Error.Code != core.CodeUnauthenticated {
		t.Fatalf("issues.list without a token answered %+v, want UNAUTHENTICATED", unauth.Error)
	}

	// MCP with an access token from the authorization server.
	status := mcpCall(t, owner.AccessToken("factory:read"), "factory.status", map[string]any{})
	var overview struct {
		FactoryID string `json:"factory_id"`
	}
	decodeOutput(t, "factory.status over MCP", status, &overview)
	m, err := manifest.Parse(d.factoryYAML(t))
	if err != nil {
		t.Fatal(err)
	}
	if overview.FactoryID != string(m.FactoryID) {
		t.Fatalf("factory.status over MCP names %q, want %s", overview.FactoryID, m.FactoryID)
	}

	// WebMCP: the tools a script in the dashboard may call.
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	resp := owner.Do(http.MethodGet, "/webmcp/actions.json", nil, nil)
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("/webmcp/actions.json: %d %v", resp.StatusCode, err)
	}
	if !slices.ContainsFunc(tools.Tools, func(tool struct {
		Name string `json:"name"`
	}) bool {
		return tool.Name == "issues.list"
	}) {
		t.Fatalf("the WebMCP tool list lacks issues.list: %+v", tools.Tools)
	}

	// A budget change filed on the dashboard waits for an approval, which
	// only the dashboard decides, after a step-up for that request.
	change := url.Values{"mode": {"unlimited"}, "alert_threshold_micros": {"40000000"}}
	if code := confirmedForm(t, owner, "budget.set", change); code != core.CodeApprovalRequired {
		t.Fatalf("a budget change without an approval answered %q", code)
	}
	request := pendingApproval(t, pat, "budget.set")
	decide := url.Values{"request_id": {request.ID}, "nonce": {request.Nonce}, "subject_digest": {request.SubjectDigest},
		"policy_revision": {request.PolicyRevision}, "decision": {"approve"}}
	if code := confirmedForm(t, owner, "approvals.decide", decide); code != "" {
		t.Fatalf("the dashboard could not decide the approval after a step-up: %q", code)
	}
	var proposal struct {
		Decision *struct {
			Decision string `json:"decision"`
		} `json:"decision"`
	}
	decodeOutput(t, "approvals.get", restCall(t, pat, "approvals.get", `{"id":"`+request.ID+`"}`), &proposal)
	if proposal.Decision == nil || proposal.Decision.Decision != "approve" {
		t.Fatalf("the REST API does not show the dashboard's decision: %+v", proposal.Decision)
	}
}

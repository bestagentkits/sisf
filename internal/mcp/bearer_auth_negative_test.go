package mcp_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/mcp"
	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/pkg/core"
)

func serviceTokenRequest() auth.ServiceTokenRequest {
	return auth.ServiceTokenRequest{ActorID: "agent:coder", Kind: core.ActorAgent, Name: "coder",
		Scopes: []string{"widgets:read", "policy:read"}}
}

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
	`"capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`

// post sends a raw initialize request with header.
func post(t *testing.T, base string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+mcp.Path, strings.NewReader(initialize))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, vs := range header {
		req.Header[k] = vs
	}
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestUnauthenticatedRequestsPointAtTheResourceMetadata(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	want := `resource_metadata="` + f.env.Server.ProtectedResourceMetadataURL() + `"`
	// A signed-in browser's session cookie is never read here.
	b := f.env.Browser(t)
	b.SignIn(t, "owner-sub")
	cookieOnly := b.Do(t, http.MethodPost, mcp.Path, nil, http.Header{"Content-Type": {"application/json"}})
	for name, resp := range map[string]*http.Response{
		"no credentials":     post(t, f.env.Base, nil),
		"garbage token":      post(t, f.env.Base, http.Header{"Authorization": {"Bearer not-a-token"}}),
		"basic credentials":  post(t, f.env.Base, http.Header{"Authorization": {"Basic dXNlcjpwYXNz"}}),
		"unknown sisf token": post(t, f.env.Base, http.Header{"Authorization": {"Bearer sisf_pat_unknown"}}),
		"ambient session":    cookieOnly,
	} {
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), want) {
			t.Errorf("%s: %d %q", name, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	}
	if resp := post(t, f.env.Base, http.Header{"Authorization": {"Bearer " + f.env.Token(t, "viewer-sub")}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("a valid token was refused: %d", resp.StatusCode)
	}
}

func TestRevokedSessionBehindTokenStopsTheClient(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tok := f.env.Token(t, "owner-sub")
	cs := f.mustConnect(t, tok)
	if _, isErr := call(t, cs, "policy.show", nil); isErr {
		t.Fatal("setup: the Owner was refused")
	}
	actor, err := f.env.Server.VerifyAccessToken(t.Context(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.env.Auth.RevokeSession(actor.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "policy.show"}); err == nil {
		t.Fatal("a token kept working after its session was revoked")
	}
	if _, err := f.connect(t, tok); err == nil {
		t.Fatal("a new connection was accepted with a token whose session was revoked")
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	tok := f.env.Token(t, "viewer-sub")
	f.env.Clock.Advance(oauthas.DefaultAccessTokenTTL + time.Second)
	if resp := post(t, f.env.Base, http.Header{"Authorization": {"Bearer " + tok}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an expired token was accepted: %d", resp.StatusCode)
	}
}

func TestEndpointRefusesIncompleteConfiguration(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for name, cfg := range map[string]mcp.Config{
		"no actions":   {AccessTokens: f.env.Server, ResourceMetadataURL: "https://x.example/m"},
		"no verifier":  {Actions: stubActions{}, ResourceMetadataURL: "https://x.example/m"},
		"relative URL": {Actions: stubActions{}, AccessTokens: f.env.Server, ResourceMetadataURL: "/m"},
	} {
		if _, err := mcp.NewHandler(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
}

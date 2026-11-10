package auth_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestServiceTokensAreNeverIssuedForAPerson(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for name, req := range map[string]auth.ServiceTokenRequest{
		"human kind":          {ActorID: "agent:coder", Kind: core.ActorHuman, Scopes: []string{"runs:write"}},
		"person's actor ID":   {ActorID: e.actorID("owner-sub"), Kind: core.ActorAgent, Scopes: []string{"runs:write"}},
		"kind and ID differ":  {ActorID: "agent:coder", Kind: core.ActorService, Scopes: []string{"runs:write"}},
		"no name":             {ActorID: "agent:", Kind: core.ActorAgent, Scopes: []string{"runs:write"}},
		"no scopes":           {ActorID: "agent:coder", Kind: core.ActorAgent},
		"longer than an hour": {ActorID: "agent:coder", Kind: core.ActorAgent, Scopes: []string{"runs:write"}, TTL: 2 * time.Hour},
	} {
		if _, err := e.svc.IssueServiceToken(req); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestServiceTokensActOnlyAsTheirAgent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	issued, err := e.svc.IssueServiceToken(auth.ServiceTokenRequest{ActorID: "agent:coder", Kind: core.ActorAgent, Scopes: []string{"prs:write"}})
	if err != nil {
		t.Fatal(err)
	}
	if !issued.ExpiresAt.Equal(e.clock.Now().Add(auth.DefaultServiceTokenTTL)) {
		t.Fatalf("expiry = %v", issued.ExpiresAt)
	}
	raw := string(issued.Token.Reveal())
	// The agent drives a browser that holds the Owner's session cookie.
	b := e.browser(t)
	b.signIn(t, "google", "owner-sub")
	actor, code := b.actor(t, http.MethodPost, bearer(raw))
	if code != http.StatusOK || actor.ID != "agent:coder" || actor.Kind != core.ActorAgent {
		t.Fatalf("a bearer token borrowed the ambient session: %+v (%d)", actor, code)
	}
	if d := e.authorize(t, actor, mergeSpec, ""); d.Effect != core.EffectDeny {
		t.Fatalf("an agent token got a person's role: %+v", d)
	}
	for path, method := range map[string]string{"/auth/session": http.MethodGet, "/auth/logout": http.MethodPost, "/auth/tokens": http.MethodGet} {
		if resp := b.do(t, method, path, "", bearer(raw)); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with a service token: %d", method, path, resp.StatusCode)
		}
	}
	if _, err := e.svc.IssuePAT(actor, auth.PATRequest{Scopes: []string{"prs:write"}}); !core.HasCode(err, core.CodeUnauthenticated) {
		t.Fatalf("a service token minted a personal token: %v", err)
	}
	// A token presented as a session cookie is not a session.
	jar := e.browser(t)
	if _, code := jar.actor(t, http.MethodGet, map[string]string{"Cookie": auth.SessionCookie + "=" + raw}); code != http.StatusUnauthorized {
		t.Fatalf("a token in the session cookie authenticated: %d", code)
	}
	// A malformed Authorization header never falls back to the cookie.
	if _, code := b.actor(t, http.MethodGet, map[string]string{"Authorization": "Basic b3duZXI6cHc="}); code != http.StatusUnauthorized {
		t.Fatalf("a non-bearer Authorization fell back to the cookie: %d", code)
	}
	e.clock.Advance(auth.DefaultServiceTokenTTL)
	if _, code := e.browser(t).actor(t, http.MethodGet, bearer(raw)); code != http.StatusUnauthorized {
		t.Fatalf("an expired service token authenticates: %d", code)
	}
}

func TestTokenValuesAreNeverPrintedOrEchoed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	issued, err := e.svc.IssueServiceToken(auth.ServiceTokenRequest{ActorID: "service:publisher", Kind: core.ActorService, Scopes: []string{"releases:write"}})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(issued.Token.Reveal())
	if !strings.HasPrefix(raw, "sisf_svc_") {
		t.Fatalf("service tokens carry a recognizable prefix")
	}
	encoded, err := json.Marshal(issued)
	if err != nil {
		t.Fatal(err)
	}
	printed := fmt.Sprintf("%v %+v %#v %s", issued, issued, issued, encoded)
	if strings.Contains(printed, raw) || strings.Contains(printed, raw[len("sisf_svc_"):]) {
		t.Fatal("the token leaked through formatting or JSON")
	}
	forged := raw + "x"
	resp := e.browser(t).do(t, http.MethodGet, "/api/whoami", "", bearer(forged))
	if body := readAll(t, resp.Body); resp.StatusCode != http.StatusUnauthorized || strings.Contains(body, forged) || strings.Contains(body, raw) {
		t.Fatalf("a rejected token was echoed: %d %s", resp.StatusCode, body)
	}
	if _, err := e.svc.Authenticate(mustRequest(t, forged)); err == nil || strings.Contains(err.Error(), forged) {
		t.Fatalf("the error names the token: %v", err)
	}
}

func mustRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://factory.test/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

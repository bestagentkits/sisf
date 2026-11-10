package auth_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/testkit/fakeoidc"
	"github.com/bestagentkits/sisf/pkg/core"
)

func TestStepUpFailsWithoutARealReauthenticationOrByAnotherPerson(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	b.signIn(t, "google", "owner-sub")
	view, _ := b.session(t)
	csrf := map[string]string{auth.CSRFHeader: view.CSRFToken}
	stepUp := func(subject string) int {
		_, providerURL := b.hop(t, http.MethodPost, "/auth/step-up/google", csrf)
		e.clock.Advance(time.Minute)
		resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, subject, providerURL), nil)
		return resp.StatusCode
	}
	// The provider answers from its earlier sign-in instead of asking again.
	e.fake.SetFaults(fakeoidc.Faults{IgnoreReauth: true})
	if status := stepUpWithoutReauth(t, e, b, csrf); status != http.StatusUnauthorized {
		t.Fatalf("a step-up without a fresh sign-in: %d", status)
	}
	e.fake.SetFaults(fakeoidc.Faults{})
	if status := stepUp("mallory-sub"); status != http.StatusUnauthorized {
		t.Fatalf("another person completed the Owner's step-up: %d", status)
	}
	if status, _, _ := mint(t, b, csrf, `{"scopes":["prs:write"]}`); status != http.StatusForbidden {
		t.Fatalf("a failed step-up was recorded: minting answered %d", status)
	}
	token, err := e.svc.IssueServiceToken(auth.ServiceTokenRequest{ActorID: "agent:coder", Kind: core.ActorAgent, Scopes: []string{"runs:write"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp := b.do(t, http.MethodPost, "/auth/step-up/google", "", map[string]string{"Authorization": "Bearer " + string(token.Token.Reveal())}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a token started a step-up: %d", resp.StatusCode)
	}
}

// stepUpWithoutReauth runs a step-up, started a minute after the original
// sign-in, through a provider that does not record a new sign-in.
func stepUpWithoutReauth(t *testing.T, e *env, b *browser, csrf map[string]string) int {
	t.Helper()
	e.clock.Advance(time.Minute)
	_, providerURL := b.hop(t, http.MethodPost, "/auth/step-up/google", csrf)
	resp, loc := b.hop(t, http.MethodGet, providerURL, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("provider: %d", resp.StatusCode)
	}
	resp, _ = b.hop(t, http.MethodGet, loc, nil)
	return resp.StatusCode
}

package auth_test

import (
	"net/http"
	"strings"
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
		_, providerURL := b.hop(t, http.MethodPost, "/auth/step-up/google"+stepUpQuery(mintRequest), csrf)
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
	if status, _, _ := mint(t, b, view.CSRFToken, patForm("", 0, "prs:write"), nil); status != http.StatusForbidden {
		t.Fatalf("a failed step-up was recorded: minting answered %d", status)
	}
	token, err := e.svc.IssueServiceToken(auth.ServiceTokenRequest{ActorID: "agent:coder", Kind: core.ActorAgent, Scopes: []string{"runs:write"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp := b.do(t, http.MethodPost, "/auth/step-up/google"+stepUpQuery(mintRequest), "", map[string]string{"Authorization": "Bearer " + string(token.Token.Reveal())}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a token started a step-up: %d", resp.StatusCode)
	}
}

// mintRequest is what the step-ups in these tests confirm: minting the
// token patForm("", 0, "prs:write") describes.
var mintRequest = auth.PATRequest{Scopes: []string{"prs:write"}}.StepUpTarget()

// stepUpWithoutReauth runs a step-up, started a minute after the original
// sign-in, through a provider that does not record a new sign-in.
func stepUpWithoutReauth(t *testing.T, e *env, b *browser, csrf map[string]string) int {
	t.Helper()
	e.clock.Advance(time.Minute)
	_, providerURL := b.hop(t, http.MethodPost, "/auth/step-up/google"+stepUpQuery(mintRequest), csrf)
	resp, loc := b.hop(t, http.MethodGet, providerURL, nil)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("provider: %d", resp.StatusCode)
	}
	resp, _ = b.hop(t, http.MethodGet, loc, nil)
	return resp.StatusCode
}

// GitHub OAuth cannot be told to sign the person in again, so a step-up
// through it would only replay the provider's own session. It is refused
// with a pointer to a provider that can, and signing in with it still works.
func TestAStepUpThroughAProviderThatCannotForceASignInIsRefused(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	if resp := b.signIn(t, "github", "owner-sub"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a GitHub sign-in: %d", resp.StatusCode)
	}
	view, _ := b.session(t)
	resp := b.do(t, http.MethodPost, "/auth/step-up/github"+stepUpQuery(mintRequest), "", map[string]string{auth.CSRFHeader: view.CSRFToken})
	body := readAll(t, resp.Body)
	if resp.StatusCode != http.StatusNotImplemented || resp.Header.Get("Location") != "" ||
		!strings.Contains(body, string(core.CodeNotSupported)) || !strings.Contains(body, "google") {
		t.Fatalf("a step-up through GitHub: %d %s", resp.StatusCode, body)
	}
}

// The session view names the providers that can confirm the person signed
// in: a provider that can force a fresh sign-in of the session's issuer.
func TestTheSessionViewNamesOnlyProvidersThatCanConfirmThePerson(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for provider, want := range map[string]string{"google": `"step_up_providers":["google"]`, "github": `"step_up_providers":[]`} {
		b := e.browser(t)
		b.signIn(t, provider, "owner-sub")
		if body := readAll(t, b.do(t, http.MethodGet, "/auth/session", "", nil).Body); !strings.Contains(body, want) {
			t.Errorf("signed in with %s, the session view is %s", provider, body)
		}
	}
}

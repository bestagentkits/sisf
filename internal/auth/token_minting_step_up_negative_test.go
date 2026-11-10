package auth_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/pkg/core"
)

// A confirmation to mint a token names that token's exact name, scopes and
// lifetime: it mints no other token, is spent by no other call, and mints
// its token once.
func TestAStepUpMintsOnlyTheTokenItNames(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	csrf := signedIn(t, b, "owner-sub")
	view, _ := b.session(t)
	confirmed := patForm("cli", 3600, "prs:write", "runs:read")
	confirmToken(t, b, "owner-sub", csrf, confirmed)

	for name, form := range map[string]map[string][]string{
		"wider scopes":     patForm("cli", 3600, "prs:write", "runs:read", "policy:write"),
		"other scopes":     patForm("cli", 3600, "policy:write"),
		"longer lifetime":  patForm("cli", 7200, "prs:write", "runs:read"),
		"default lifetime": patForm("cli", 0, "prs:write", "runs:read"),
		"another name":     patForm("ci", 3600, "prs:write", "runs:read"),
	} {
		if status, _, _ := mint(t, b, csrf, form, nil); status != http.StatusForbidden {
			t.Errorf("%s: minted with another token's confirmation: %d", name, status)
		}
	}
	target := auth.PATRequest{Name: "cli", Scopes: []string{"runs:read", "prs:write"}, TTL: 3600 * 1e9}.StepUpTarget()
	if _, err := e.svc.ConsumeStepUp(context.Background(), view.SessionID, target.Action, target.Target); !core.HasCode(err, core.CodeStepUpRequired) {
		t.Fatalf("an action call spent a confirmation to mint a token: %v", err)
	}
	// The scope order is not part of what was confirmed.
	reordered := patForm("cli", 3600, "runs:read", "prs:write")
	if status, _, _ := mint(t, b, csrf, reordered, nil); status != http.StatusCreated {
		t.Fatalf("the confirmed token: %d", status)
	}
	if status, _, _ := mint(t, b, csrf, confirmed, nil); status != http.StatusForbidden {
		t.Fatalf("one confirmation minted two tokens: %d", status)
	}
}

// Minting spends a step-up, so only the person's own form post may do it: a
// script's fetch, with the session cookie and the CSRF token, is refused,
// and the refusal does not spend the confirmation.
func TestATokenIsMintedOnlyFromANavigatedFormPost(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	csrf := signedIn(t, b, "owner-sub")
	form := patForm("", 0, "prs:write")
	confirmToken(t, b, "owner-sub", csrf, form)

	if resp := b.do(t, http.MethodPost, "/auth/tokens", `{"scopes":["prs:write"]}`,
		map[string]string{auth.CSRFHeader: csrf, "Content-Type": "application/json"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a JSON body minted a token: %d", resp.StatusCode)
	}
	for _, mode := range []string{"cors", "same-origin", "no-cors"} {
		if status, _, _ := mint(t, b, csrf, form, map[string]string{"Sec-Fetch-Mode": mode}); status != http.StatusForbidden {
			t.Errorf("a fetch in mode %s minted a token: %d", mode, status)
		}
	}
	if status, _, _ := mint(t, b, csrf, form, nil); status != http.StatusCreated {
		t.Fatalf("the person's own form post after refused fetches: %d", status)
	}
}

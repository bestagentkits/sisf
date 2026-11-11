package auth_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
)

// flood starts sign-ins nobody finishes, perSource from each of sources
// network addresses, as an unauthenticated caller can.
func flood(t *testing.T, e *env, sources, perSource int) {
	t.Helper()
	handler := e.svc.Handler()
	for s := range sources {
		for range perSource {
			req := httptest.NewRequest(http.MethodGet, "/auth/login/google?return_to=/", nil)
			req.RemoteAddr = fmt.Sprintf("203.0.%d.%d:4000", s/250, s%250+1)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusFound {
				t.Fatalf("an unfinished sign-in from source %d: %d", s, rec.Code)
			}
		}
	}
}

// Sign-ins are open to anyone, so one address starting thousands of them
// must not push out the sign-in a person on another address is finishing.
func TestASignInFloodFromOneAddressDoesNotEvictAnotherAddressesSignIn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	e.clock.Advance(time.Second) // the person's sign-in is the oldest open one
	flood(t, e, 1, 1100)
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the Owner's sign-in after a flood from one address: %d, want it completed", resp.StatusCode)
	}
}

// Spreading the flood over many addresses does not help either: when every
// open sign-in is taken, the ones dropped come from the addresses holding
// the most, not from a person with one.
func TestASignInFloodFromManyAddressesDoesNotEvictAPersonsSignIn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	e.clock.Advance(time.Second) // the person's sign-in is the oldest open one
	flood(t, e, 70, 20)
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the Owner's sign-in after a flood from many addresses: %d, want it completed", resp.StatusCode)
	}
}

// A step-up comes from a signed-in session; unauthenticated sign-ins never
// push it out, however many addresses they come from.
func TestAStepUpOutlastsAFloodOfUnauthenticatedSignIns(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := e.browser(t)
	csrf := signedIn(t, b, "owner-sub")
	target := auth.PATRequest{Name: "cli", Scopes: []string{"prs:write"}}.StepUpTarget()
	_, providerURL := b.hop(t, http.MethodPost, "/auth/step-up/google"+stepUpQuery(target), map[string]string{auth.CSRFHeader: csrf})
	if providerURL == "" {
		t.Fatal("the step-up did not start")
	}
	e.clock.Advance(time.Second) // the step-up is the oldest open flow
	flood(t, e, 1100, 1)
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the Owner's step-up after a flood: %d, want it completed", resp.StatusCode)
	}
}

// Any account at the provider can sign in, so role-less sign-ins filling
// every session slot must not sign out a person who holds a role.
func TestSessionsOfAPersonWithARoleOutlastRoleLessSignIns(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(e *env, cfg *auth.Config) {
		cfg.MaxSessions = 4
		owner := e.actorID("owner-sub")
		cfg.HoldsRole = func(actorID string) bool { return actorID == owner }
	})
	owner := e.browser(t)
	owner.signIn(t, "google", "owner-sub")
	e.clock.Advance(time.Second) // the Owner's session is the closest to expiry
	for range 12 {
		e.browser(t).signIn(t, "google", "mallory-sub")
		e.browser(t).signIn(t, "google", "alice-sub")
	}
	if _, ok := owner.session(t); !ok {
		t.Fatal("role-less sign-ins signed the Owner out")
	}
}

// Among people without a role, the one holding the most sessions loses one
// first, so one account signing in again and again cannot sign others out.
func TestOneAccountSigningInRepeatedlyCannotSignOthersOut(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(_ *env, cfg *auth.Config) { cfg.MaxSessions = 4 })
	alice := e.browser(t)
	alice.signIn(t, "google", "alice-sub")
	e.clock.Advance(time.Second) // alice's session is the closest to expiry
	for range 12 {
		e.browser(t).signIn(t, "google", "mallory-sub")
	}
	if _, ok := alice.session(t); !ok {
		t.Fatal("one account's repeated sign-ins signed another person out")
	}
}

// The identity kept for each signed-in actor is forgotten once the actor
// holds no session and no token, so sign-ins of ever new accounts cannot
// grow it without bound; a live token keeps it.
func TestAnIdentityIsForgottenWithItsLastSessionAndToken(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.browser(t)
	alice.signIn(t, "google", "alice-sub")
	owner := e.browser(t)
	csrf := signedIn(t, owner, "owner-sub")
	form := patForm("cli", 0, "prs:write")
	confirmToken(t, owner, "owner-sub", csrf, form)
	if status, _, _ := mint(t, owner, csrf, form, nil); status != http.StatusCreated {
		t.Fatalf("mint: %d", status)
	}
	for _, b := range []*browser{alice, owner} {
		view, _ := b.session(t)
		if err := e.svc.RevokeSession(view.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	e.browser(t).signIn(t, "google", "mallory-sub")
	if _, ok := e.svc.Identity(e.actorID("alice-sub")); ok {
		t.Error("the identity of an actor with no session or token is still kept")
	}
	if _, ok := e.svc.Identity(e.actorID("owner-sub")); !ok {
		t.Error("the identity of an actor with a live token was forgotten")
	}
}

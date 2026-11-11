package auth_test

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/auth"
	"github.com/bestagentkits/sisf/internal/control"
)

// flood starts sign-ins nobody finishes, perSource from each of sources
// network addresses, as an unauthenticated caller can, and returns how many
// were refused because every slot was taken.
func flood(t *testing.T, e *env, sources, perSource int) int {
	t.Helper()
	return floodFrom(t, e, sources, perSource, func(s int) string { return fmt.Sprintf("203.0.%d.%d:4000", s/250, s%250+1) })
}

// floodFrom is flood with the address of source s given by address.
func floodFrom(t *testing.T, e *env, sources, perSource int, address func(s int) string) int {
	t.Helper()
	handler := e.svc.Handler()
	refused := 0
	for s := range sources {
		for range perSource {
			req := httptest.NewRequest(http.MethodGet, "/auth/login/google?return_to=/", nil)
			req.RemoteAddr = address(s)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			switch rec.Code {
			case http.StatusFound:
			case http.StatusTooManyRequests:
				refused++
			default:
				t.Fatalf("an unfinished sign-in from source %d: %d", s, rec.Code)
			}
		}
	}
	return refused
}

// Sign-ins are open to anyone, so one address starting thousands of them
// must not push out the sign-in a person on another address is finishing.
func TestASignInFloodFromOneAddressDoesNotEvictAnotherAddressesSignIn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	e.clock.Advance(time.Second) // the person's sign-in is the oldest open one
	if refused := flood(t, e, 1, 1100); refused != 0 {
		t.Fatalf("%d sign-ins from one address were refused, want its own oldest pushed out instead", refused)
	}
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
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	e.clock.Advance(time.Second) // the person's sign-in is the oldest open one
	if refused := flood(t, e, 70, 20); refused != 0 {
		t.Fatalf("%d sign-ins from addresses holding many were refused, want their own oldest pushed out instead", refused)
	}
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the Owner's sign-in after a flood from many addresses: %d, want it completed", resp.StatusCode)
	}
}

// A flood of one sign-in from each of ever new addresses holds one flow per
// source, so no source is busier than the person's. A newcomer is still
// admitted, so the flood locks nobody out: it pushes out a flow chosen at
// random, which the flood cannot aim at the person's. Here the choice is
// seeded, so the outcome repeats.
func TestAFloodOfOneSignInPerAddressIsAdmittedAndMissesAPersonsSignIn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
	rng := rand.New(rand.NewPCG(7, 7))
	auth.SetEvictionPick(e.svc, func(n int) (int, error) { return rng.IntN(n), nil })
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	e.clock.Advance(time.Second) // the person's sign-in is the oldest open one
	if refused := flood(t, e, 1100, 1); refused != 0 {
		t.Fatalf("%d sign-ins were refused because every slot held one flow per address, want each admitted", refused)
	}
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the Owner's sign-in after a flood of one sign-in per address: %d, want it completed", resp.StatusCode)
	}
}

// Once a flood's flows lapse, its slots are free again and a sign-in is
// accepted without pushing anything out.
func TestSignInsAreAcceptedAgainOnceAFloodsFlowsLapse(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
	flood(t, e, 1100, 1)
	e.clock.Advance(time.Hour)
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a sign-in after the flood lapsed: %d, want it completed", resp.StatusCode)
	}
}

// One IPv6 host holds a whole /64, so a flood rotating addresses inside it
// counts as one source and pushes out only its own sign-ins.
func TestAnIPv6FloodInsideOneSlash64CountsAsOneSource(t *testing.T) {
	t.Parallel()
	e := newEnv(t, func(_ *env, cfg *auth.Config) { cfg.ClientKey = control.ClientAddress(nil) })
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	e.clock.Advance(time.Second) // the person's sign-in is the oldest open one
	refused := floodFrom(t, e, 1100, 1, func(s int) string { return fmt.Sprintf("[2001:db8:1:2::%x]:4000", s+1) })
	if refused != 0 {
		t.Fatalf("%d sign-ins from one /64 were refused, want its own oldest pushed out instead", refused)
	}
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the Owner's sign-in after a flood from one /64: %d, want it completed", resp.StatusCode)
	}
}

// ownerHoldsARole names the Owner as holding a role, as the wiring does for
// the Owners named at install.
func ownerHoldsARole(e *env, cfg *auth.Config) {
	owner := e.actorID("owner-sub")
	cfg.HoldsRole = func(actorID string) bool { return actorID == owner }
}

// A step-up of a person who holds a role is never pushed out by
// unauthenticated sign-ins, however many addresses they come from.
func TestAStepUpOutlastsAFloodOfUnauthenticatedSignIns(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ownerHoldsARole)
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
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

// A person who holds a role can still start a step-up while a flood of one
// sign-in per address has every slot taken.
func TestAStepUpStartsWhileAFloodOfOneSignInPerAddressFillsEverySlot(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ownerHoldsARole)
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
	b := e.browser(t)
	csrf := signedIn(t, b, "owner-sub")
	if refused := flood(t, e, 1100, 1); refused != 0 {
		t.Fatalf("%d sign-ins were refused because every slot held one flow per address, want each admitted", refused)
	}
	target := auth.PATRequest{Name: "cli", Scopes: []string{"prs:write"}}.StepUpTarget()
	_, providerURL := b.hop(t, http.MethodPost, "/auth/step-up/google"+stepUpQuery(target), map[string]string{auth.CSRFHeader: csrf})
	if providerURL == "" {
		t.Fatal("the Owner's step-up did not start while the flood filled every slot")
	}
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the Owner's step-up started during a flood: %d, want it completed", resp.StatusCode)
	}
}

// Any account can sign in and start step-ups, so a role-less account
// starting them by the thousand pushes out neither a person's sign-in nor
// the Owner's step-up, even from the same address.
func TestStepUpsOfARoleLessAccountPushOutNobodyElse(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ownerHoldsARole)
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
	owner := e.browser(t)
	csrf := signedIn(t, owner, "owner-sub")
	target := auth.PATRequest{Name: "cli", Scopes: []string{"prs:write"}}.StepUpTarget()
	_, ownerStepUp := owner.hop(t, http.MethodPost, "/auth/step-up/google"+stepUpQuery(target), map[string]string{auth.CSRFHeader: csrf})
	person := e.browser(t)
	signIn := person.toProvider(t, "google")
	e.clock.Advance(time.Second) // the person's flows are the oldest open ones
	mallory := e.browser(t)
	malloryCSRF := signedIn(t, mallory, "mallory-sub")
	for range 1100 {
		mallory.hop(t, http.MethodPost, "/auth/step-up/google"+stepUpQuery(target), map[string]string{auth.CSRFHeader: malloryCSRF})
	}
	if resp, _ := person.hop(t, http.MethodGet, person.atProvider(t, "alice-sub", signIn), nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("a sign-in after a role-less account's step-ups: %d, want it completed", resp.StatusCode)
	}
	if resp, _ := owner.hop(t, http.MethodGet, owner.atProvider(t, "owner-sub", ownerStepUp), nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("the Owner's step-up after a role-less account's step-ups: %d, want it completed", resp.StatusCode)
	}
}

// A flood from the address a person shares, as behind a proxy that forwards
// no client address, reaches the person's sign-in only once it has started
// as many as every slot holds, as it did before sources were told apart.
func TestASignInFloodSharingThePersonsAddressTakesEverySlotToEvictIt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
	b := e.browser(t)
	providerURL := b.toProvider(t, "google")
	e.clock.Advance(time.Second)
	handler := e.svc.Handler()
	for range 1000 {
		req := httptest.NewRequest(http.MethodGet, "/auth/login/google?return_to=/", nil)
		req.RemoteAddr = "127.0.0.1:4000"
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	if resp, _ := b.hop(t, http.MethodGet, b.atProvider(t, "owner-sub", providerURL), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a sign-in after fewer flows than slots from its own address: %d, want it completed", resp.StatusCode)
	}
}

// Any account at the provider can sign in, so role-less sign-ins filling
// every session slot must not sign out a person who holds a role.
func TestSessionsOfAPersonWithARoleOutlastRoleLessSignIns(t *testing.T) {
	t.Parallel()
	e := newEnv(t, ownerHoldsARole, func(_ *env, cfg *auth.Config) { cfg.MaxSessions = 4 })
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
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
	auth.LiftStartLimits(e.svc) // this test is about the table behind the start limits
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

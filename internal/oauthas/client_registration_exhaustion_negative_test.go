package oauthas_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/oauthas"
	"github.com/bestagentkits/sisf/internal/oauthas/oauthastest"
)

// sourceHeader names the network source of a test registration, so one
// process can play many addresses.
const sourceHeader = "X-Test-Source"

// registrationLimits keys clients on sourceHeader and holds at most max
// registered clients.
func registrationLimits(maxClients int) func(*oauthas.Config) {
	return func(cfg *oauthas.Config) {
		cfg.MaxClients = maxClients
		cfg.ClientKey = func(r *http.Request) string { return r.Header.Get(sourceHeader) }
	}
}

// registerFrom registers a client from source and returns the status and
// the client ID.
func registerFrom(t *testing.T, e *oauthastest.Env, source string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.Base+oauthas.RegisterPath,
		bytes.NewBufferString(`{"client_name":"agent","redirect_uris":["`+e.RedirectURI+`"]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(sourceHeader, source)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.ClientID
}

// approveFor runs a signed-in authorization of clientID by the Owner and
// returns the code.
func approveFor(t *testing.T, e *oauthastest.Env, clientID string) string {
	t.Helper()
	b := signedIn(t, e, "owner-sub")
	q := e.AuthorizeQuery(oauthastest.NewPKCE(t))
	q.Set("client_id", clientID)
	return oauthastest.Code(t, b.Decide(t, authorize(t, e, b, q), "approve"))
}

// registered reports whether the authorization endpoint knows clientID.
func registered(t *testing.T, e *oauthastest.Env, clientID string) bool {
	t.Helper()
	b := signedIn(t, e, "owner-sub")
	q := e.AuthorizeQuery(oauthastest.NewPKCE(t))
	q.Set("client_id", clientID)
	resp := authorize(t, e, b, q)
	return resp.StatusCode != http.StatusBadRequest || !bytes.Contains([]byte(readBody(t, resp)), []byte("not registered"))
}

// Registration needs no credentials, so clients nobody uses, registered
// from one address or from many, must not keep a person's agent from
// registering.
func TestUnusedRegistrationsDoNotKeepAClientFromRegistering(t *testing.T) {
	t.Parallel()
	for name, flood := range map[string]struct{ sources, each int }{
		"from one address":    {1, 40},
		"from many addresses": {8, 10},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, registrationLimits(20))
			for s := range flood.sources {
				for range flood.each {
					registerFrom(t, e, fmt.Sprintf("203.0.113.%d", s))
				}
			}
			status, id := registerFrom(t, e, "192.0.2.10")
			if status != http.StatusCreated {
				t.Fatalf("a client's registration after a flood: %d, want it registered", status)
			}
			approveFor(t, e, id)
		})
	}
}

// A registered client nobody has used expires; one a person consented to
// lives until it has gone unused for a long while.
func TestRegisteredClientsExpireUnlessUsed(t *testing.T) {
	t.Parallel()
	e := newEnv(t, registrationLimits(20))
	_, unused := registerFrom(t, e, "203.0.113.1")
	_, used := registerFrom(t, e, "192.0.2.10")
	approveFor(t, e, used)
	e.Clock.Advance(2 * time.Hour)
	if registered(t, e, unused) {
		t.Error("a client nobody used is still registered hours later")
	}
	if !registered(t, e, used) {
		t.Fatal("a client a person consented to expired within hours")
	}
	e.Clock.Advance(40 * 24 * time.Hour)
	if registered(t, e, used) {
		t.Error("a client unused for over a month is still registered")
	}
}

// When every slot is taken, a client a person has consented to is never
// dropped for a new registration; the unused ones of the busiest source go.
func TestAClientInUseOutlastsARegistrationFlood(t *testing.T) {
	t.Parallel()
	e := newEnv(t, registrationLimits(20))
	_, used := registerFrom(t, e, "192.0.2.10")
	approveFor(t, e, used)
	for s := range 8 {
		for range 10 {
			registerFrom(t, e, fmt.Sprintf("203.0.113.%d", s))
		}
	}
	if !registered(t, e, used) {
		t.Fatal("a registration flood dropped a client in use")
	}
}

// A flood of one registration from each of ever new addresses holds one
// client per source, so no source is busier than the person's. A newcomer
// then never drops an older unused client of another source: it is refused
// until clients lapse, and the person's client stays registered.
func TestAFloodOfOneRegistrationPerAddressDoesNotDropAPersonsClient(t *testing.T) {
	t.Parallel()
	e := newEnv(t, registrationLimits(20))
	_, id := registerFrom(t, e, "192.0.2.10")
	e.Clock.Advance(time.Second) // the person's client is the oldest one
	refused := 0
	for s := range 30 {
		switch status, _ := registerFrom(t, e, fmt.Sprintf("203.0.113.%d", s)); status {
		case http.StatusCreated:
		case http.StatusServiceUnavailable:
			refused++
		default:
			t.Fatalf("a registration from source %d: %d", s, status)
		}
	}
	if refused == 0 {
		t.Fatal("more registrations than slots were all accepted, so some client was dropped")
	}
	if !registered(t, e, id) {
		t.Fatal("a flood of one registration per address dropped a person's client")
	}
	e.Clock.Advance(2 * time.Hour)
	if status, _ := registerFrom(t, e, "192.0.2.11"); status != http.StatusCreated {
		t.Fatalf("a registration once the flood's clients lapsed: %d, want it registered", status)
	}
}

// A client counts as in use only through the consent of a person who holds
// a scope: a signed-in account with no role reaches neither consent nor a
// code, so it cannot pin registered clients against a flood.
func TestAnAccountWithoutARoleCannotPinRegisteredClients(t *testing.T) {
	t.Parallel()
	e := newEnv(t, registrationLimits(20))
	nobody := signedIn(t, e, "nobody-sub")
	for range 20 {
		_, id := registerFrom(t, e, "203.0.113.1")
		q := e.AuthorizeQuery(oauthastest.NewPKCE(t))
		q.Set("client_id", id)
		if resp := authorize(t, e, nobody, q); resp.StatusCode == http.StatusOK {
			t.Fatal("an account without a role reached the consent page")
		}
	}
	if status, _ := registerFrom(t, e, "192.0.2.10"); status != http.StatusCreated {
		t.Fatalf("a registration after a role-less account tried every client: %d, want it registered", status)
	}
}

// A flood from the address a person shares, as behind a proxy that forwards
// no client address, reaches the person's unused client only once it has
// registered as many as there are slots.
func TestARegistrationFloodSharingThePersonsAddressTakesEverySlotToDropTheirClient(t *testing.T) {
	t.Parallel()
	e := newEnv(t, registrationLimits(20))
	_, id := registerFrom(t, e, "192.0.2.10")
	for range 15 {
		registerFrom(t, e, "192.0.2.10")
	}
	if !registered(t, e, id) {
		t.Fatal("a client was dropped after fewer registrations than slots from its own address")
	}
}

package api_test

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/control/api"
)

// sourceHeader names the network source of a test request; the tests key
// clients on it so one process can play many addresses.
const sourceHeader = "X-Test-Source"

func keyedBySourceHeader(cfg *api.Config) {
	cfg.ClientKey = func(r *http.Request) string { return r.Header.Get(sourceHeader) }
}

// startFrom opens a login request from source and returns the status and
// the request.
func startFrom(t *testing.T, b *browser, source string) (int, started) {
	t.Helper()
	r := b.do(t, http.MethodPost, api.DeviceStartPath, `{"name":"cli"}`,
		map[string]string{"Content-Type": "application/json", sourceHeader: source})
	var s started
	if r.status == http.StatusOK {
		r.json(t, &s)
	}
	return r.status, s
}

// Starting a login needs no credentials, so open requests nobody approves,
// from one address or from many, must not keep a person from starting
// theirs.
func TestOpenLoginRequestsDoNotKeepAPersonFromStartingOne(t *testing.T) {
	t.Parallel()
	for name, flood := range map[string]struct{ sources, each int }{
		"from one address":    {1, 1100},
		"from many addresses": {120, 10},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, sampleRegistry, keyedBySourceHeader)
			attacker := e.browser(t)
			for s := range flood.sources {
				for range flood.each {
					startFrom(t, attacker, fmt.Sprintf("203.0.113.%d", s))
				}
			}
			cli := e.browser(t)
			status, s := startFrom(t, cli, "192.0.2.10")
			if status != http.StatusOK {
				t.Fatalf("the Owner's login request after a flood: %d, want it opened", status)
			}
			person, csrf := e.owner(t)
			person.confirmApproval(t, "owner-sub", csrf, s.UserCode)
			if r := person.approve(t, s.UserCode, csrf); r.status != http.StatusOK {
				t.Fatalf("approve: %d %s", r.status, r.body)
			}
			e.clock.Advance(time.Duration(s.Interval) * time.Second)
			if r, p := cli.poll(t, s.DeviceCode); r.status != http.StatusOK || p.Token == "" {
				t.Fatalf("collect: %d", r.status)
			}
		})
	}
}

// When every slot is taken, the request dropped comes from the address
// holding the most, so a flood does not push out a person's open request,
// and a request already approved is never dropped before it is collected.
func TestAFloodDropsItsOwnLoginRequestsBeforeAPersons(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry, keyedBySourceHeader)
	cli, collector := e.browser(t), e.browser(t)
	_, open := startFrom(t, cli, "192.0.2.10")
	_, approved := startFrom(t, collector, "192.0.2.11")
	person, csrf := e.owner(t)
	person.confirmApproval(t, "owner-sub", csrf, approved.UserCode)
	if r := person.approve(t, approved.UserCode, csrf); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	e.clock.Advance(time.Second) // the person's requests are the oldest open ones
	attacker := e.browser(t)
	for s := range 110 {
		for range 10 {
			startFrom(t, attacker, fmt.Sprintf("203.0.113.%d", s))
		}
	}
	e.clock.Advance(time.Duration(open.Interval) * time.Second)
	if r, p := cli.poll(t, open.DeviceCode); r.status != http.StatusAccepted || p.Status != "pending" {
		t.Fatalf("the person's open request after a flood: %d %s, want it still pending", r.status, r.body)
	}
	if r, p := collector.poll(t, approved.DeviceCode); r.status != http.StatusOK || p.Token == "" {
		t.Fatalf("the approved request after a flood: %d, want its token collected", r.status)
	}
}

// A flood of one start from each of ever new addresses holds one request
// per source, so no source is busier than the person's. A newcomer is
// still admitted, so the flood locks nobody out: it drops a waiting request
// chosen at random, which the flood cannot aim at the person's. Here the
// choice is seeded, so the outcome repeats.
func TestAFloodOfOneLoginRequestPerAddressIsAdmittedAndMissesAPersonsRequest(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(7, 7))
	e := newEnvWithEvictionPick(t, sampleRegistry, func(n int) (int, error) { return rng.IntN(n), nil }, keyedBySourceHeader)
	cli := e.browser(t)
	_, open := startFrom(t, cli, "192.0.2.10")
	e.clock.Advance(time.Second) // the person's request is the oldest open one
	attacker := e.browser(t)
	refused := 0
	for s := range 1100 {
		switch status, _ := startFrom(t, attacker, fmt.Sprintf("203.0.%d.%d", s/250, s%250+1)); status {
		case http.StatusOK:
		case http.StatusTooManyRequests:
			refused++
		default:
			t.Fatalf("a start from source %d: %d", s, status)
		}
	}
	if refused != 0 {
		t.Fatalf("%d starts were refused because every slot held one request per address, want each admitted", refused)
	}
	e.clock.Advance(time.Duration(open.Interval) * time.Second)
	if r, p := cli.poll(t, open.DeviceCode); r.status != http.StatusAccepted || p.Status != "pending" {
		t.Fatalf("the person's open request after a flood of one start per address: %d %s, want it still pending", r.status, r.body)
	}
}

// A flood from the address a person shares, as behind a proxy that forwards
// no client address, reaches the person's waiting request only once it has
// started as many as there are slots.
func TestALoginFloodSharingThePersonsAddressTakesEverySlotToDropTheirRequest(t *testing.T) {
	t.Parallel()
	e := newEnv(t, sampleRegistry, keyedBySourceHeader)
	cli := e.browser(t)
	_, open := startFrom(t, cli, "192.0.2.10")
	attacker := e.browser(t)
	for range 1000 {
		startFrom(t, attacker, "192.0.2.10")
	}
	e.clock.Advance(time.Duration(open.Interval) * time.Second)
	if r, p := cli.poll(t, open.DeviceCode); r.status != http.StatusAccepted || p.Status != "pending" {
		t.Fatalf("a request after fewer starts than slots from its own address: %d %s, want it still pending", r.status, r.body)
	}
}

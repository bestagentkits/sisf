package api_test

import (
	"fmt"
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
		"from one address":    {1, 300},
		"from many addresses": {60, 6},
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
	for s := range 40 {
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

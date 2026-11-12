package oauthas

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// Sources each registering clients at their own full rate drop only their
// own clients, or none while the table has room, so they never spend the
// allowance of random evictions: nine sources at ten registrations a
// minute for ten minutes leave a tenth source's registration admitted
// every minute. Nor does a table full of unused clients of sources holding
// several: a registration from each of more new sources than the eviction
// allowance drops the busiest source's clients and is admitted.
func TestBusySourcesWithinTheirRatesDoNotLockOutARegistration(t *testing.T) {
	t.Parallel()
	clock := &stillClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := clientTable(clock, DefaultMaxClients, rand.New(rand.NewPCG(57, 58)))
	for minute := range 10 {
		for source := range 9 {
			for n := range report.StartsPerSource {
				if id, wait := s.registerThroughLimits(fmt.Sprint("busy-", source)); id == "" {
					t.Fatalf("minute %d: registration %d of source %d refused, wait %s", minute, n, source, wait)
				}
			}
		}
		if id, wait := s.registerThroughLimits(fmt.Sprint("person-", minute)); id == "" {
			t.Fatalf("minute %d: a person's registration after nine sources registered at their rates was refused, wait %s", minute, wait)
		}
		clock.now = clock.now.Add(report.StartWindow)
	}
	s.mu.Lock()
	for i := 0; len(s.dynamic) < s.cfg.MaxClients; i++ {
		s.clientSeq++
		id := fmt.Sprint("crowd-", s.clientSeq)
		s.clients[id] = Client{ID: id, Dynamic: true}
		s.dynamic[id] = &clientUse{source: fmt.Sprint("crowd-", i%4), seq: s.clientSeq, registered: clock.now}
	}
	s.mu.Unlock()
	for i := range 2 * report.FloodSafeRate(DefaultMaxClients, report.StartSurvival) {
		if id, wait := s.registerThroughLimits(fmt.Sprint("newcomer-", i)); id == "" {
			t.Fatalf("newcomer %d into a table full of sources holding several was refused, wait %s", i, wait)
		}
	}
}

// Once a table full of one unused client per source has had its random
// evictions for the window, a registration from yet another source is
// answered 429 with the time to retry, and the table is left as it was.
func TestARegistrationOverTheEvictionRateIsRefusedWithATimeToRetry(t *testing.T) {
	t.Parallel()
	clock := &stillClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := clientTable(clock, DefaultMaxClients, rand.New(rand.NewPCG(59, 60)))
	register := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"redirect_uris":["http://127.0.0.1:9000/cb"]}`))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		s.handleRegister(rec, req)
		return rec
	}
	s.topUpClients()
	for i := range report.FloodSafeRate(DefaultMaxClients, report.StartSurvival) {
		if rec := register(fmt.Sprintf("203.0.%d.%d:4000", i/250, i%250+1)); rec.Code != http.StatusCreated {
			t.Fatalf("registration %d within the eviction rate: %d %s", i, rec.Code, rec.Body)
		}
	}
	s.mu.Lock()
	before := maps.Clone(s.dynamic)
	s.mu.Unlock()
	rec := register("198.51.100.7:4000")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("a registration over the eviction rate: %d, Retry-After %q; want 429 with a time to retry", rec.Code, rec.Header().Get("Retry-After"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !maps.Equal(before, s.dynamic) {
		t.Fatal("a refused registration changed the table")
	}
}

package oauthas

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// topUpClients fills the table with unused clients of sources holding one
// each, as a flood that has run for a while keeps it, so every admitted
// registration drops one.
func (s *Server) topUpClients() {
	now := s.cfg.Clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.dynamic) < s.cfg.MaxClients {
		s.clientSeq++
		id := fmt.Sprint("fill-", s.clientSeq)
		s.clients[id] = Client{ID: id, Dynamic: true}
		s.dynamic[id] = &clientUse{source: id, seq: s.clientSeq, registered: now}
	}
}

// registerThroughLimits registers a client from source as the route does:
// the source's start limit first, then the table, which charges a random
// eviction against the eviction rate. It returns the client's ID, or ""
// and the wait it was given.
func (s *Server) registerThroughLimits(source string) (string, time.Duration) {
	if ok, wait := s.admitRegistration(source); !ok {
		return "", wait
	}
	s.mu.Lock()
	id := fmt.Sprintf("client-%d", s.clientSeq+1)
	s.mu.Unlock()
	if added, wait := s.addDynamicClient(Client{ID: id, Dynamic: true}, source); !added {
		return "", wait
	}
	return id, 0
}

// A flood rotating through a new source for every registration, at 50 a
// second against a table kept full of one unused client per source, has
// its random evictions held to the eviction rate, and a person's client consented to
// within a minute of its registration survives in at least 85% of 400
// cases. A person refused while the flood holds the window registers again
// after the Retry-After it was given; people arrive every five seconds,
// and each is checked a minute after its registration was admitted.
func TestARotatingFloodOfRegistrationsIsHeldToTheEvictionRate(t *testing.T) {
	t.Parallel()
	const (
		people     = 400
		floodEvery = 20 * time.Millisecond
		arrival    = 5 * time.Second
	)
	limit := report.FloodSafeRate(DefaultMaxClients, report.StartSurvival)
	clock := &stillClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := clientTable(clock, DefaultMaxClients, rand.New(rand.NewPCG(51, 52)))
	var evictions []time.Time
	pick := s.pick
	s.pick = func(n int) (int, error) {
		evictions = append(evictions, clock.now)
		return pick(n)
	}
	type person struct {
		source   string
		next     time.Time
		id       string
		admitted time.Time
	}
	var (
		waiting  []*person
		admitted int
		arrived  int
		survived int
		checked  int
		flood    int
	)
	begin := clock.now
	nextArrival := begin
	for tick := begin; checked < people; tick = tick.Add(floodEvery) {
		for arrived < people && !nextArrival.After(tick) {
			waiting = append(waiting, &person{source: fmt.Sprint("person-", arrived), next: nextArrival})
			arrived++
			nextArrival = nextArrival.Add(arrival)
		}
		for _, p := range waiting {
			if p.id != "" || p.next.After(tick) {
				continue
			}
			clock.now = p.next
			s.topUpClients()
			id, wait := s.registerThroughLimits(p.source)
			if id == "" {
				if wait <= 0 {
					t.Fatalf("a refused registration names no time to retry")
				}
				p.next = clock.now.Add(time.Duration((wait+time.Second-1)/time.Second) * time.Second)
				continue
			}
			p.id, p.admitted = id, clock.now
			admitted++
		}
		clock.now = tick
		s.topUpClients()
		if id, _ := s.registerThroughLimits(fmt.Sprint("flood-", flood)); id != "" {
			admitted++
		}
		flood++
		kept := waiting[:0]
		for _, p := range waiting {
			if p.id != "" && !tick.Before(p.admitted.Add(time.Minute)) {
				s.mu.Lock()
				if _, ok := s.dynamic[p.id]; ok {
					survived++
				}
				s.mu.Unlock()
				checked++
				continue
			}
			kept = append(kept, p)
		}
		waiting = kept
	}
	slices.SortFunc(evictions, func(a, b time.Time) int { return a.Compare(b) })
	for i, at := range evictions {
		n := 0
		for _, other := range evictions[i:] {
			if other.Sub(at) < time.Minute {
				n++
			}
		}
		if n > 2*limit {
			t.Fatalf("%d random evictions within a minute of %s, want at most %d across two windows of %d", n, at, 2*limit, limit)
		}
	}
	minutes := int(clock.now.Sub(begin)/time.Minute) + 1
	if len(evictions) > minutes*limit {
		t.Fatalf("%d random evictions in %d minutes, want at most %d a minute", len(evictions), minutes, limit)
	}
	t.Logf("%d of %d registrations admitted, %d random evictions over %d minutes; %d of %d people's clients survived a minute",
		admitted, flood+people, len(evictions), minutes, survived, people)
	if rate := float64(survived) / people; rate < 0.85 {
		t.Fatalf("%d of %d people's clients survived a minute of the flood (%.0f%%), want at least 85%%", survived, people, rate*100)
	}
}

// One source may register a few clients a minute; the next registration
// is refused with 429 and the time to retry, and another source is not
// held up by it.
func TestARegistrationOverTheSourcesRateIsRefusedWithATimeToRetry(t *testing.T) {
	t.Parallel()
	clock := &stillClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := clientTable(clock, DefaultMaxClients, rand.New(rand.NewPCG(53, 54)))
	register := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{"redirect_uris":["http://127.0.0.1:9000/cb"]}`))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		s.handleRegister(rec, req)
		return rec
	}
	for n := range report.StartsPerSource {
		if rec := register("203.0.113.9:4000"); rec.Code != http.StatusCreated {
			t.Fatalf("registration %d: %d %s", n, rec.Code, rec.Body)
		}
	}
	if rec := register("203.0.113.9:4000"); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("a registration over the source's rate: %d, Retry-After %q; want 429 with a time to retry", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := register("203.0.113.10:4000"); rec.Code != http.StatusCreated {
		t.Fatalf("another source: %d %s", rec.Code, rec.Body)
	}
}

// A source that holds one unused client and registers another loses its
// own client, not anyone else's, though every source holds one.
func TestASourceRegisteringAgainDropsOnlyItsOwnClient(t *testing.T) {
	t.Parallel()
	clock := &stillClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := clientTable(clock, DefaultMaxClients, rand.New(rand.NewPCG(55, 56)))
	own := s.register("person")
	s.topUpClients()
	others := map[string]bool{}
	for id := range s.dynamic {
		if id != own {
			others[id] = true
		}
	}
	if s.register("person") == "" {
		t.Fatal("the source's new registration was refused")
	}
	if _, ok := s.dynamic[own]; ok {
		t.Fatal("the source's earlier client was kept and another source's dropped")
	}
	for id := range others {
		if _, ok := s.dynamic[id]; !ok {
			t.Fatalf("another source's client %s was dropped", id)
		}
	}
}

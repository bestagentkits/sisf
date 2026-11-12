package auth

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// rateClock stands still until a test moves it.
type rateClock struct{ now time.Time }

func (c *rateClock) Now() time.Time                         { return c.now }
func (c *rateClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// stubProvider answers sign-ins and step-ups without a network.
type stubProvider struct{}

func (stubProvider) Name() string   { return "stub" }
func (stubProvider) Issuer() string { return "https://stub.example" }
func (stubProvider) AuthorizeURL(req identity.AuthRequest) (string, error) {
	return "https://stub.example/authorize?state=" + req.State, nil
}
func (stubProvider) StepUpURL(req identity.AuthRequest) (string, error) {
	return "https://stub.example/authorize?prompt=login&state=" + req.State, nil
}
func (stubProvider) Exchange(context.Context, identity.Exchange) (identity.Identity, error) {
	return identity.Identity{}, core.NewError(core.CodeNotSupported, "not in this test")
}
func (stubProvider) ExchangeFresh(context.Context, identity.Exchange, time.Time) (identity.Identity, error) {
	return identity.Identity{}, core.NewError(core.CodeNotSupported, "not in this test")
}

// rateService is a service whose sign-in table is full of one flow from
// each of maxFlows sources, choosing which to push out with rng; the
// Owner's session is under ownerKey.
func rateService(t *testing.T, clock *rateClock, rng *rand.Rand) *Service {
	t.Helper()
	s, err := New(Config{Providers: []identity.Provider{stubProvider{}}, Clock: clock,
		HoldsRole: func(actorID string) bool { return actorID == "owner" }})
	if err != nil {
		t.Fatal(err)
	}
	s.pick = func(n int) (int, error) { return rng.IntN(n), nil }
	s.sessions[ownerKey] = &session{key: ownerKey, actorID: "owner", expires: clock.now.Add(time.Hour)}
	s.topUp(clock.now)
	return s
}

const ownerKey = "owner-session"

// topUp fills the table with flows of sources holding one each, as a flood
// that has run for a while keeps it, so every admitted start pushes one out.
func (s *Service) topUp(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.flows) < maxFlows {
		s.flowSeq++
		s.flows[fmt.Sprint("fill-", s.flowSeq)] = &flow{source: fmt.Sprint("fill-", s.flowSeq), seq: s.flowSeq, started: now, expires: now.Add(flowTTL)}
	}
}

// flowOf returns the key of the flow source started, or "".
func (s *Service) flowOf(source string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, f := range s.flows {
		if f.source == source {
			return k
		}
	}
	return ""
}

// retryAfterOf is the wait a refused start names.
func retryAfterOf(t *testing.T, err error) time.Duration {
	t.Helper()
	typed, ok := err.(*core.Error)
	if !ok || typed.Code != core.CodeRateLimited {
		t.Fatalf("a refused start: %v, want RATE_LIMITED", err)
	}
	seconds, convErr := strconv.Atoi(typed.Details["retry_after_seconds"])
	if convErr != nil || seconds < 1 {
		t.Fatalf("a refused start names no time to retry: %+v", typed.Details)
	}
	return time.Duration(seconds) * time.Second
}

// A flood rotating through a new source for every start, at 50 starts a
// second against a table kept full of one flow per source, has its random
// evictions held to the eviction rate, and a person's sign-in finished within a minute of
// its start survives in at least 85% of 400 cases. A person refused while
// the flood holds the window starts again after the Retry-After it was
// given; people arrive every five seconds, and each is checked a minute
// after its start was admitted. The Owner's step-up starts at once
// throughout, whatever the flood spent.
func TestARotatingFloodIsHeldToTheEvictionRate(t *testing.T) {
	t.Parallel()
	const (
		people     = 400
		floodEvery = 20 * time.Millisecond
		arrival    = 5 * time.Second
	)
	limit := report.FloodSafeRate(maxFlows, report.StartSurvival)
	clock := &rateClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := rateService(t, clock, rand.New(rand.NewPCG(21, 22)))
	// Every random eviction but those of the Owner's exempt step-ups is
	// recorded.
	var evictions []time.Time
	ownerStepping := false
	pick := s.pick
	s.pick = func(n int) (int, error) {
		if !ownerStepping {
			evictions = append(evictions, clock.now)
		}
		return pick(n)
	}
	type person struct {
		source   string
		next     time.Time
		key      string
		admitted time.Time
	}
	var (
		waiting  []*person
		admitted int
		arrived  int
		survived int
		checked  int
		flood    int
		heldUp   int
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
			if p.key != "" || p.next.After(tick) {
				continue
			}
			clock.now = p.next
			s.topUp(clock.now)
			if _, _, err := s.beginFlow("stub", "/", p.source, "", StepUpTarget{}); err != nil {
				p.next = clock.now.Add(retryAfterOf(t, err))
				continue
			}
			p.key, p.admitted = s.flowOf(p.source), clock.now
			admitted++
		}
		clock.now = tick
		s.topUp(clock.now)
		if _, _, err := s.beginFlow("stub", "/", fmt.Sprint("flood-", flood), "", StepUpTarget{}); err == nil {
			admitted++
		}
		flood++
		if flood%3000 == 0 {
			// Every minute the Owner confirms an action, flood or not; a
			// probe from a new address shows whether the flood holds the
			// limits at that moment.
			if _, _, err := s.beginFlow("stub", "/", fmt.Sprint("probe-", flood), "", StepUpTarget{}); err != nil {
				heldUp++
			}
			ownerStepping = true
			if _, _, err := s.beginFlow("stub", "/", "owner-address", ownerKey, StepUpTarget{Action: "merge", Target: "acme/app#1"}); err != nil {
				t.Fatalf("the Owner's step-up during the flood: %v", err)
			}
			ownerStepping = false
		}
		kept := waiting[:0]
		for _, p := range waiting {
			if p.key != "" && !tick.Before(p.admitted.Add(time.Minute)) {
				s.mu.Lock()
				if _, ok := s.flows[p.key]; ok {
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
	if heldUp == 0 {
		t.Fatal("the flood never held the limits when the Owner stepped up, so the step-up's exemption was not tried")
	}
	t.Logf("%d of %d flood starts and people admitted, %d random evictions over %d minutes; %d of %d people's sign-ins survived a minute",
		admitted, flood+people, len(evictions), minutes, survived, people)
	if rate := float64(survived) / people; rate < 0.85 {
		t.Fatalf("%d of %d people's sign-ins survived a minute of the flood (%.0f%%), want at least 85%%", survived, people, rate*100)
	}
}

// One source may start a few sign-ins a minute, not more, and is told
// when to try again; another source is not held up by it.
func TestOneSourceIsHeldToItsOwnStartRate(t *testing.T) {
	t.Parallel()
	clock := &rateClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s, err := New(Config{Providers: []identity.Provider{stubProvider{}}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	for n := range report.StartsPerSource {
		if _, _, err := s.beginFlow("stub", "/", "busy", "", StepUpTarget{}); err != nil {
			t.Fatalf("start %d from one source: %v", n, err)
		}
	}
	_, _, err = s.beginFlow("stub", "/", "busy", "", StepUpTarget{})
	if wait := retryAfterOf(t, err); wait > report.StartWindow {
		t.Fatalf("a source over its rate is told to wait %s, want at most %s", wait, report.StartWindow)
	}
	if _, _, err := s.beginFlow("stub", "/", "other", "", StepUpTarget{}); err != nil {
		t.Fatalf("another source: %v", err)
	}
	clock.now = clock.now.Add(report.StartWindow)
	if _, _, err := s.beginFlow("stub", "/", "busy", "", StepUpTarget{}); err != nil {
		t.Fatalf("the source a window later: %v", err)
	}
	// Over HTTP the refusal is 429 with the time to retry.
	handler := s.Handler()
	var rec *httptest.ResponseRecorder
	for range report.StartsPerSource + 1 {
		req := httptest.NewRequest(http.MethodGet, "/auth/login/stub", nil)
		req.RemoteAddr = "198.51.100.9:4000"
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("a sign-in over the source's rate: %d, Retry-After %q; want 429 with a time to retry", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// A source that holds one flow and starts another loses its own flow, not
// anyone else's, though every source holds one: it is counted as if its new
// flow were in the table already.
func TestASourceStartingAgainPushesOutOnlyItsOwnFlow(t *testing.T) {
	t.Parallel()
	s := flowTable(rand.New(rand.NewPCG(31, 32)))
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	own := s.admit(now, "person", false)
	for i := 0; len(s.flows) < maxFlows; i++ {
		s.admit(now, fmt.Sprintf("single-%d", i), false)
	}
	others := map[string]bool{}
	for k := range s.flows {
		if k != own {
			others[k] = true
		}
	}
	again := s.admit(now, "person", false)
	if again == "" {
		t.Fatal("the source's new start was refused")
	}
	if _, ok := s.flows[own]; ok {
		t.Fatal("the source's earlier flow was kept and another source's pushed out")
	}
	for k := range others {
		if _, ok := s.flows[k]; !ok {
			t.Fatalf("another source's flow %s was pushed out", k)
		}
	}
}

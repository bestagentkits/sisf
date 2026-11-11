package api

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
)

// topUpGrants fills the table with waiting requests of sources holding one
// each, as a flood that has run for a while keeps it, so every admitted
// start drops one.
func (s *deviceStore) topUpGrants() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	for len(s.grants) < maxOpenGrants {
		s.seq++
		code := fmt.Sprint("fill-", s.seq)
		s.grants[digestOf(code)] = &grant{userCode: code, expires: now.Add(s.ttl), source: code, seq: s.seq}
		s.byUser[code] = digestOf(code)
	}
}

// startThroughLimits starts a request from source as the route does: the
// start limits first, then the table.
func (s *deviceStore) startThroughLimits(source string) (string, error) {
	if err := s.admitStart(source); err != nil {
		return "", err
	}
	code, _, err := s.start("cli", nil, source)
	return digestOf(code), err
}

// waitOf is the wait a refused start names.
func waitOf(t *testing.T, err error) time.Duration {
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
// second against a table kept full of one request per source, has its
// random evictions held to the eviction rate, and a person's request approved within a minute
// of its start survives in at least 85% of 400 cases. A person refused
// while the flood holds the window starts again after the Retry-After it
// was given; people arrive every five seconds, and each is checked a
// minute after its start was admitted.
func TestARotatingFloodOfLoginRequestsIsHeldToTheEvictionRate(t *testing.T) {
	t.Parallel()
	const (
		people     = 400
		floodEvery = 20 * time.Millisecond
		arrival    = 5 * time.Second
	)
	limit := report.FloodSafeRate(maxOpenGrants, report.StartSurvival)
	clock := &fixedClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := newDeviceStore(clock, 10*time.Minute, 5*time.Second)
	rng := rand.New(rand.NewPCG(41, 42))
	var evictions []time.Time
	s.pick = func(n int) (int, error) {
		evictions = append(evictions, clock.now)
		return rng.IntN(n), nil
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
			s.topUpGrants()
			key, err := s.startThroughLimits(p.source)
			if err != nil {
				p.next = clock.now.Add(waitOf(t, err))
				continue
			}
			p.key, p.admitted = key, clock.now
			admitted++
		}
		clock.now = tick
		s.topUpGrants()
		if _, err := s.startThroughLimits(fmt.Sprint("flood-", flood)); err == nil {
			admitted++
		}
		flood++
		kept := waiting[:0]
		for _, p := range waiting {
			if p.key != "" && !tick.Before(p.admitted.Add(time.Minute)) {
				s.mu.Lock()
				if _, ok := s.grants[p.key]; ok {
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
	t.Logf("%d of %d starts admitted, %d random evictions over %d minutes; %d of %d people's login requests survived a minute",
		admitted, flood+people, len(evictions), minutes, survived, people)
	if rate := float64(survived) / people; rate < 0.85 {
		t.Fatalf("%d of %d people's login requests survived a minute of the flood (%.0f%%), want at least 85%%", survived, people, rate*100)
	}
}

// One source may start a few login requests a minute, not more, and is
// told when to try again; another source is not held up by it.
func TestOneSourceIsHeldToItsOwnLoginRequestRate(t *testing.T) {
	t.Parallel()
	clock := &fixedClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := newDeviceStore(clock, 10*time.Minute, 5*time.Second)
	for n := range report.StartsPerSource {
		if _, err := s.startThroughLimits("busy"); err != nil {
			t.Fatalf("start %d from one source: %v", n, err)
		}
	}
	_, err := s.startThroughLimits("busy")
	if wait := waitOf(t, err); wait > report.StartWindow {
		t.Fatalf("a source over its rate is told to wait %s, want at most %s", wait, report.StartWindow)
	}
	if _, err := s.startThroughLimits("other"); err != nil {
		t.Fatalf("another source: %v", err)
	}
}

// A source that holds one waiting request and starts another loses its
// own request, not anyone else's, though every source holds one.
func TestASourceStartingAgainDropsOnlyItsOwnLoginRequest(t *testing.T) {
	t.Parallel()
	clock := &fixedClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := newDeviceStore(clock, 10*time.Minute, 5*time.Second)
	rng := rand.New(rand.NewPCG(43, 44))
	s.pick = func(n int) (int, error) { return rng.IntN(n), nil }
	code, _, err := s.start("cli", nil, "person")
	if err != nil {
		t.Fatal(err)
	}
	own := digestOf(code)
	s.topUpGrants()
	others := map[string]bool{}
	for k := range s.grants {
		if k != own {
			others[k] = true
		}
	}
	if _, _, err := s.start("cli", nil, "person"); err != nil {
		t.Fatalf("the source's new start: %v", err)
	}
	if _, ok := s.grants[own]; ok {
		t.Fatal("the source's earlier request was kept and another source's dropped")
	}
	for k := range others {
		if _, ok := s.grants[k]; !ok {
			t.Fatalf("another source's request %s was dropped", k)
		}
	}
}

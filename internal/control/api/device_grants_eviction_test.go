package api

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/core"
)

// fixedClock stands still until a test moves it.
type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time                         { return c.now }
func (c *fixedClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

var _ core.Clock = (*fixedClock)(nil)

// A flood of one login request from each of many addresses, at about 1.7
// starts a second, keeps every slot taken by sources holding one each.
// Every start is still admitted, a request being approved or approved and
// not collected is never dropped, and a person's request approved within a
// minute of its start survives in at least 85% of cases: people start every
// other request of the flood, and each is checked a minute after it
// started. The table is filled at the start and the test ends before those
// requests lapse, so every start drops one, the worst case of that rate.
// The start limits are lifted: the test is about the table behind them.
func TestAOnePerSourceFloodRarelyDropsAPersonsLoginRequest(t *testing.T) {
	t.Parallel()
	const people = 400
	interval := time.Second * 10 / 17
	perMinute := int(time.Minute / interval)
	clock := &fixedClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	begin := clock.now
	s := newDeviceStore(clock, 10*time.Minute, 5*time.Second)
	s.starts = report.NewStartLimits(1<<30, 1<<30, time.Minute, 1<<20)
	rng := rand.New(rand.NewPCG(5, 6))
	s.pick = func(n int) (int, error) { return rng.IntN(n), nil }
	start := func(source string) string {
		code, _, err := s.start("cli", nil, source)
		if err != nil {
			t.Fatalf("a start from %s with every slot held by one request per source: %v", source, err)
		}
		return digestOf(code)
	}
	approved := start("owner")
	s.grants[approved].approved = true
	claimed := start("maintainer")
	s.grants[claimed].claimed = true
	for n := 0; len(s.grants) < maxOpenGrants; n++ {
		start(fmt.Sprintf("fill-%d", n))
	}
	type person struct {
		key     string
		started int
	}
	var waiting []person
	survived, checked := 0, 0
	for n := 0; checked < people; n++ {
		clock.now = clock.now.Add(interval)
		if n%2 == 0 {
			waiting = append(waiting, person{key: start(fmt.Sprintf("person-%d", n)), started: n})
		} else {
			start(fmt.Sprintf("flood-%d", n))
		}
		for len(waiting) > 0 && n-waiting[0].started >= perMinute {
			if _, ok := s.grants[waiting[0].key]; ok {
				survived++
			}
			checked++
			waiting = waiting[1:]
		}
		if s.grants[approved] == nil || s.grants[claimed] == nil {
			t.Fatalf("start %d dropped a request being approved or approved", n)
		}
	}
	if elapsed := clock.now.Sub(begin); elapsed >= 10*time.Minute {
		t.Fatalf("the flood ran %s, past the requests' lifetime", elapsed)
	}
	t.Logf("%d of %d people's login requests survived a minute of the flood", survived, people)
	if rate := float64(survived) / people; rate < 0.85 {
		t.Fatalf("%d of %d people's login requests survived a minute of the flood (%.0f%%), want at least 85%%", survived, people, rate*100)
	}
}

// A start while every open request is being approved or waits to be
// collected, so none may be dropped, is refused without spending a random
// eviction, and says when to come back: when the first request lapses.
func TestALoginRequestWithNothingToDropSpendsNoEviction(t *testing.T) {
	t.Parallel()
	clock := &fixedClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := newDeviceStore(clock, 10*time.Minute, 5*time.Second)
	s.starts = report.NewStartLimits(1<<30, 1, time.Minute, 1<<20)
	for n := range maxOpenGrants {
		code, _, err := s.start("cli", nil, fmt.Sprintf("approved-%d", n))
		if err != nil {
			t.Fatal(err)
		}
		s.grants[digestOf(code)].approved = true
	}
	clock.now = clock.now.Add(4 * time.Minute)
	_, _, err := s.start("cli", nil, "newcomer")
	var typed *core.Error
	if !errors.As(err, &typed) || typed.Code != core.CodeRateLimited || typed.Details["retry_after_seconds"] != "360" {
		t.Fatalf("a start with every request approved: %v, want RATE_LIMITED until the first request lapses in 360 seconds", err)
	}
	if ok, _ := s.starts.Evict(clock.now); !ok {
		t.Fatal("a refused start spent the random eviction it never made")
	}
}

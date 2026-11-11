package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
	"github.com/bestagentkits/sisf/pkg/identity"
)

// Sources each starting sign-ins at their own full rate push out only
// their own flows, or none while the table has room, so they never spend
// the allowance of random evictions: nine sources at ten starts a minute
// for ten minutes leave a tenth source's sign-in admitted every minute.
// Nor does a table full of flows of sources holding several: a start from
// each of more new sources than the eviction allowance pushes out the
// busiest source's flows and is admitted.
func TestBusySourcesWithinTheirRatesDoNotLockOutASignIn(t *testing.T) {
	t.Parallel()
	clock := &rateClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s, err := New(Config{Providers: []identity.Provider{stubProvider{}}, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	for minute := range 10 {
		for source := range 9 {
			for n := range report.StartsPerSource {
				if _, _, err := s.beginFlow("stub", "/", fmt.Sprint("busy-", source), "", StepUpTarget{}); err != nil {
					t.Fatalf("minute %d: start %d of source %d: %v", minute, n, source, err)
				}
			}
		}
		if _, _, err := s.beginFlow("stub", "/", fmt.Sprint("person-", minute), "", StepUpTarget{}); err != nil {
			t.Fatalf("minute %d: a person's sign-in after nine sources started at their rates: %v", minute, err)
		}
		clock.now = clock.now.Add(report.StartWindow)
	}
	s.mu.Lock()
	for i := 0; len(s.flows) < maxFlows; i++ {
		s.flowSeq++
		source := fmt.Sprint("crowd-", i%4)
		s.flows[fmt.Sprint("crowd-", s.flowSeq)] = &flow{source: source, seq: s.flowSeq, started: clock.now, expires: clock.now.Add(flowTTL)}
	}
	s.mu.Unlock()
	for i := range 2 * report.FloodSafeRate(maxFlows, report.StartSurvival) {
		if _, _, err := s.beginFlow("stub", "/", fmt.Sprint("newcomer-", i), "", StepUpTarget{}); err != nil {
			t.Fatalf("newcomer %d into a table full of sources holding several: %v", i, err)
		}
	}
}

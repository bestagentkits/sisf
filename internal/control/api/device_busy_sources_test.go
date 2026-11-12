package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// Sources each starting login requests at their own full rate drop only
// their own requests, or none while the table has room, so they never
// spend the allowance of random evictions: nine sources at ten starts a
// minute for ten minutes leave a tenth source's request admitted every
// minute. Nor does a table full of requests of sources holding several: a
// start from each of more new sources than the eviction allowance drops
// the busiest source's requests and is admitted.
func TestBusySourcesWithinTheirRatesDoNotLockOutALoginRequest(t *testing.T) {
	t.Parallel()
	clock := &fixedClock{now: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	s := newDeviceStore(clock, 10*time.Minute, 5*time.Second)
	for minute := range 10 {
		for source := range 9 {
			for n := range report.StartsPerSource {
				if _, err := s.startThroughLimits(fmt.Sprint("busy-", source)); err != nil {
					t.Fatalf("minute %d: start %d of source %d: %v", minute, n, source, err)
				}
			}
		}
		if _, err := s.startThroughLimits(fmt.Sprint("person-", minute)); err != nil {
			t.Fatalf("minute %d: a person's login request after nine sources started at their rates: %v", minute, err)
		}
		clock.now = clock.now.Add(report.StartWindow)
	}
	s.mu.Lock()
	for i := 0; len(s.grants) < maxOpenGrants; i++ {
		s.seq++
		code := fmt.Sprint("crowd-", s.seq)
		s.grants[digestOf(code)] = &grant{userCode: code, expires: clock.now.Add(s.ttl), source: fmt.Sprint("crowd-", i%4), seq: s.seq}
		s.byUser[code] = digestOf(code)
	}
	s.mu.Unlock()
	for i := range 2 * report.FloodSafeRate(maxOpenGrants, report.StartSurvival) {
		if _, err := s.startThroughLimits(fmt.Sprint("newcomer-", i)); err != nil {
			t.Fatalf("newcomer %d into a table full of sources holding several: %v", i, err)
		}
	}
}

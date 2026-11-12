package report_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/intake/report"
)

// FloodSafeRate is the largest rate at which twice that many random
// evictions from a full table leave an entry standing with the survival
// asked for.
func TestFloodSafeRateKeepsAnEntryWithTheSurvivalAskedFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		slots int
		want  int
	}{{1024, 83}, {1000, 81}, {1, 1}} {
		got := report.FloodSafeRate(tc.slots, 0.85)
		if got != tc.want {
			t.Errorf("FloodSafeRate(%d, 0.85) = %d, want %d", tc.slots, got, tc.want)
		}
		if tc.slots < 2 {
			continue
		}
		keep := 1 - 1/float64(tc.slots)
		if math.Pow(keep, float64(2*got)) < 0.85 || math.Pow(keep, float64(2*(got+1))) >= 0.85 {
			t.Errorf("FloodSafeRate(%d, 0.85) = %d is not the largest safe rate", tc.slots, got)
		}
	}
}

// The start limits hold each source to its rate at admission and spend the
// eviction allowance only when asked for an eviction: sources that never
// push out another source's entry at random cannot use it up, however many
// starts they make, and the evictions stay held to their rate.
func TestStartLimitsChargeTheEvictionAllowanceOnlyForEvictions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	l := report.NewStartLimiter(1024)
	evictions := report.FloodSafeRate(1024, report.StartSurvival)
	for minute := range 10 {
		at := now.Add(time.Duration(minute) * report.StartWindow)
		for source := range 9 {
			for n := range report.StartsPerSource {
				if ok, _ := l.Admit(fmt.Sprint("busy-", source), at); !ok {
					t.Fatalf("minute %d: start %d of source %d refused within its own rate", minute, n, source)
				}
			}
			if ok, wait := l.Admit(fmt.Sprint("busy-", source), at); ok || wait <= 0 || wait > report.StartWindow {
				t.Fatalf("minute %d: a start of source %d over its rate: allowed %v, wait %s", minute, source, ok, wait)
			}
		}
		if ok, _ := l.Admit("person", at); !ok {
			t.Fatalf("minute %d: a tenth source was refused after nine sources spent their own rates", minute)
		}
		if ok, _ := l.Evict(at); !ok {
			t.Fatalf("minute %d: the eviction allowance was spent by starts that evicted nothing", minute)
		}
	}
	later := now.Add(time.Hour)
	for n := range evictions {
		if ok, _ := l.Evict(later); !ok {
			t.Fatalf("eviction %d of %d in a window refused", n, evictions)
		}
	}
	if ok, wait := l.Evict(later); ok || wait <= 0 || wait > report.StartWindow {
		t.Fatalf("an eviction over the rate: allowed %v, wait %s; want refused within a window", ok, wait)
	}
	if ok, _ := l.Admit("newcomer", later); !ok {
		t.Fatal("a start was refused at admission because the eviction allowance was spent")
	}
}

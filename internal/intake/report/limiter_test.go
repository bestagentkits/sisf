package report_test

import (
	"math"
	"testing"

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

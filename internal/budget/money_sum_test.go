package budget

import (
	"math"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

func TestAddMicrosStopsAtTheLargestAmount(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		in   []core.Micros
		want core.Micros
	}{
		"plain sum":                {[]core.Micros{1, 2, 3}, 6},
		"two totals at the cap":    {[]core.Micros{1 << 62, 1 << 62}, math.MaxInt64},
		"three totals and more":    {[]core.Micros{1 << 62, 1 << 62, 1 << 62, 1_000_000}, math.MaxInt64},
		"a negative counts as 0":   {[]core.Micros{-5, 7}, 7},
		"the largest amount stays": {[]core.Micros{math.MaxInt64, 1}, math.MaxInt64},
	} {
		if got := addMicros(c.in...); got != c.want {
			t.Errorf("%s: addMicros(%v) = %d, want %d", name, c.in, got, c.want)
		}
	}
}

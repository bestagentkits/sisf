package budget

import (
	"math"

	"github.com/bestagentkits/sisf/pkg/core"
)

// addMicros adds amounts of money without wrapping around. The store caps
// each of a period's totals, but the gate adds several of them and an
// estimate; a plain sum of totals near that cap passes the largest int64 and
// turns negative, which would let any reservation fit. The sum stops at the
// largest amount instead, and a negative amount, which no total holds,
// counts as 0.
func addMicros(amounts ...core.Micros) core.Micros {
	var sum core.Micros
	for _, a := range amounts {
		if a <= 0 {
			continue
		}
		if a > math.MaxInt64-sum {
			return math.MaxInt64
		}
		sum += a
	}
	return sum
}

// usedIn is what a period holds and has spent.
func usedIn(t core.BudgetTotals) core.Micros {
	return addMicros(t.Reserved, t.SpentVerified, t.SpentUnverified)
}

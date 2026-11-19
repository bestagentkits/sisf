// Package providercost reads what Anthropic and OpenAI report an
// organization spent, for the budget gate's provider cost floor. It runs in
// the control zone with an admin credential no agent holds, and uses only
// the standard library.
package providercost

import (
	"math"
	"math/big"
	"regexp"

	"github.com/bestagentkits/sisf/pkg/core"
)

// decimal is a decimal number as the providers write amounts: a JSON
// number, with a short exponent at most, so nothing is read loosely and no
// amount is costly to compute.
var decimal = regexp.MustCompile(`^-?[0-9]{1,30}(\.[0-9]{1,30})?([eE][+-]?[0-9]{1,2})?$`)

// micros converts an amount written as a decimal number of units, where
// perUnit micros make one unit, to micros, rounding up: a provider's
// fraction of a micro is never dropped from what was spent. It parses
// exactly and never through a float. A negative amount, as a credit,
// counts as 0, as the ledger counts one: it never lowers what was spent.
func micros(amount string, perUnit int64) (core.Micros, error) {
	if !decimal.MatchString(amount) {
		return 0, core.NewError(core.CodeInvalidArgument, "the provider reported an amount that is not a plain decimal number")
	}
	r, ok := new(big.Rat).SetString(amount)
	if !ok {
		return 0, core.NewError(core.CodeInvalidArgument, "the provider reported an amount that is not a plain decimal number")
	}
	if r.Sign() <= 0 {
		return 0, nil
	}
	r.Mul(r, new(big.Rat).SetInt64(perUnit))
	q, rem := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return math.MaxInt64, nil
	}
	return core.Micros(q.Int64()), nil
}

// add sums amounts without wrapping, stopping at the largest amount.
func add(a, b core.Micros) core.Micros {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}

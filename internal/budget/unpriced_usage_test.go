package budget_test

import (
	"context"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/pkg/core"
)

// tokensOnly is what run n reports when its provider counts tokens and
// states no cost, as the Codex CLI does.
func tokensOnly(n int) core.Usage {
	u := usage(n, 0, false)
	u.InputTokens, u.OutputTokens = 1_200, 300
	return u
}

func (e *env) settle(id string, u core.Usage) {
	e.t.Helper()
	if err := e.gate.Settle(context.Background(), e.ledger, id, u); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) totals() core.BudgetTotals {
	e.t.Helper()
	totals, err := e.ledger.BudgetTotals(context.Background(), time.Time{})
	if err != nil {
		e.t.Fatal(err)
	}
	return totals
}

// Under a cap, usage whose provider reported no cost is charged at what its
// run reserved, so such runs spend the cap like any other and the next
// reservation past it is refused. The record as reported is kept, with its
// tokens and no cost, next to the unverified charge.
func TestUsageWithoutACostIsChargedAtTheReservedAmountAndReachesTheCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	e.configure(capped(3*million, 0))
	first := e.mustReserve(1, million)
	e.settle(first.ID, tokensOnly(1))
	second := e.mustReserve(2, 2*million)
	e.settle(second.ID, tokensOnly(2))

	if got := e.totals(); got.SpentUnverified != 3*million || got.SpentVerified != 0 || got.Unpriced != 2 || got.UnpricedUncharged != 0 {
		t.Errorf("totals = %+v, want 3 million unverified from two records without a cost", got)
	}
	if e.status(first.ID) != core.ReservationSettled || e.status(second.ID) != core.ReservationSettled {
		t.Error("a charge at the reserved amount suspended its run")
	}
	_, err := e.reserve(3, 1)
	wantCode(t, err, core.CodeBudgetExceeded)

	reported, charge := e.ledger.usage[0], e.ledger.usage[1]
	if reported.Cost != 0 || reported.InputTokens != 1_200 || reported.OutputTokens != 300 || reported.Verified {
		t.Errorf("the record as reported was changed: %+v", reported)
	}
	if charge.Cost != million || charge.Verified || charge.InputTokens != 0 || charge.OutputTokens != 0 ||
		charge.ReservationID != first.ID || charge.RunID != first.RunID {
		t.Errorf("the charge is %+v, want the reserved amount, unverified, with no tokens", charge)
	}
}

// Usage that states a cost, usage the provider verified and usage with no
// tokens at all are recorded as they are: only a provider's silence about
// the cost of tokens it counted is charged.
func TestUsageWithACostOrWithoutTokensIsRecordedAsItIs(t *testing.T) {
	t.Parallel()
	priced := tokensOnly(1)
	priced.Cost = million / 2
	verified := tokensOnly(2)
	verified.Verified = true
	for name, u := range map[string]core.Usage{
		"priced":          priced,
		"verified":        verified,
		"no tokens":       usage(3, 0, false),
		"verified, empty": usage(4, 0, true),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.confirmUnlimited(0)
			e.configure(capped(10*million, 0))
			r := e.mustReserve(1, 4*million)
			u.IssueID, u.RunID = r.IssueID, r.RunID
			e.settle(r.ID, u)
			if len(e.ledger.usage) != 1 || e.ledger.usage[0].Cost != u.Cost {
				t.Errorf("recorded %+v, want the usage as reported", e.ledger.usage)
			}
			if got := e.totals(); got.SpentVerified+got.SpentUnverified != u.Cost || got.Unpriced != 0 {
				t.Errorf("totals = %+v", got)
			}
			if st := e.readStatus(); warned(st, "no cost") {
				t.Errorf("warned about a missing cost: %v", st.Warnings)
			}
		})
	}
}

// The Owner is told when usage in the period had no cost reported, and
// whether it was charged at the reserved amount or, with nothing reserved,
// is not counted at all. The warning about unverified usage stays.
func TestStatusWarnsAboutUsageWithoutACost(t *testing.T) {
	t.Parallel()
	charged := newEnv(t)
	charged.confirmUnlimited(0)
	charged.configure(capped(10*million, 0))
	r := charged.mustReserve(1, 2*million)
	charged.settle(r.ID, tokensOnly(1))
	st := charged.readStatus()
	if !warned(st, "no cost reported") || !warned(st, "charged at the amount reserved") || warned(st, "not counted") || !warned(st, "unverified") {
		t.Errorf("warnings = %v", st.Warnings)
	}
	if st.SpentUnverifiedMicros != 2*million || st.AvailableMicros == nil || *st.AvailableMicros != 8*million {
		t.Errorf("status = %+v", st)
	}

	uncounted := newEnv(t)
	uncounted.confirmUnlimited(0)
	r = uncounted.mustReserve(1, 0)
	uncounted.settle(r.ID, tokensOnly(1))
	st = uncounted.readStatus()
	if !warned(st, "no cost reported") || !warned(st, "not counted") || warned(st, "charged at the amount reserved") {
		t.Errorf("warnings = %v", st.Warnings)
	}
	if st.SpentUnverifiedMicros != 0 || uncounted.totals().UnpricedUncharged != 1 {
		t.Errorf("status = %+v", st)
	}
}

// The alert threshold counts the charge for usage without a cost.
func TestAlertFiresOnUsageChargedAtTheReservedAmount(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(2 * million)
	r := e.mustReserve(1, 3*million)
	e.settle(r.ID, tokensOnly(1))
	if got := e.notes.kinds(); len(got) != 1 || got[0] != budget.NoticeAlertThreshold {
		t.Fatalf("notices = %v, want one alert", got)
	}
	if n := e.notes.items[0]; n.SpentMicros != 3*million || n.LimitMicros != 2*million {
		t.Errorf("notice = %+v", n)
	}
}

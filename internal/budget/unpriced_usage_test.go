package budget_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// tokensOnly is what run n reports when its provider counts tokens and
// states no cost, as the Codex CLI does.
func tokensOnly(n int) core.Usage {
	u := usage(n, 0, false)
	u.InputTokens, u.OutputTokens = 1_200, 300
	return u
}

// Settle records usage without a reported cost as it was reported and adds
// no charge of its own: what its run reserved is charged, less the costs
// reported, only when the run's reservation is closed.
func TestSettlingUsageWithoutACostRecordsItAsReported(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.confirmUnlimited(0)
	e.configure(capped(10*million, 0))
	r := e.mustReserve(1, 2*million)
	u := tokensOnly(1)
	if err := e.gate.Settle(context.Background(), e.ledger, r.ID, u); err != nil {
		t.Fatal(err)
	}
	if len(e.ledger.usage) != 1 {
		t.Fatalf("recorded %d usage records, want the one reported: %+v", len(e.ledger.usage), e.ledger.usage)
	}
	if got := e.ledger.usage[0]; got.Cost != 0 || got.InputTokens != u.InputTokens || got.OutputTokens != u.OutputTokens || got.Verified {
		t.Errorf("the record as reported was changed: %+v", got)
	}
	if e.status(r.ID) != core.ReservationSettled {
		t.Errorf("reservation is %s, want settled", e.status(r.ID))
	}
}

// budget.get warns once about usage whose provider reported no cost, and
// not about usage with a cost, verified usage or usage without tokens.
func TestStatusWarnsOnlyAboutUsageWithoutACost(t *testing.T) {
	t.Parallel()
	priced := tokensOnly(1)
	priced.Cost = million / 2
	verified := tokensOnly(1)
	verified.Verified = true
	for name, c := range map[string]struct {
		u    core.Usage
		want int
	}{
		"tokens, no cost": {tokensOnly(1), 1},
		"priced":          {priced, 0},
		"verified":        {verified, 0},
		"no tokens":       {usage(1, 0, false), 0},
		"verified, empty": {usage(1, 0, true), 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.confirmUnlimited(0)
			if err := e.ledger.AppendUsage(context.Background(), c.u); err != nil {
				t.Fatal(err)
			}
			st := e.readStatus()
			got := 0
			for _, w := range st.Warnings {
				if strings.Contains(w, "no cost reported") {
					got++
				}
			}
			if got != c.want {
				t.Errorf("warnings = %v, want %d about usage with no cost reported", st.Warnings, c.want)
			}
			if c.want == 1 {
				unpricedWarning(t, st)
			}
		})
	}
}

package budget_test

import (
	"context"
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

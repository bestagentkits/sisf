package budget_test

import (
	"testing"

	"github.com/bestagentkits/sisf/internal/budget"
	"github.com/bestagentkits/sisf/internal/dispatcher"
)

// The reservation of a run can be found from the run's ID alone: it is
// the one the dispatcher's claim of the run made.
func TestARunsReservationIsDerivedFromItsID(t *testing.T) {
	e := newEnv(t)
	e.confirmUnlimited(0)
	req := request(1, 1000)
	req.IdempotencyKey = dispatcher.ClaimIdempotencyKey(req.RunID)
	r, err := e.gate.Reserve(t.Context(), e.ledger, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := budget.RunReservationID(req.RunID); got != r.ID {
		t.Fatalf("the derived reservation of %s is %s, want %s", req.RunID, got, r.ID)
	}
	if other := budget.RunReservationID("run-2"); other == r.ID {
		t.Fatal("two runs derive the same reservation")
	}
}

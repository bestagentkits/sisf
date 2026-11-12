package budget_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

func (e *env) put(key string, v any) {
	e.t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		e.t.Fatal(err)
	}
	entry, _ := e.ledger.GetKV(context.Background(), "install", key)
	if _, err := e.ledger.PutKV(context.Background(), state.KVEntry{Namespace: "install", Key: key, Value: raw, Version: entry.Version}); err != nil {
		e.t.Fatal(err)
	}
}

// request is the claim of run n of its own issue.
func request(n int, estimate core.Micros) core.ReservationRequest {
	return core.ReservationRequest{FactoryID: factory, IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: n},
		RunID: core.RunID(fmt.Sprintf("run-%d", n)), Estimate: estimate, IdempotencyKey: fmt.Sprintf("claim/run-%d", n)}
}

// usage is what run n reports it spent.
func usage(n int, cost core.Micros, verified bool) core.Usage {
	return core.Usage{FactoryID: factory, IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: n},
		RunID: core.RunID(fmt.Sprintf("run-%d", n)), ModelRoute: core.ModelRoute{Provider: "anthropic", Model: "claude"},
		Cost: cost, Verified: verified}
}

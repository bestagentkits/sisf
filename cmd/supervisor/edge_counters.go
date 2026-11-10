package main

import (
	"context"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/edge"
)

// edgeCounters reads the edge's cumulative counters for the decision layer:
// one endpoint per revision the edge served, with the requests that got a
// response and those that failed with a server error since the edge started.
type edgeCounters struct{ edge edge.Controller }

var _ decision.CounterSource = edgeCounters{}

// EndpointCounters returns the totals, sorted by endpoint. A count the edge
// reports as negative is untrusted data and read as zero.
func (c edgeCounters) EndpointCounters(ctx context.Context) ([]decision.EndpointCounter, error) {
	totals, err := c.edge.Counts(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]decision.EndpointCounter, 0, len(totals))
	for revision, counts := range totals {
		out = append(out, decision.EndpointCounter{
			Endpoint: revision, Requests: nonNegative(counts.Samples), ServerErrors: nonNegative(counts.ServerErrors),
		})
	}
	slices.SortFunc(out, func(a, b decision.EndpointCounter) int { return strings.Compare(a.Endpoint, b.Endpoint) })
	return out, nil
}

func nonNegative(n int64) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}

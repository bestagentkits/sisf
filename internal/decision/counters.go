package decision

import (
	"context"
	"time"
)

// EndpointCounter is the cumulative count of requests and 5xx responses the
// edge has seen for one endpoint since it started.
type EndpointCounter struct {
	Endpoint     string
	Requests     uint64
	ServerErrors uint64
}

// CounterSource reads the edge's cumulative counters. Everything it returns
// is untrusted data: endpoint names in particular may come from clients.
type CounterSource interface {
	EndpointCounters(ctx context.Context) ([]EndpointCounter, error)
}

// Window is the traffic of one endpoint between two snapshots.
type Window struct {
	Endpoint     string
	Requests     int64
	ServerErrors int64
	From, To     time.Time
	// Reset is set when a counter went backwards, which happens when the
	// edge restarts. The counts are then not known for the whole window.
	Reset bool
}

// Rate is the share of requests that failed with a 5xx, or 0 without traffic.
func (w Window) Rate() float64 {
	if w.Requests <= 0 {
		return 0
	}
	return float64(w.ServerErrors) / float64(w.Requests)
}

// sampler turns successive cumulative snapshots into windows.
type sampler struct {
	last   map[string]EndpointCounter
	lastAt time.Time
}

// next records the snapshot taken at at and returns the window since the
// previous one for each endpoint in it. The first snapshot only sets the
// starting point and returns nothing.
func (s *sampler) next(snapshot []EndpointCounter, at time.Time) []Window {
	prev, from := s.last, s.lastAt
	s.last, s.lastAt = make(map[string]EndpointCounter, len(snapshot)), at
	for _, c := range snapshot {
		s.last[c.Endpoint] = c
	}
	if prev == nil {
		return nil
	}
	out := make([]Window, 0, len(s.last))
	for name, cur := range s.last {
		// An endpoint first seen now had all of its traffic in this window.
		before := prev[name]
		w := Window{Endpoint: name, From: from, To: at}
		if cur.Requests < before.Requests || cur.ServerErrors < before.ServerErrors {
			w.Reset = true
			before = EndpointCounter{}
		}
		w.Requests = clampInt64(cur.Requests - before.Requests)
		w.ServerErrors = clampInt64(cur.ServerErrors - before.ServerErrors)
		out = append(out, w)
	}
	return out
}

func clampInt64(v uint64) int64 {
	const maxInt64 = 1<<63 - 1
	if v > maxInt64 {
		return maxInt64
	}
	return int64(v)
}

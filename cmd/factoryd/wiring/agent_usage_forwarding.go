package wiring

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/core"
)

// usageForwarding is a wrapped coding agent that still reports what its
// runs consumed. The wrappers of the agent zone (fresh workspaces, the
// model override) embed agent.Backend, which hides the adapter's optional
// agent.UsageReporter; the run controller records spend only through that
// interface, so without it a real agent's spend is never recorded. The
// wrappers pass the adapter's RunRef through unchanged, so the adapter
// answers for the runs the wrapper started.
type usageForwarding struct {
	agent.Backend
	usage agent.UsageReporter
}

// withUsageOf returns outer reporting the usage of adapter, or outer itself
// when the adapter reports none.
func withUsageOf(outer, adapter agent.Backend) agent.Backend {
	reporter, ok := adapter.(agent.UsageReporter)
	if !ok {
		return outer
	}
	return usageForwarding{Backend: outer, usage: reporter}
}

// Usage reports what the run consumed, as the adapter recorded it.
func (u usageForwarding) Usage(ctx context.Context, run agent.RunRef) ([]core.Usage, error) {
	return u.usage.Usage(ctx, run)
}

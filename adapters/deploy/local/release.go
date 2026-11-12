package local

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// ObservedRevision reports what serves the unit now. The edge says which
// revision is the stable one; that revision is then observed as above, and
// the edge must really point at its slot.
func (b *Backend) ObservedRevision(ctx context.Context, unit string) (deploy.Observed, error) {
	nothing := deploy.Observed{Unit: unit, ObservedAt: b.cfg.Clock.Now()}
	if !unitPattern.MatchString(unit) {
		return nothing, core.NewError(core.CodeInvalidArgument, "the unit name must be lowercase letters, digits and dashes")
	}
	split, err := b.cfg.Edge.Split(ctx)
	if err != nil {
		return nothing, hold("the edge could not say what it serves", err)
	}
	digest := core.ReleaseDigest(split.Stable.Revision)
	if digest.Validate() != nil {
		return nothing, nil
	}
	obs, err := b.observe(ctx, deploy.Revision{Unit: unit, Digest: digest})
	if err != nil {
		return nothing, hold("the running revision could not be observed", err)
	}
	s, ok := b.slotOf(digest)
	if !ok || !sameUpstream(split.Stable.URL, b.url(s)) {
		obs.Healthy = false
	}
	return obs, nil
}

func sameUpstream(x, y string) bool {
	ux, errX := edge.ParseUpstream(x)
	uy, errY := edge.ParseUpstream(y)
	return errX == nil && errY == nil && ux.String() == uy.String()
}

// Measure reads the edge's counters for the revision over the window, the
// application's health now and the critical lines its process wrote. Counters
// that cannot be read hold the release; they are never read as zero. Requests
// are the responses the edge saw, the denominator of an error rate.
func (b *Backend) Measure(ctx context.Context, rev deploy.Revision, window time.Duration) (deploy.Metrics, error) {
	if err := checkRevision(rev); err != nil {
		return deploy.Metrics{}, err
	}
	if window <= 0 {
		return deploy.Metrics{}, core.NewError(core.CodeInvalidArgument, "the window must be longer than zero")
	}
	counts, err := b.cfg.Edge.Counts(ctx, window)
	if err != nil {
		return deploy.Metrics{}, hold("the edge counters could not be read", err)
	}
	obs, err := b.observe(ctx, rev)
	if err != nil {
		return deploy.Metrics{}, hold("the revision could not be observed", err)
	}
	now := b.cfg.Clock.Now()
	var critical int64
	if s, ok := b.slotOf(rev.Digest); ok {
		critical = s.proc.CriticalSince(now.Add(-window))
	}
	c := counts[string(rev.Digest)]
	return deploy.Metrics{
		Revision: rev, From: now.Add(-window), To: now,
		Requests: c.Samples, ServerErrors: c.ServerErrors, Healthy: obs.Matches(rev), CriticalErrors: critical,
	}, nil
}

// Promote switches all traffic to rev, which must be running and healthy as
// itself. Only 100 is accepted: this backend has no traffic split. The
// previous revision keeps running on its slot as the way back.
func (b *Backend) Promote(ctx context.Context, rev deploy.Revision, trafficPercent int) error {
	if err := checkRevision(rev); err != nil {
		return err
	}
	if trafficPercent != 100 {
		return core.NewError(core.CodeNotSupported, "the local backend switches whole revisions; only 100 percent is accepted")
	}
	b.ops.Lock()
	defer b.ops.Unlock()
	if err := b.requireRunning(ctx, rev); err != nil {
		return err
	}
	s, ok := b.slotOf(rev.Digest)
	if !ok {
		return hold("the revision is not running", nil)
	}
	if err := b.cfg.Edge.SetSplit(ctx, edge.Split{Stable: b.target(s)}); err != nil {
		return hold("the edge did not accept the switch", err)
	}
	return nil
}

// Rollback makes an earlier revision the stable one again. It must still run,
// on its slot, healthy as itself; if it was replaced there is nothing to roll
// back to and the release holds. The revision it replaces is stopped.
func (b *Backend) Rollback(ctx context.Context, to deploy.Revision) error {
	if err := checkRevision(to); err != nil {
		return err
	}
	b.ops.Lock()
	defer b.ops.Unlock()
	s, ok := b.slotOf(to.Digest)
	if !ok {
		return hold("the revision to roll back to is no longer running", nil)
	}
	if err := b.requireRunning(ctx, to); err != nil {
		return err
	}
	if err := b.cfg.Edge.SetSplit(ctx, edge.Split{Stable: b.target(s)}); err != nil {
		return hold("the edge did not accept the rollback", err)
	}
	for _, other := range b.slot {
		if other.name != s.name {
			if err := b.stopSlot(ctx, other); err != nil {
				b.cfg.Logger.Warn("the replaced revision could not be stopped", "slot", other.name, "error", err.Error())
			}
		}
	}
	return nil
}

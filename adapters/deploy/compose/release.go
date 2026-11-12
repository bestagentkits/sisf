package compose

import (
	"context"
	"strings"
	"time"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/deploy/healthprobe"
)

// Measure reads the edge's counters for the revision over the window, the
// application's health now, and the critical lines in its log. Counters or a
// log that cannot be read hold the release; they are never read as zero.
// Requests are the responses the edge saw, the denominator of an error rate.
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
	logs, err := b.api.logsSince(ctx, b.containerName(rev.Unit, rev.Digest), now.Add(-window))
	if err != nil {
		return deploy.Metrics{}, hold("the revision's log could not be read", err)
	}
	c := counts[string(rev.Digest)]
	var critical int64
	for _, line := range strings.Split(logs, "\n") {
		if healthprobe.IsCriticalLine(line) {
			critical++
		}
	}
	return deploy.Metrics{
		Revision: rev, From: now.Add(-window), To: now,
		Requests: c.Samples, ServerErrors: c.ServerErrors, Healthy: obs.Matches(rev), CriticalErrors: critical,
	}, nil
}

// Promote sends trafficPercent of traffic to rev, which must be running and
// healthy as itself. 100 makes it the stable revision and keeps the previous
// one running as the way back; less sends that share to it as the canary.
func (b *Backend) Promote(ctx context.Context, rev deploy.Revision, trafficPercent int) error {
	if err := checkRevision(rev); err != nil {
		return err
	}
	if trafficPercent < 1 || trafficPercent > 100 {
		return core.NewError(core.CodeInvalidArgument, "traffic percent must be 1 to 100")
	}
	if err := b.requireRunning(ctx, rev); err != nil {
		return err
	}
	current, err := b.cfg.Edge.Split(ctx)
	if err != nil {
		return hold("the edge could not say what it serves", err)
	}
	if trafficPercent == 100 {
		if err := b.cfg.Edge.SetSplit(ctx, edge.Split{Stable: b.target(rev)}); err != nil {
			return hold("the edge did not accept the switch", err)
		}
		b.retire(ctx, rev, current.Stable.Revision)
		return nil
	}
	if current.Stable.Revision == string(rev.Digest) {
		return core.NewError(core.CodeInvalidArgument, "the revision is already the stable one")
	}
	canary := b.target(rev)
	if err := b.cfg.Edge.SetSplit(ctx, edge.Split{Stable: current.Stable, Canary: &canary, CanaryPercent: trafficPercent}); err != nil {
		return hold("the edge did not accept the split", err)
	}
	return nil
}

// Rollback makes an earlier revision the stable one again. That revision must
// still exist and be healthy as itself; if it has been removed there is
// nothing to roll back to and the release holds. Only containers change:
// volumes are never touched. The revisions it replaces are stopped and kept
// for diagnosis until the next promotion removes them.
func (b *Backend) Rollback(ctx context.Context, to deploy.Revision) error {
	if err := checkRevision(to); err != nil {
		return err
	}
	name := b.containerName(to.Unit, to.Digest)
	if _, err := b.api.inspect(ctx, name); err != nil {
		if core.HasCode(err, core.CodeNotFound) {
			return hold("the revision to roll back to no longer exists", err)
		}
		return hold("the revision to roll back to could not be inspected", err)
	}
	if err := b.api.start(ctx, name); err != nil {
		return hold("the revision to roll back to could not be started", err)
	}
	if err := b.waitReady(ctx, to); err != nil {
		return err
	}
	if err := b.cfg.Edge.SetSplit(ctx, edge.Split{Stable: b.target(to)}); err != nil {
		return hold("the edge did not accept the rollback", err)
	}
	b.eachOther(ctx, to, nil, func(other string) error { return b.api.stop(ctx, other) })
	return nil
}

// requireRunning holds the release unless rev is observed running as itself.
func (b *Backend) requireRunning(ctx context.Context, rev deploy.Revision) error {
	obs, err := b.observe(ctx, rev)
	if err != nil {
		return hold("the revision could not be observed", err)
	}
	if !obs.Matches(rev) {
		return hold("the revision is not running and healthy as itself", nil)
	}
	return nil
}

// retire removes the unit's containers other than rev and the previous
// stable revision, which stays as the way back. A failure to remove is
// logged and does not undo a switch that happened.
func (b *Backend) retire(ctx context.Context, rev deploy.Revision, previousStable string) {
	keep := map[string]bool{}
	if d := core.ReleaseDigest(previousStable); d.Validate() == nil {
		keep[b.containerName(rev.Unit, d)] = true
	}
	b.eachOther(ctx, rev, keep, func(other string) error { return b.api.remove(ctx, other) })
}

// eachOther applies act to every container of rev's unit except rev's and
// the names in keep.
func (b *Backend) eachOther(ctx context.Context, rev deploy.Revision, keep map[string]bool, act func(name string) error) {
	views, err := b.api.list(ctx, LabelFactory+"="+string(b.cfg.Factory), LabelUnit+"="+rev.Unit)
	if err != nil {
		b.cfg.Logger.Warn("the unit's containers could not be listed", "error", err.Error())
		return
	}
	self := b.containerName(rev.Unit, rev.Digest)
	for _, v := range views {
		if v.Name == self || keep[v.Name] || v.Name == "" {
			continue
		}
		if err := act(v.Name); err != nil {
			b.cfg.Logger.Warn("a container of the unit could not be retired", "container", v.Name, "error", err.Error())
		}
	}
}

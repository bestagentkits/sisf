package compose

import (
	"context"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/deploy/healthprobe"
)

// observe reads what runs as rev from two sources that do not share a
// result: the label on the container, and the revision the application
// reports about itself. Observed.Digest is what the application says it is.
// Observed.Healthy needs the container running, the label to name rev, and
// the application to be healthy and to say the same revision. A container
// that is gone or stopped, or an application that does not answer, is
// observed as not healthy; only a failure to ask Docker is an error.
func (b *Backend) observe(ctx context.Context, rev deploy.Revision) (deploy.Observed, error) {
	obs := deploy.Observed{Unit: rev.Unit, ObservedAt: b.cfg.Clock.Now()}
	name := b.containerName(rev.Unit, rev.Digest)
	view, err := b.api.inspect(ctx, name)
	if core.HasCode(err, core.CodeNotFound) {
		return obs, nil
	}
	if err != nil {
		return obs, err
	}
	if !view.State.Running {
		return obs, nil
	}
	health, err := healthprobe.Probe(ctx, b.cfg.HTTP, b.cfg.Resolve(name))
	if err != nil {
		if ctx.Err() != nil {
			return obs, ctx.Err()
		}
		return obs, nil
	}
	labels := view.Config.Labels
	obs.Digest = core.ReleaseDigest(health.Revision)
	obs.Healthy = health.Healthy && health.Revision != "" &&
		labels[LabelDigest] == health.Revision && labels[LabelUnit] == rev.Unit && labels[LabelFactory] == string(b.cfg.Factory)
	return obs, nil
}

// ObservedRevision reports what serves the unit now. The edge says which
// revision is the stable one; that revision is then observed as above, and
// the edge must really point at its container. Nothing a deploy call
// returned is consulted.
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
	rev := deploy.Revision{Unit: unit, Digest: digest}
	obs, err := b.observe(ctx, rev)
	if err != nil {
		return nothing, hold("the running revision could not be observed", err)
	}
	if !b.sameUpstream(split.Stable.URL, b.cfg.Resolve(b.containerName(unit, digest))) {
		obs.Healthy = false
	}
	return obs, nil
}

// sameUpstream compares two upstream URLs by their canonical form.
func (b *Backend) sameUpstream(x, y string) bool {
	ux, errX := edge.ParseUpstream(x)
	uy, errY := edge.ParseUpstream(y)
	return errX == nil && errY == nil && ux.String() == uy.String()
}

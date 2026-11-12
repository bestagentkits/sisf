package release

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// rollout is one release in flight.
type rollout struct {
	h      *holding
	v      verifiedRelease
	unit   string
	rev    deploy.Revision
	lkg    LKG
	lkgRev deploy.Revision
	// baseline is the 5xx rate of the revision being replaced.
	baseline float64
	// lowTraffic is set once real traffic did not reach MinSamples in a window.
	lowTraffic bool
	canary     bool
	monitor    *Monitor
	windows    []WindowReport
	extensions int
	observed   deploy.Observed
	// feature and summary go into the changelog and the catalog.
	feature, summary string
}

// preflight proves there is a way back and a way to observe before anything
// is deployed. It holds when:
//
//   - the backend cannot say what it can do, or cannot switch with a way
//     back, observe a revision and measure it (a broken adapter);
//   - the unit has no LKG, or the LKG is the release itself;
//   - the LKG is not observed running and healthy as itself, so the way back
//     cannot be verified;
//   - the LKG cannot be measured (no metrics);
//   - real traffic is too low to judge and there is no synthetic suite.
func (p *Pipeline) preflight(ctx context.Context, h *holding, v verifiedRelease) (*rollout, error) {
	unit := v.artifact.Unit
	caps, err := p.cfg.Backend.Capabilities(ctx)
	if err != nil {
		return nil, holdf("the deployment backend cannot say what it supports").WithCause(err)
	}
	if !deploy.SelfPublishAllowed(caps) {
		return nil, holdf("the deployment backend cannot switch with a way back, observe and measure a release")
	}
	lkg, found, err := p.cfg.LKG.Get(ctx, unit)
	switch {
	case err != nil:
		return nil, holdf("the last known good release of %s cannot be read", unit).WithCause(err)
	case !found:
		return nil, holdf("unit %s has no last known good release to roll back to", unit)
	case lkg.Digest == v.artifact.Digest:
		return nil, holdf("the release is already the last known good release; rolling back to it is no way back")
	}
	r := &rollout{
		h: h, v: v, unit: unit, lkg: lkg, monitor: NewMonitor(p.cfg.Monitor),
		rev:    deploy.Revision{Unit: unit, Digest: v.artifact.Digest},
		lkgRev: deploy.Revision{Unit: unit, Digest: lkg.Digest},
		canary: caps.Supports(core.CapDeployCanary) && len(p.cfg.CanarySteps) > 1,
	}
	observed, err := p.cfg.Backend.ObservedRevision(ctx, unit)
	if err != nil {
		return nil, holdf("what runs as %s cannot be observed", unit).WithCause(err)
	}
	if !observed.Matches(r.lkgRev) {
		return nil, holdf("the last known good release is not observed running and healthy, so the way back is not verified")
	}
	r.observed = observed
	metrics, err := p.cfg.Backend.Measure(ctx, r.lkgRev, p.cfg.Monitor.Window)
	if err != nil {
		return nil, holdf("the metrics of %s cannot be read, so the release could not be judged", unit).WithCause(err)
	}
	if rate, ok := metrics.ErrorRate(); ok && metrics.Requests >= p.cfg.Monitor.MinSamples {
		r.baseline = rate
	} else {
		r.lowTraffic = true
	}
	if r.lowTraffic && p.cfg.Synthetic == nil {
		return nil, holdf("real traffic is too low to judge the release and the application has no synthetic suite")
	}
	return r, nil
}

// start stages the artifact, checks its preview, and writes the start of the
// rollout under the rollback preflight. Staging sends no traffic, so a
// failure here leaves users on the LKG and the issue in ReleaseCandidate.
func (p *Pipeline) start(ctx context.Context, r *rollout) error {
	staged, err := p.cfg.Backend.Stage(ctx, r.v.artifact)
	if err != nil {
		return holdf("the candidate could not be staged").WithCause(err)
	}
	if staged.Unit != r.unit || staged.Digest != r.v.artifact.Digest {
		return core.NewError(core.CodeTampered, "the backend staged another build than the verified artifact")
	}
	r.rev = staged
	caps, err := p.cfg.Backend.Capabilities(ctx)
	if err != nil {
		return holdf("the deployment backend cannot say what it supports").WithCause(err)
	}
	if caps.Supports(core.CapDeployPreview) {
		if _, err := p.cfg.Backend.Preview(ctx, staged); err != nil {
			return holdf("the staged candidate has no preview").WithCause(err)
		}
	}
	proof := statemachine.RolloutProof{
		IssueID: r.h.record.IssueID, RunID: r.h.record.RunID, FactoryID: p.cfg.FactoryID,
		Artifact: r.v.artifact, RollbackTarget: r.lkgRev, RollbackVerified: true, MetricsAvailable: true,
	}
	return p.step(ctx, r.h, stepSpec{
		event: core.EventRolloutStarted, reason: "the rollback preflight passed",
		candidate: r.h.record.CandidateDigest, release: r.v.artifact.Digest,
		guards: statemachine.Guards{Rollout: statemachine.RolloutGuardFunc(func(context.Context, core.Transition) (statemachine.RolloutProof, error) {
			return proof, nil
		})},
	})
}

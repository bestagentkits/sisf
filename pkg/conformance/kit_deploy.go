package conformance

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// DeployProbe is what the kit needs to exercise a deployment backend.
type DeployProbe struct {
	// Baseline and Candidate are two builds of the same unit. The kit
	// publishes the baseline, then the candidate, then returns to the baseline.
	Baseline  deploy.Artifact
	Candidate deploy.Artifact
	// Window is the span metrics are read over.
	Window time.Duration
	// EndToEndProven records that the backend also passed an end-to-end run.
	EndToEndProven bool
}

// GradeDeploy grades a deployment backend. It starts from the backend's
// declaration and then performs one release and one rollback, judging every
// step by the revision observed running and never by the call that asked for
// it. Each step the backend fails lowers the area that step proves.
//
// An error means the backend could not be graded at all.
func GradeDeploy(ctx context.Context, backend deploy.Backend, probe DeployProbe) (Row, error) {
	if backend == nil {
		return Row{}, core.NewError(core.CodeInvalidArgument, "no deployment backend was given to grade")
	}
	if err := probe.validate(); err != nil {
		return Row{}, err
	}
	declared, err := backend.Capabilities(ctx)
	if err != nil {
		return Row{}, undeclared(backend.Name(), err)
	}
	row := Grade(Declaration{Kind: KindDeploy, Name: backend.Name(), Capabilities: declared, EndToEndProven: probe.EndToEndProven})
	unit := probe.Baseline.Unit

	// running reports whether the observed revision is rev, and why not.
	running := func(rev deploy.Revision) (bool, string) {
		observed, err := backend.ObservedRevision(ctx, unit)
		if err != nil {
			return false, "the running revision could not be observed: " + err.Error()
		}
		return observed.Matches(rev), "the observed revision is not the expected one, or is not healthy"
	}

	baseline, err := backend.Stage(ctx, probe.Baseline)
	if err == nil {
		err = backend.Promote(ctx, baseline, 100)
	}
	if err != nil {
		row.lower(core.CapLifecycle, "the baseline could not be staged and promoted: "+err.Error())
		return row, nil
	}
	if ok, why := running(baseline); !ok {
		row.lower(core.CapObservability, "after promoting the baseline, "+why)
		return row, nil
	}

	candidate, err := backend.Stage(ctx, probe.Candidate)
	if err != nil {
		row.lower(core.CapLifecycle, "the candidate could not be staged: "+err.Error())
		return row, nil
	}
	// Staging must not disturb what serves traffic.
	if live, err := backend.ObservedRevision(ctx, unit); err != nil {
		row.lower(core.CapObservability, "the running revision could not be observed while a candidate was staged: "+err.Error())
	} else if !live.Matches(baseline) {
		row.lower(core.CapIsolation, "staging the candidate changed the revision that serves traffic")
	}
	if err := backend.Promote(ctx, candidate, 100); err != nil {
		row.lower(core.CapLifecycle, "the candidate could not be promoted: "+err.Error())
		return row, nil
	}
	if ok, why := running(candidate); !ok {
		row.lower(core.CapObservability, "after promoting the candidate, "+why)
	}
	if _, err := backend.Measure(ctx, candidate, probe.Window); err != nil {
		row.lower(core.CapObservability, "the candidate could not be measured: "+err.Error())
	}

	if err := backend.Rollback(ctx, baseline); err != nil {
		row.lower(core.CapRollback, "the rollback failed: "+err.Error())
		return row, nil
	}
	// A rollback that returned without error proves nothing until the last
	// known good revision is seen running again.
	if ok, why := running(baseline); !ok {
		row.lower(core.CapRollback, "after the rollback, "+why)
	}
	return row, nil
}

// validate requires two signed builds of one unit that differ in content and
// a positive measuring window.
func (p DeployProbe) validate() error {
	if err := p.Baseline.Validate(); err != nil {
		return err
	}
	if err := p.Candidate.Validate(); err != nil {
		return err
	}
	switch {
	case p.Baseline.Unit != p.Candidate.Unit:
		return core.NewError(core.CodeInvalidArgument, "the baseline and the candidate must be builds of the same unit")
	case p.Baseline.Digest == p.Candidate.Digest:
		return core.NewError(core.CodeInvalidArgument, "the baseline and the candidate must differ, or a rollback cannot be told from a release")
	case p.Window <= 0:
		return core.NewError(core.CodeInvalidArgument, "the measuring window must be positive")
	}
	return nil
}

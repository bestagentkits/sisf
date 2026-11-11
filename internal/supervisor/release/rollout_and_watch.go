package release

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// roll moves traffic to the release step by step, watching each partial
// step, then observes the release taking all traffic and watches it through
// the observation window. It returns why the release must be restored to
// the LKG, if it must, and an error when it can neither go on nor be
// judged.
func (p *Pipeline) roll(ctx context.Context, r *rollout) (*undo, error) {
	steps := []int{100}
	if r.canary {
		steps = p.cfg.CanarySteps
	}
	for _, percent := range steps {
		if p.frozen(ctx) {
			return stoppedByEmergency(), nil
		}
		if err := p.cfg.Backend.Promote(ctx, r.rev, percent); err != nil {
			return &undo{cause: statemachine.BlockReleaseInterrupted, reason: "traffic could not be moved to the release: " + err.Error()}, nil
		}
		if percent < 100 {
			if why, err := p.watch(ctx, r, percent, p.cfg.StepWindows, false); why != nil || err != nil {
				return why, err
			}
		}
	}
	if why, err := p.takeAllTraffic(ctx, r); why != nil || err != nil {
		return why, err
	}
	return p.watch(ctx, r, 100, p.cfg.ObservationWindows, true)
}

// stoppedByEmergency is the undo of a release an emergency stop froze
// during its rollout.
func stoppedByEmergency() *undo {
	return &undo{cause: statemachine.BlockReleaseInterrupted, reason: "an emergency stop froze publishing during the rollout"}
}

// unjudged is the undo of a release that could not be judged while it
// served traffic.
func unjudged(reason string) *undo {
	return &undo{cause: statemachine.BlockReleaseUnjudged, reason: reason}
}

// takeAllTraffic proves the release serves all traffic: the revision read
// from the running system, in an observation made since the rollout
// started, is the release, healthy, and its metrics can be read. Only then
// is the rollout written as healthy.
func (p *Pipeline) takeAllTraffic(ctx context.Context, r *rollout) (*undo, error) {
	observed, err := p.cfg.Backend.ObservedRevision(ctx, r.unit)
	if err != nil {
		return unjudged("the release cannot be observed once it took all traffic"), nil
	}
	r.observed = observed
	if !observed.Matches(r.rev) {
		return judgedUnhealthy("the revision observed running is not the release, or it is not healthy"), nil
	}
	started := rolloutStartedAt(r.h.record)
	if observed.ObservedAt.Before(started) {
		return unjudged("the running system answered with an observation made before the rollout, so the release cannot be observed"), nil
	}
	metrics, err := p.cfg.Backend.Measure(ctx, r.rev, p.cfg.Monitor.Window)
	if err != nil {
		return unjudged("the release cannot be measured once it took all traffic"), nil
	}
	proof := statemachine.CanaryProof{
		IssueID: r.h.record.IssueID, RunID: r.h.record.RunID, Released: r.rev.Digest, Observed: observed.Digest,
		MetricsAvailable: true, Healthy: metrics.Healthy && metrics.CriticalErrors == 0, ObservedAt: observed.ObservedAt,
		RolloutStartedAt: started,
	}
	if !proof.Healthy {
		return judgedUnhealthy("critical health: the release failed its health check once it took all traffic"), nil
	}
	return nil, p.step(ctx, r.h, stepSpec{
		event: core.EventRolloutHealthy, reason: "the release is observed serving all traffic", release: r.rev.Digest,
		evidence: []string{"observed:" + string(observed.Digest)},
		guards: statemachine.Guards{Canary: statemachine.CanaryGuardFunc(func(context.Context, core.Transition) (statemachine.CanaryProof, error) {
			return proof, nil
		})},
	})
}

// rolloutStartedAt is when the store wrote the start of the rollout the
// record is in: the record's own stamp, written with the step into Canary,
// which no later write changes while the record stays there. A record in
// any other state yields no instant, and the canary guard refuses it.
func rolloutStartedAt(record core.StateRecord) time.Time {
	if record.State != core.StateCanary {
		return time.Time{}
	}
	return record.UpdatedAt
}

// watch observes the release window by window at one traffic share. It
// watches windows windows; in the observation phase a release judged under
// low traffic is held until the synthetic hold has lasted MinHold, plus one
// window for each failed real request.
func (p *Pipeline) watch(ctx context.Context, r *rollout, percent, windows int, observing bool) (*undo, error) {
	started := p.cfg.Clock.Now()
	for i := 0; ; i++ {
		if i >= windows {
			need := p.cfg.LowTraffic.MinHold + time.Duration(r.extensions)*p.cfg.Monitor.Window
			if !observing || !r.lowTraffic || p.cfg.Clock.Now().Sub(started) >= need {
				return nil, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.cfg.Clock.After(p.cfg.Monitor.Window):
		}
		if p.frozen(ctx) {
			return stoppedByEmergency(), nil
		}
		if err := p.renew(ctx, r.h); err != nil {
			return nil, err
		}
		metrics, err := p.cfg.Backend.Measure(ctx, r.rev, p.cfg.Monitor.Window)
		if err != nil {
			return unjudged("the release cannot be measured any more, so it cannot be judged"), nil
		}
		if percent < 100 {
			p.refreshBaseline(ctx, r)
		}
		report, decision := r.monitor.Judge(metrics, r.baseline)
		report.Percent = percent
		var why *undo
		switch decision {
		case DecisionRollback:
			why = judgedUnhealthy(report.Note)
		case DecisionLowTraffic:
			why, err = p.judgeLowTraffic(ctx, r, &report, metrics)
		}
		r.windows = append(r.windows, report)
		if why != nil || err != nil {
			return why, err
		}
	}
}

// refreshBaseline takes the error rate of the revision still serving the
// rest of the traffic as the baseline, when it served enough requests.
func (p *Pipeline) refreshBaseline(ctx context.Context, r *rollout) {
	stable, err := p.cfg.Backend.Measure(ctx, r.lkgRev, p.cfg.Monitor.Window)
	if err != nil {
		return
	}
	if rate, ok := stable.ErrorRate(); ok && stable.Requests >= p.cfg.Monitor.MinSamples {
		r.baseline = rate
	}
}

// judgeLowTraffic judges a window with too few real requests by the
// synthetic suite. A failed synthetic run restores the LKG as a release
// judged unhealthy; a suite that could not be run judged nothing, so the
// LKG is restored as for a release that could not be judged, and a
// supervisor that is stopping leaves it to the recovery. A failed real
// request does not restore the LKG, but extends the hold and is kept as
// evidence.
func (p *Pipeline) judgeLowTraffic(ctx context.Context, r *rollout, report *WindowReport, metrics deploy.Metrics) (*undo, error) {
	r.lowTraffic = true
	if p.cfg.Synthetic == nil {
		return unjudged("real traffic is too low to judge the release and the application has no synthetic suite"), nil
	}
	result, err := p.cfg.Synthetic.Run(ctx, r.rev)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		report.Synthetic = &SyntheticResult{Detail: "the synthetic suite could not be run"}
		return unjudged("the synthetic suite could not be run during the low-traffic hold, so the release could not be judged: " +
			string(core.CodeOf(err))), nil
	}
	report.Synthetic = &result
	if !result.Passed {
		return judgedUnhealthy("a synthetic check failed during the low-traffic hold"), nil
	}
	if metrics.ServerErrors > 0 {
		r.extensions++
		report.Note += "; a real request failed, which extends the hold by one window"
		if r.extensions > p.cfg.LowTraffic.MaxExtensions {
			return judgedUnhealthy("real requests kept failing through the low-traffic hold"), nil
		}
	}
	return nil, nil
}

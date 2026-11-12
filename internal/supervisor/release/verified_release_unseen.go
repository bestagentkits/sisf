package release

import (
	"context"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
)

// waitsUnseen words what finishing waits on while a verified release is
// not observed serving, healthy.
const waitsUnseen = "the running system does not show the verified release serving, healthy, nor another release in its place; " +
	"it is observed again on every pass, for as long as a release is watched before it is verified"

// unseenPoll is how often a release that is not seen is observed again
// while the supervisor starts (RecordVerifiedLKGs).
const unseenPoll = 5 * time.Second

// unseenWait is how long a verified release that is not observed serving,
// healthy, without another release observed serving in its place, waits
// before it is blocked: as long as a release is watched before it is
// verified, the observation windows of the rollout.
func (p *Pipeline) unseenWait() time.Duration {
	return p.cfg.Monitor.Window * time.Duration(p.cfg.ObservationWindows)
}

// waitUnseen holds a verified release the running system does not show
// serving, healthy (lkgUnseen), and observes it again on later passes. A
// probe that failed or found nothing, or the release answering its health
// check as down for a moment, does not block a healthy release. Once it was
// not seen for unseenWait since this process first found it so, it is
// blocked as not judged, the LKG restored first (blockVerified). The wait
// starts again whenever the release is seen serving, healthy, even by a
// pass that fails later (settleLKG), and on every pass an emergency stop
// skips (FinishVerified). It is kept by this process only: it starts again
// when the supervisor does, and no document of the control zone can
// shorten or end it.
func (p *Pipeline) waitUnseen(ctx context.Context, h *holding, unit string, d lkgDecision) (Outcome, error) {
	wait := p.unseenWait()
	if p.unseenFor(h.record.RunID) < wait {
		err := core.NewError(core.CodeUnavailable, d.reason+"; it is observed again until it is, or another release is")
		return Outcome{Release: h.record.ReleaseDigest, State: h.record.State, Result: ResultHeld, Observed: d.observed,
			Reason: err.Error(), Waits: waitsUnseen}, err
	}
	d.verdict = lkgUnjudged
	d.reason += " for " + wait.String() + ", the length of the observation windows, and no later release replaced it"
	out, err := p.blockVerified(ctx, h, unit, d)
	if err == nil {
		p.seen(h.record.RunID)
	}
	return out, err
}

// unseenFor is how long this process has not seen the verified release of
// run, counted from now when this is the first time.
func (p *Pipeline) unseenFor(run core.RunID) time.Duration {
	now := p.cfg.Clock.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	since, waiting := p.unseenSince[run]
	if !waiting {
		since = now
		p.unseenSince[run] = now
	}
	return now.Sub(since)
}

// seen forgets that the verified release of run was not seen.
func (p *Pipeline) seen(run core.RunID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.unseenSince, run)
}

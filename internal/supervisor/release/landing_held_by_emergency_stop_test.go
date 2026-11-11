package release

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/bestagentkits/sisf/internal/catalog"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/state"
)

// countingLander merges every landing it is asked for and counts them.
type countingLander struct{ lands atomic.Int32 }

func (l *countingLander) Land(context.Context, LandRequest) (Landing, error) {
	l.lands.Add(1)
	return Landing{Merged: true, Summary: "the change is merged"}, nil
}

// freezingPublisher publishes, then an emergency stop freezes the factory,
// as if it was called between the publication and the merge.
type freezingPublisher struct {
	fakePublisher
	freeze *fakeFreeze
}

func (p *freezingPublisher) Publish(ctx context.Context, e catalog.Entry) (string, error) {
	ref, err := p.fakePublisher.Publish(ctx, e)
	p.freeze.frozen.Store(true)
	return ref, err
}

// An emergency stop that lands between a verified release's publication
// and its merge holds the issue in Verified with its landing due. Once
// the stop is lifted, the landing is finished from that record: merged
// once, then the issue closes.
func TestALandingHeldByAnEmergencyStopIsFinishedOnceTheStopIsLifted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newWorld(t)
	lander := &countingLander{}
	w.cfg.Lander = lander
	w.cfg.Publisher = &freezingPublisher{freeze: w.freeze}
	issue, run := issueRef(30), core.RunID("run-30")
	w.seedReleaseCandidate(issue, run, firstBuild)

	out, err := w.pipeline().Release(ctx, w.request(issue, run, firstBuild))
	if !core.HasCode(err, core.CodeFrozen) || out.Result != ResultHeld {
		t.Fatalf("Release = %+v, %v; want held by the emergency stop", out, err)
	}
	w.requireState(issue, core.StateVerified)
	var due LandRequest
	if err := w.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		var err error
		due, err = ReadLandingDue(ctx, tx, run)
		return err
	}); err != nil || due.Release != core.ReleaseDigest(firstBuild) {
		t.Fatalf("the landing due is %+v, %v", due, err)
	}
	if lander.lands.Load() != 0 {
		t.Fatal("a landing ran while the factory was frozen")
	}

	w.freeze.frozen.Store(false)
	out, err = w.pipeline().FinishVerified(ctx, due)
	if err != nil || out.Result != ResultClosed {
		t.Fatalf("FinishVerified = %+v, %v", out, err)
	}
	w.requireState(issue, core.StateClosed)
	if lander.lands.Load() != 1 {
		t.Fatalf("landed %d times", lander.lands.Load())
	}
}

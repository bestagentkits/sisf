package release

import (
	"context"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// fakeClock is a core.Clock moved only by the code under test: waiting for a
// duration moves it forward by that duration at once, so no test sleeps.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	fired := make(chan time.Time, 1)
	fired <- c.Advance(d)
	return fired
}

// Advance moves the clock forward by d and returns the new instant.
func (c *fakeClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

// fakeBackend is an in-process deployment backend. The edge it models sends
// totalPerWindow responses per window, split between the stable revision
// and the canary; each revision fails errorRate of them. What it reports as
// observed is the stable revision as the running system would say, unless a
// test makes the running system disagree with what the calls answered.
type fakeBackend struct {
	mu    sync.Mutex
	clock *fakeClock
	caps  core.CapabilitySet

	stable, canary core.ReleaseDigest
	percent        int
	existing       map[core.ReleaseDigest]bool
	totalPerWindow int64
	errorRate      map[core.ReleaseDigest]float64
	unhealthy      map[core.ReleaseDigest]bool
	critical       map[core.ReleaseDigest]int64
	// runningInstead, when set, is what the running system reports whatever
	// the calls answered.
	runningInstead core.ReleaseDigest
	// observedAt, when set, is the instant every observation is dated with,
	// as a backend that answers from a stale cache would date it.
	observedAt time.Time

	capsErr, observeErr, stageErr, rollbackErr error
	measureErr                                 map[core.ReleaseDigest]error

	// onMeasure, when set, is called, without the lock, before each
	// measurement, so a test can act in the middle of a rollout.
	onMeasure func(rev core.ReleaseDigest)

	// dataVolume stands for the application's stored data; no call changes it.
	dataVolume string
	promotions []int
	rollbacks  []core.ReleaseDigest
	staged     []core.ReleaseDigest
}

func newFakeBackend(clock *fakeClock, stable core.ReleaseDigest) *fakeBackend {
	return &fakeBackend{
		clock: clock, stable: stable, existing: map[core.ReleaseDigest]bool{stable: true},
		caps: core.CapabilitySet{
			core.CapDeployPreview: core.SupportSupported, core.CapDeployBlueGreen: core.SupportSupported,
			core.CapDeployCanary: core.SupportSupported, core.CapDeployMetrics: core.SupportSupported,
			core.CapDeployObservedRevision: core.SupportSupported, core.CapDeployRollback: core.SupportSupported,
		},
		totalPerWindow: 10000, errorRate: map[core.ReleaseDigest]float64{}, unhealthy: map[core.ReleaseDigest]bool{},
		critical: map[core.ReleaseDigest]int64{}, measureErr: map[core.ReleaseDigest]error{}, dataVolume: "customer-data-v1",
	}
}

func (b *fakeBackend) Name() string { return "fake" }

func (b *fakeBackend) Capabilities(context.Context) (core.CapabilitySet, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.caps, b.capsErr
}

func (b *fakeBackend) Stage(_ context.Context, a deploy.Artifact) (deploy.Revision, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stageErr != nil {
		return deploy.Revision{}, b.stageErr
	}
	b.existing[a.Digest] = true
	b.staged = append(b.staged, a.Digest)
	return deploy.Revision{Unit: a.Unit, Digest: a.Digest, ID: "slot-" + string(a.Digest)[7:15]}, nil
}

func (b *fakeBackend) Preview(_ context.Context, rev deploy.Revision) (deploy.Preview, error) {
	return deploy.Preview{Revision: rev, URL: "http://preview.invalid"}, nil
}

// share is the part of the window's responses rev served.
func (b *fakeBackend) share(rev core.ReleaseDigest) int64 {
	switch {
	case rev == b.canary && b.canary != "":
		return b.totalPerWindow * int64(b.percent) / 100
	case rev == b.stable && b.canary != "":
		return b.totalPerWindow * int64(100-b.percent) / 100
	case rev == b.stable:
		return b.totalPerWindow
	}
	return 0
}

func (b *fakeBackend) Measure(_ context.Context, rev deploy.Revision, window time.Duration) (deploy.Metrics, error) {
	b.mu.Lock()
	hook := b.onMeasure
	b.mu.Unlock()
	if hook != nil {
		hook(rev.Digest)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.measureErr[rev.Digest]; err != nil {
		return deploy.Metrics{}, err
	}
	served := b.share(rev.Digest)
	now := b.clock.Now()
	return deploy.Metrics{
		Revision: rev, From: now.Add(-window), To: now, Requests: served,
		ServerErrors: int64(float64(served) * b.errorRate[rev.Digest]),
		Healthy:      !b.unhealthy[rev.Digest], CriticalErrors: b.critical[rev.Digest],
	}, nil
}

func (b *fakeBackend) Promote(_ context.Context, rev deploy.Revision, percent int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.promotions = append(b.promotions, percent)
	if percent == 100 {
		b.stable, b.canary, b.percent = rev.Digest, "", 0
		return nil
	}
	b.canary, b.percent = rev.Digest, percent
	return nil
}

func (b *fakeBackend) ObservedRevision(_ context.Context, unit string) (deploy.Observed, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.observeErr != nil {
		return deploy.Observed{}, b.observeErr
	}
	running := b.stable
	if b.runningInstead != "" {
		running = b.runningInstead
	}
	at := b.clock.Now()
	if !b.observedAt.IsZero() {
		at = b.observedAt
	}
	return deploy.Observed{Unit: unit, Digest: running, Healthy: !b.unhealthy[running], ObservedAt: at}, nil
}

func (b *fakeBackend) Rollback(_ context.Context, to deploy.Revision) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rollbackErr != nil {
		return b.rollbackErr
	}
	if !b.existing[to.Digest] {
		return core.NewError(core.CodeReleaseHold, "the revision to roll back to no longer exists")
	}
	b.rollbacks = append(b.rollbacks, to.Digest)
	b.stable, b.canary, b.percent, b.runningInstead = to.Digest, "", 0, ""
	return nil
}

// snapshot returns what a test asserts on, read under the lock.
func (b *fakeBackend) snapshot() (promotions []int, rollbacks, staged []core.ReleaseDigest, data string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int(nil), b.promotions...), append([]core.ReleaseDigest(nil), b.rollbacks...),
		append([]core.ReleaseDigest(nil), b.staged...), b.dataVolume
}

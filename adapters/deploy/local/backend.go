package local

import (
	"context"
	"regexp"
	"sync"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
	"github.com/bestagentkits/sisf/pkg/deploy/healthprobe"
)

var unitPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// slot is one of the two places a revision can run.
type slot struct {
	name   string
	port   int
	digest core.ReleaseDigest
	proc   Process
}

// Backend is the Standalone deployment backend. It is safe for concurrent
// use. It keeps its processes in memory, so a restart of the owner of the
// backend loses the handles. With a state directory it also records each
// slot's process there, and StopStale stops what an earlier backend left
// running before the next Stage reuses its port.
type Backend struct {
	cfg  Config
	ops  sync.Mutex // one mutation at a time
	mu   sync.Mutex // guards slots
	slot [2]*slot
}

var _ deploy.Backend = (*Backend)(nil)

// New validates the configuration and returns the backend. It starts nothing.
func New(cfg Config) (*Backend, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Backend{cfg: cfg, slot: [2]*slot{{name: "blue", port: cfg.Ports[0]}, {name: "green", port: cfg.Ports[1]}}}, nil
}

// Name identifies the backend.
func (*Backend) Name() string { return "local" }

// Capabilities declares blue-green with a preview, metrics, an observable
// revision and rollback. Canary is not declared: the edge could split, but
// this backend promotes only whole revisions.
func (*Backend) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{
		core.CapDeployPreview: core.SupportSupported, core.CapDeployBlueGreen: core.SupportSupported,
		core.CapDeployMetrics: core.SupportSupported, core.CapDeployObservedRevision: core.SupportSupported,
		core.CapDeployRollback: core.SupportSupported,
	}, nil
}

func hold(msg string, cause error) error {
	e := core.NewError(core.CodeReleaseHold, msg)
	if cause != nil {
		e = e.WithCause(cause)
	}
	return e
}

func checkRevision(rev deploy.Revision) error {
	if !unitPattern.MatchString(rev.Unit) {
		return core.NewError(core.CodeInvalidArgument, "the unit name must be lowercase letters, digits and dashes")
	}
	return rev.Digest.Validate()
}

func (b *Backend) url(s slot) string { return "http://" + b.cfg.addr(s.port) }

func (b *Backend) target(s slot) edge.Target {
	return edge.Target{Revision: string(s.digest), URL: b.url(s)}
}

// slotOf returns a snapshot of the slot that holds the digest.
func (b *Backend) slotOf(digest core.ReleaseDigest) (slot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.slot {
		if s.proc != nil && s.digest == digest {
			return *s, true
		}
	}
	return slot{}, false
}

// Stage starts the artifact on the slot that does not serve traffic and
// waits until the application itself reports that revision healthy. Whatever
// ran on that slot before, the previous revision included, is stopped.
func (b *Backend) Stage(ctx context.Context, a deploy.Artifact) (deploy.Revision, error) {
	if err := a.ValidateFor(b.cfg.Factory); err != nil {
		return deploy.Revision{}, err
	}
	if !unitPattern.MatchString(a.Unit) {
		return deploy.Revision{}, core.NewError(core.CodeInvalidArgument, "the unit name must be lowercase letters, digits and dashes")
	}
	b.ops.Lock()
	defer b.ops.Unlock()
	split, err := b.cfg.Edge.Split(ctx)
	if err != nil {
		return deploy.Revision{}, hold("the edge could not say what it serves", err)
	}
	s := b.idleSlot(core.ReleaseDigest(split.Stable.Revision))
	if err := b.stopSlot(ctx, s); err != nil {
		return deploy.Revision{}, hold("the previous process on the idle slot could not be stopped", err)
	}
	proc, err := b.cfg.Starter.Start(ctx, StartSpec{
		Unit: a.Unit, Image: a.Image, Addr: b.cfg.addr(s.port),
		Env: append([]string{RevisionEnv + "=" + string(a.Digest)}, b.cfg.Env...),
	})
	if err != nil {
		return deploy.Revision{}, core.NewError(core.CodeUnavailable, "the process could not be started: "+err.Error()).WithCause(err)
	}
	b.mu.Lock()
	s.digest, s.proc = a.Digest, proc
	b.mu.Unlock()
	b.recordSlot(ctx, s, a.Unit, a.Digest, proc)
	rev := deploy.Revision{Unit: a.Unit, Digest: a.Digest, ID: s.name}
	if err := b.waitReady(ctx, rev); err != nil {
		if stopErr := b.stopSlot(context.WithoutCancel(ctx), s); stopErr != nil {
			b.cfg.Logger.Warn("a failed revision could not be stopped", "slot", s.name, "error", stopErr.Error())
		}
		return deploy.Revision{}, err
	}
	return rev, nil
}

// idleSlot is the slot that does not hold the stable revision.
func (b *Backend) idleSlot(stable core.ReleaseDigest) *slot {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slot[0].proc != nil && b.slot[0].digest == stable {
		return b.slot[1]
	}
	return b.slot[0]
}

// stopSlot stops the process of a slot and empties it.
func (b *Backend) stopSlot(ctx context.Context, s *slot) error {
	b.mu.Lock()
	proc := s.proc
	b.mu.Unlock()
	if proc == nil {
		return nil
	}
	if err := proc.Stop(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	s.proc, s.digest = nil, ""
	b.mu.Unlock()
	b.forgetSlot(s)
	return nil
}

// observe reads what runs as rev from the backend's record of the process
// and from the application's own answer. Observed.Digest is what the
// application says it is; Healthy needs the process alive, the record to name
// rev and the application to be healthy and to say the same revision.
func (b *Backend) observe(ctx context.Context, rev deploy.Revision) (deploy.Observed, error) {
	obs := deploy.Observed{Unit: rev.Unit, ObservedAt: b.cfg.Clock.Now()}
	s, ok := b.slotOf(rev.Digest)
	if !ok || !s.proc.Alive() {
		return obs, nil
	}
	health, err := healthprobe.Probe(ctx, b.cfg.HTTP, b.url(s))
	if err != nil {
		if ctx.Err() != nil {
			return obs, ctx.Err()
		}
		return obs, nil
	}
	obs.Digest = core.ReleaseDigest(health.Revision)
	obs.Healthy = health.Healthy && health.Revision != "" && health.Revision == string(rev.Digest)
	return obs, nil
}

// waitReady polls until the application reports the revision healthy. A
// healthy application that names another revision fails at once.
func (b *Backend) waitReady(ctx context.Context, rev deploy.Revision) error {
	deadline := b.cfg.Clock.Now().Add(b.cfg.ReadyTimeout)
	for {
		obs, err := b.observe(ctx, rev)
		switch {
		case err == nil && obs.Matches(rev):
			return nil
		case err == nil && obs.Digest != "" && obs.Digest != rev.Digest:
			return hold("the staged application reports another revision than the one asked for", nil)
		case ctx.Err() != nil:
			return ctx.Err()
		}
		if !b.cfg.Clock.Now().Before(deadline) {
			return hold("the staged revision did not report itself healthy in time", err)
		}
		select {
		case <-b.cfg.Clock.After(b.cfg.PollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Preview says where a staged revision can be reached.
func (b *Backend) Preview(ctx context.Context, rev deploy.Revision) (deploy.Preview, error) {
	if err := checkRevision(rev); err != nil {
		return deploy.Preview{}, err
	}
	if err := b.requireRunning(ctx, rev); err != nil {
		return deploy.Preview{}, err
	}
	s, ok := b.slotOf(rev.Digest)
	if !ok {
		return deploy.Preview{}, hold("the revision is not running", nil)
	}
	return deploy.Preview{Revision: rev, URL: b.url(s)}, nil
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

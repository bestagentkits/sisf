package compose

import (
	"context"
	"errors"

	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// Backend is the Compose deployment backend. It is safe for concurrent use.
type Backend struct {
	cfg Config
	api *dockerAPI
}

var _ deploy.Backend = (*Backend)(nil)

// New validates the configuration and returns the backend. It does not
// contact Docker.
func New(cfg Config) (*Backend, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	api, err := newDockerAPI(cfg.Endpoint, cfg.APIVersion)
	if err != nil {
		return nil, err
	}
	return &Backend{cfg: cfg, api: api}, nil
}

// Name identifies the backend.
func (*Backend) Name() string { return "compose" }

// Capabilities declares what this backend proves by tests: a preview, a
// switch with a way back in both forms, an observable revision and metrics.
func (*Backend) Capabilities(context.Context) (core.CapabilitySet, error) {
	return core.CapabilitySet{
		core.CapDeployPreview: core.SupportSupported, core.CapDeployBlueGreen: core.SupportSupported,
		core.CapDeployCanary: core.SupportSupported, core.CapDeployMetrics: core.SupportSupported,
		core.CapDeployObservedRevision: core.SupportSupported, core.CapDeployRollback: core.SupportSupported,
	}, nil
}

// target is where the edge reaches a revision.
func (b *Backend) target(rev deploy.Revision) edge.Target {
	return edge.Target{Revision: string(rev.Digest), URL: b.cfg.Resolve(b.containerName(rev.Unit, rev.Digest))}
}

// checkRevision refuses a revision whose unit or digest could not name a
// container, before anything is derived from it.
func checkRevision(rev deploy.Revision) error {
	if !unitPattern.MatchString(rev.Unit) {
		return core.NewError(core.CodeInvalidArgument, "the unit name must be lowercase letters, digits and dashes")
	}
	return rev.Digest.Validate()
}

// hold is the error of a release that cannot be judged.
func hold(msg string, cause error) error {
	e := core.NewError(core.CodeReleaseHold, msg)
	if cause != nil {
		e = e.WithCause(cause)
	}
	return e
}

// Stage starts the artifact next to the running revision and waits until the
// application itself reports that revision healthy. It sends no traffic.
func (b *Backend) Stage(ctx context.Context, a deploy.Artifact) (deploy.Revision, error) {
	if err := a.ValidateFor(b.cfg.Factory); err != nil {
		return deploy.Revision{}, err
	}
	if !unitPattern.MatchString(a.Unit) {
		return deploy.Revision{}, core.NewError(core.CodeInvalidArgument, "the unit name must be lowercase letters, digits and dashes")
	}
	if !pinned(a.Image) {
		return deploy.Revision{}, core.NewError(core.CodeInvalidArgument, "the image must be pinned by digest")
	}
	rev := deploy.Revision{Unit: a.Unit, Digest: a.Digest, ID: b.containerName(a.Unit, a.Digest)}
	if err := b.ensureRunning(ctx, rev, a); err != nil {
		return deploy.Revision{}, err
	}
	if err := b.waitReady(ctx, rev); err != nil {
		if cleanup := errors.Join(b.api.stop(context.WithoutCancel(ctx), rev.ID), b.api.remove(context.WithoutCancel(ctx), rev.ID)); cleanup != nil {
			b.cfg.Logger.Warn("a failed revision could not be removed", "container", rev.ID, "error", cleanup.Error())
		}
		return deploy.Revision{}, err
	}
	return rev, nil
}

// ensureRunning creates and starts the revision's container, or starts the
// one a previous Stage left, provided it was made for this digest.
func (b *Backend) ensureRunning(ctx context.Context, rev deploy.Revision, a deploy.Artifact) error {
	existing, err := b.api.inspect(ctx, rev.ID)
	switch {
	case core.HasCode(err, core.CodeNotFound):
		if err := b.api.create(ctx, rev.ID, b.spec(a)); err != nil {
			return err
		}
	case err != nil:
		return err
	case existing.Config.Labels[LabelDigest] != string(a.Digest) || existing.Config.Labels[LabelFactory] != string(b.cfg.Factory):
		return core.NewError(core.CodeConflict, "a container of that name exists for another revision")
	}
	return b.api.start(ctx, rev.ID)
}

// waitReady polls the application until it reports the revision healthy. A
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

// Preview says where a staged revision can be reached. It is refused for a
// revision that is not running and healthy as itself.
func (b *Backend) Preview(ctx context.Context, rev deploy.Revision) (deploy.Preview, error) {
	if err := checkRevision(rev); err != nil {
		return deploy.Preview{}, err
	}
	obs, err := b.observe(ctx, rev)
	if err != nil {
		return deploy.Preview{}, hold("the revision could not be observed", err)
	}
	if !obs.Matches(rev) {
		return deploy.Preview{}, hold("the revision is not running and healthy", nil)
	}
	return deploy.Preview{Revision: rev, URL: b.cfg.Resolve(b.containerName(rev.Unit, rev.Digest))}, nil
}

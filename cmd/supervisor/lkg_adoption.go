package main

import (
	"context"
	"log/slog"

	"github.com/bestagentkits/sisf/internal/supervisor/release"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// adoptedUnit is the unit whose last known good release the supervisor
// starts itself under the local deploy backend.
const adoptedUnit = "app"

// lkgAdopter starts the last known good release of the application through
// the local deploy backend. That backend keeps its processes in memory, so
// it can only observe, measure and roll back to a process it started: the
// way back of the first release exists only once the supervisor has
// started the LKG itself and the edge sends traffic to it.
type lkgAdopter struct {
	factory core.FactoryID
	backend deploy.Backend
	lkg     *release.LKGBook
	signer  core.Signer
	// image names the LKG for the local program, pinned by the LKG's
	// digest, when the book records no image: the release the Owner signed
	// at install pins a digest only.
	image string
}

// staleStopper is a backend that can stop the application processes an
// earlier supervisor started and lost track of when it stopped.
type staleStopper interface {
	StopStale(ctx context.Context) error
}

// adopt stages the LKG and sends it all traffic, unless it is already
// observed serving the unit. A process an earlier supervisor started, which
// still holds a slot's port, is stopped first; only a process the backend
// recorded and still finds to be the same one is signalled.
func (a *lkgAdopter) adopt(ctx context.Context) error {
	lkg, found, err := a.lkg.Get(ctx, adoptedUnit)
	if err != nil {
		return err
	}
	if !found {
		return core.NewError(core.CodeNotFound, "unit "+adoptedUnit+" has no last known good release to start")
	}
	rev := deploy.Revision{Unit: adoptedUnit, Digest: lkg.Digest}
	if observed, err := a.backend.ObservedRevision(ctx, adoptedUnit); err == nil && observed.Matches(rev) {
		return nil
	}
	if stale, ok := a.backend.(staleStopper); ok {
		if err := stale.StopStale(ctx); err != nil {
			return err
		}
	}
	image := lkg.Image
	if image == "" {
		image = a.image + "@" + string(lkg.Digest)
	}
	artifact, err := supplychain.SignArtifact(ctx, a.signer, deploy.Artifact{
		FactoryID: a.factory, Unit: adoptedUnit, Digest: lkg.Digest, Image: image,
		// The LKG record, which only the supervisor writes, is the record
		// of where this build came from.
		SBOMRef: "lkg/" + lkg.Source + ":" + string(lkg.Digest), ProvenanceRef: "lkg/" + lkg.Source + ":" + string(lkg.Digest),
	})
	if err != nil {
		return err
	}
	staged, err := a.backend.Stage(ctx, artifact)
	if err != nil {
		return err
	}
	return a.backend.Promote(ctx, staged, 100)
}

// until adopts the LKG every interval until it succeeds, and reports
// whether it did before ctx ended.
func (a *lkgAdopter) until(ctx context.Context, l *lifecycleFlags, clock core.Clock, log *slog.Logger) bool {
	for {
		err := a.adopt(ctx)
		if err == nil {
			log.Info("the last known good release serves the application", "unit", adoptedUnit)
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		log.Warn("the last known good release could not be started yet", "code", string(core.CodeOf(err)), "error", err.Error())
		select {
		case <-ctx.Done():
			return false
		case <-clock.After(l.interval):
		}
	}
}

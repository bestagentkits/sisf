package conformancetest

import (
	"context"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// Deploy is an in-memory deployment backend. It keeps, per unit, the
// revision that serves traffic.
type Deploy struct {
	BackendName string
	Declared    core.CapabilitySet
	// Fail makes the named method ("Capabilities", "Stage", "Promote",
	// "Measure", "ObservedRevision", "Rollback") return the given error.
	Fail map[string]error
	// StageTakesTraffic makes staging switch traffic to the staged revision.
	StageTakesTraffic bool
	// RollbackDoesNothing makes Rollback report success and change nothing.
	RollbackDoesNothing bool

	mu   sync.Mutex
	live map[string]deploy.Revision
}

var _ deploy.Backend = (*Deploy)(nil)

// Name returns the backend's name.
func (d *Deploy) Name() string { return d.BackendName }

// Capabilities returns the declared set.
func (d *Deploy) Capabilities(context.Context) (core.CapabilitySet, error) {
	return d.Declared, d.Fail["Capabilities"]
}

// serve makes rev the revision that serves its unit.
func (d *Deploy) serve(rev deploy.Revision) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.live == nil {
		d.live = map[string]deploy.Revision{}
	}
	d.live[rev.Unit] = rev
}

// Stage prepares a revision of the artifact without sending it traffic.
func (d *Deploy) Stage(_ context.Context, artifact deploy.Artifact) (deploy.Revision, error) {
	if err := d.Fail["Stage"]; err != nil {
		return deploy.Revision{}, err
	}
	rev := deploy.Revision{Unit: artifact.Unit, Digest: artifact.Digest, ID: "slot-" + string(artifact.Digest)[7:15]}
	if d.StageTakesTraffic {
		d.serve(rev)
	}
	return rev, nil
}

// Preview returns where the staged revision can be reached.
func (d *Deploy) Preview(_ context.Context, rev deploy.Revision) (deploy.Preview, error) {
	return deploy.Preview{Revision: rev, URL: "http://127.0.0.1:0/" + rev.ID}, nil
}

// Measure returns metrics of a healthy revision.
func (d *Deploy) Measure(_ context.Context, rev deploy.Revision, window time.Duration) (deploy.Metrics, error) {
	if err := d.Fail["Measure"]; err != nil {
		return deploy.Metrics{}, err
	}
	now := time.Now()
	return deploy.Metrics{Revision: rev, From: now.Add(-window), To: now, Requests: 100, Healthy: true}, nil
}

// Promote switches all traffic to rev.
func (d *Deploy) Promote(_ context.Context, rev deploy.Revision, _ int) error {
	if err := d.Fail["Promote"]; err != nil {
		return err
	}
	d.serve(rev)
	return nil
}

// ObservedRevision reports the revision that serves the unit.
func (d *Deploy) ObservedRevision(_ context.Context, unit string) (deploy.Observed, error) {
	if err := d.Fail["ObservedRevision"]; err != nil {
		return deploy.Observed{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rev, ok := d.live[unit]
	if !ok {
		return deploy.Observed{}, core.NewError(core.CodeNotFound, "nothing serves this unit")
	}
	return deploy.Observed{Unit: unit, Digest: rev.Digest, Healthy: true, ObservedAt: time.Now()}, nil
}

// Rollback switches traffic back to an earlier revision.
func (d *Deploy) Rollback(_ context.Context, to deploy.Revision) error {
	if err := d.Fail["Rollback"]; err != nil {
		return err
	}
	if !d.RollbackDoesNothing {
		d.serve(to)
	}
	return nil
}

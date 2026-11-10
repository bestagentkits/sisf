package release

import (
	"context"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// Result is how a release ended.
type Result string

const (
	// ResultClosed: observed, verified, recorded as the LKG, published and closed.
	ResultClosed Result = "closed"
	// ResultBlocked: restored to the LKG, the issue RolledBack then Blocked
	// with a recovery report.
	ResultBlocked Result = "blocked"
	// ResultHeld: nothing was decided; the release waits for a person or a
	// later attempt. Success is not assumed.
	ResultHeld Result = "held"
	// ResultRefused: the request was refused before anything was deployed.
	ResultRefused Result = "refused"
	// ResultRestoreFailed: the release had to be undone and the LKG could
	// not be observed running again. A person must act.
	ResultRestoreFailed Result = "restore_failed"
)

// Outcome is what a release did and the evidence for it.
type Outcome struct {
	Result Result `json:"result"`
	// State is the lifecycle state the issue was left in.
	State      core.State         `json:"state"`
	Release    core.ReleaseDigest `json:"release"`
	RestoredTo core.ReleaseDigest `json:"restored_to,omitempty"`
	// Observed is the last observation of what runs, read from the system.
	Observed       deploy.Observed `json:"observed"`
	Windows        []WindowReport  `json:"windows,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	PublicationRef string          `json:"publication_ref,omitempty"`
}

// Pipeline releases candidates of one factory.
type Pipeline struct {
	cfg Config
}

// New returns a pipeline built from cfg.
func New(cfg Config) (*Pipeline, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Pipeline{cfg: cfg}, nil
}

// Release runs one candidate through the pipeline. The issue must be in
// ReleaseCandidate under the request's run. A non-nil error comes with an
// Outcome whose Result says whether the release was refused, held or could
// not be restored; a release that was rolled back returns ResultBlocked and
// no error.
func (p *Pipeline) Release(ctx context.Context, req Request) (Outcome, error) {
	out := Outcome{Release: req.Artifact.Digest}
	stop := func(result Result, err error) (Outcome, error) {
		out.Result, out.Reason = result, err.Error()
		return out, err
	}
	if err := req.validate(); err != nil {
		return stop(ResultRefused, err)
	}
	if p.frozen(ctx) {
		return stop(ResultHeld, core.NewError(core.CodeFrozen, "an emergency stop froze publishing"))
	}
	if err := checkGovernance(p.cfg.Protected, req.Artifact.Unit, req.Candidate.ChangedPaths); err != nil {
		return stop(ResultRefused, err)
	}
	h, err := p.take(ctx, req.IssueID)
	if err != nil {
		return stop(ResultHeld, err)
	}
	defer p.letGo(ctx, h)
	out.State = h.record.State
	if h.record.State != core.StateReleaseCandidate || h.record.RunID != req.RunID {
		return stop(ResultRefused, core.NewError(core.CodeConflict, "the issue is not a release candidate of this run"))
	}
	v, err := p.verify(ctx, req, h.record)
	if err != nil {
		_ = p.audit(ctx, h, "release.verify", core.OutcomeDenied, map[string]string{"code": string(core.CodeOf(err))})
		return stop(ResultRefused, err)
	}
	r, err := p.preflight(ctx, h, v)
	if err != nil {
		_ = p.audit(ctx, h, "release.preflight", core.OutcomeDenied, map[string]string{"code": string(core.CodeOf(err))})
		return stop(ResultHeld, err)
	}
	r.feature, r.summary = req.Feature, req.Summary
	if err := p.start(ctx, r); err != nil {
		out.State = h.record.State
		return stop(ResultHeld, err)
	}
	reason, err := p.roll(ctx, r)
	out.Windows, out.Observed, out.State = r.windows, r.observed, h.record.State
	if err != nil {
		return stop(ResultHeld, err)
	}
	if reason != "" {
		return p.restore(ctx, r, reason)
	}
	return p.finish(ctx, r)
}

// frozen reports whether an emergency stop holds. A stop that cannot be
// read counts as holding.
func (p *Pipeline) frozen(ctx context.Context) bool {
	frozen, err := p.cfg.Freeze.Frozen(ctx)
	return err != nil || frozen
}

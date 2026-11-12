package release

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/core/statemachine"
	"github.com/bestagentkits/sisf/pkg/deploy"
)

// Result is how a release ended.
type Result string

const (
	// ResultClosed: observed, verified, recorded as the LKG, published and closed.
	ResultClosed Result = "closed"
	// ResultBlocked: restored to the LKG, the issue RolledBack then Blocked
	// with a recovery report and the cause of the block.
	ResultBlocked Result = "blocked"
	// ResultHeld: nothing was deployed and nothing was decided; the issue
	// waits in ReleaseCandidate for a person or a later attempt. Success is
	// not assumed. A release whose rollout started is never held.
	ResultHeld Result = "held"
	// ResultRefused: the request was refused before anything was deployed
	// and the issue blocked. When the block could not be written yet the
	// issue is still in ReleaseCandidate (Outcome.State) and the refusal is
	// judged again later.
	ResultRefused Result = "refused"
	// ResultInterrupted: the release was stopped after its rollout started
	// and its issue could not be blocked yet, because the supervisor is
	// stopping or the record could not be written. The issue is left in the
	// state Outcome.State names, and the supervisor's recovery restores the
	// LKG and blocks it.
	ResultInterrupted Result = "interrupted"
	// ResultRestoreFailed: the release had to be undone and the LKG could
	// not be observed running again. The supervisor's recovery tries again
	// on every pass; a person must look.
	ResultRestoreFailed Result = "restore_failed"
)

// Outcome is what a release did and the evidence for it.
type Outcome struct {
	Result Result `json:"result"`
	// State is the lifecycle state the issue was left in.
	State      core.State         `json:"state"`
	Release    core.ReleaseDigest `json:"release"`
	RestoredTo core.ReleaseDigest `json:"restored_to,omitempty"`
	// Cause is why the issue was blocked, when it was.
	Cause statemachine.BlockCause `json:"cause,omitempty"`
	// Observed is the last observation of what runs, read from the system.
	Observed       deploy.Observed `json:"observed"`
	Windows        []WindowReport  `json:"windows,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	PublicationRef string          `json:"publication_ref,omitempty"`
	// Waits says, for a verified release that could not be finished yet,
	// what finishing it waits on, in fixed words with an error code that
	// are safe to show on the issue.
	Waits string `json:"waits,omitempty"`
}

// Pipeline releases candidates of one factory.
type Pipeline struct {
	cfg Config
	// mu guards driving: the issues a release or a recovery of this
	// pipeline is working on. A recovery never touches an issue a live
	// release of this process drives, and two recoveries of one issue
	// never run at once.
	mu      sync.Mutex
	driving map[core.IssueRef]bool
	// restoredWithoutLease is, per issue, the release and the LKG a
	// recovery switched back to while another holder kept the issue lease.
	restoredWithoutLease map[core.IssueRef]string
	// unseenSince is, per run, when this process first found its verified
	// release not observed serving, healthy, without another release
	// observed serving, healthy, in its place (verified_release_unseen.go).
	unseenSince map[core.RunID]time.Time
}

// New returns a pipeline built from cfg.
func New(cfg Config) (*Pipeline, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	return &Pipeline{cfg: cfg, driving: map[core.IssueRef]bool{}, restoredWithoutLease: map[core.IssueRef]string{},
		unseenSince: map[core.RunID]time.Time{}}, nil
}

// drive marks issue as driven by the caller until done is called; false
// when a release or a recovery of this pipeline drives it already.
func (p *Pipeline) drive(issue core.IssueRef) (done func(), ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.driving[issue] {
		return nil, false
	}
	p.driving[issue] = true
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.driving, issue)
	}, true
}

// Release runs one candidate through the pipeline. The issue must be in
// ReleaseCandidate under the request's run. A non-nil error comes with an
// Outcome whose Result says whether the release was refused, held,
// interrupted or could not be restored; a release that was rolled back
// returns ResultBlocked and no error. An error with no Result means the
// release did not begin: the issue is not this run's release candidate,
// is being released or recovered already, or what the refusal checks read
// could not be read.
//
// Before the rollout starts nothing is deployed: a refusal blocks the
// issue and a hold leaves it in ReleaseCandidate. Once the rollout started
// the release either closes or is undone: any failure to go on restores
// the LKG and blocks the issue.
func (p *Pipeline) Release(ctx context.Context, req Request) (Outcome, error) {
	out := Outcome{Release: req.Artifact.Digest}
	stop := func(result Result, err error) (Outcome, error) {
		out.Result, out.Reason = result, err.Error()
		return out, err
	}
	if err := req.IssueID.Validate(); err != nil {
		return out, err
	}
	done, ok := p.drive(req.IssueID)
	if !ok {
		return out, core.NewError(core.CodeLeaseHeld, "the issue is being released or recovered already")
	}
	defer done()
	if p.frozen(ctx) {
		return stop(ResultHeld, core.NewError(core.CodeFrozen, "an emergency stop froze publishing"))
	}
	h, err := p.take(ctx, req.IssueID)
	if err != nil {
		return stop(ResultHeld, err)
	}
	defer p.letGo(ctx, h)
	out.State = h.record.State
	if h.record.State != core.StateReleaseCandidate || h.record.RunID != req.RunID {
		return out, core.NewError(core.CodeConflict, "the issue is not a release candidate of this run")
	}
	v, err := p.refusal(ctx, req, h.record)
	switch {
	case err != nil && judgedRefusal(ctx, err):
		return p.refuse(ctx, h, out, err)
	case core.HasCode(err, core.CodeReleaseHold):
		// Only the base check holds here, on an LKG entry the supervisor
		// did not write: held for the Owner, as the preflight holds it.
		_ = p.audit(ctx, h, "release.preflight", core.OutcomeDenied, map[string]string{"code": string(core.CodeOf(err))})
		return stop(ResultHeld, err)
	case err != nil:
		// Nothing was judged: the release did not begin and is tried again.
		return out, fmt.Errorf("the release could not be judged yet, so it is tried again: %w", err)
	}
	r, err := p.preflight(ctx, h, v)
	if core.HasCode(err, core.CodeAlreadyExists) {
		// The candidate builds the program that serves: nothing can be
		// deployed or judged, and the lifecycle has no way from here to
		// Verified that does not claim a rollout. The issue is blocked and
		// its pull request left for the Owner to merge by hand.
		return p.refuse(ctx, h, out, err)
	}
	if err != nil {
		_ = p.audit(ctx, h, "release.preflight", core.OutcomeDenied, map[string]string{"code": string(core.CodeOf(err))})
		return stop(ResultHeld, err)
	}
	r.feature, r.summary, r.candidate = req.Feature, req.Summary, req.Candidate
	if err := p.start(ctx, r); err != nil {
		out.State = h.record.State
		return stop(ResultHeld, err)
	}
	why, err := p.roll(ctx, r)
	out.Windows, out.Observed, out.State = r.windows, r.observed, h.record.State
	switch {
	case err != nil && ctx.Err() != nil:
		// The supervisor is stopping. What runs is left as it is, and the
		// recovery at the next start restores the LKG.
		return stop(ResultInterrupted, err)
	case err != nil:
		why = &undo{cause: statemachine.BlockReleaseInterrupted, reason: "the release could not be watched to its end: " + err.Error()}
	}
	if why != nil {
		return p.restore(ctx, r, *why)
	}
	return p.finish(ctx, r)
}

// frozen reports whether an emergency stop holds. A stop that cannot be
// read counts as holding.
func (p *Pipeline) frozen(ctx context.Context) bool {
	frozen, err := p.cfg.Freeze.Frozen(ctx)
	return err != nil || frozen
}

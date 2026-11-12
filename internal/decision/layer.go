// Package decision is the deterministic part of the Decision Layer for one
// signal: the 5xx rate of each endpoint, read from the edge's counters.
//
// Each run follows the same steps: data quality and redaction, baseline,
// typed questions, evidence, scope and policy, proposal. Only a regression
// that recurs over a known baseline with enough samples becomes a proposal,
// and a condition is proposed once, keyed by its fingerprint. Thin data
// yields collect_more_evidence and quiet or healthy endpoints are ignored,
// so nothing is opened without evidence.
//
// Counter data is untrusted: an endpoint name is quoted in what the layer
// writes and never changes a threshold, a kind, a priority or the policy.
// The layer recommends; it grants no permission and changes no policy.
package decision

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/telemetry"
)

// Config wires the layer.
type Config struct {
	Source CounterSource
	Sink   ProposalSink
	// Store deduplicates proposals. Nil means an in-process store.
	Store ProposalStore
	// Redactor cleans endpoint names before they are written anywhere.
	Redactor telemetry.Redactor
	// Clock times windows. Nil means the wall clock.
	Clock core.Clock
	// Policy pins the Owner-approved rules every proposal is made under.
	Policy core.PolicyRefs
	Rules  Rules
	// Features maps an endpoint name to the approved feature it belongs to.
	Features map[string]string
}

// Outcome is the decision for one endpoint in one run.
type Outcome struct {
	// Endpoint is the redacted endpoint name.
	Endpoint    string
	Fingerprint string
	Verdict     pkgdecision.Verdict
	Reason      string
	Answers     []pkgdecision.Answer
	Evidence    pkgdecision.Evidence
	// Proposal is set when the verdict is propose, also for a duplicate.
	Proposal *pkgdecision.Proposal
	// NewProposal is true only when this run handed the proposal to the sink.
	NewProposal bool
}

// Layer runs the decision flow. It is safe for concurrent use; runs are
// serialized.
type Layer struct {
	cfg     Config
	mu      sync.Mutex
	sampler sampler
	states  map[string]*endpointState
}

// New checks the configuration. Policy must be pinned and the rules valid.
func New(cfg Config) (*Layer, error) {
	if cfg.Source == nil || cfg.Sink == nil || cfg.Redactor == nil {
		return nil, invalid("the decision layer needs a counter source, a proposal sink and a redactor")
	}
	if err := cfg.Policy.Validate(); err != nil {
		return nil, core.NewError(core.CodePolicyHold, "the decision layer needs policy pinned to approved commits").WithCause(err)
	}
	if err := cfg.Rules.Validate(); err != nil {
		return nil, err
	}
	if cfg.Store == nil {
		cfg.Store = NewMemoryProposals()
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	return &Layer{cfg: cfg, states: map[string]*endpointState{}}, nil
}

// Run reads the counters once and decides about every endpoint that has a
// window. The first run only sets the starting point. A failing sink or
// store does not stop the other endpoints; the first failure is returned
// and the proposal is offered again on the next run.
func (l *Layer) Run(ctx context.Context) ([]Outcome, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot, err := l.cfg.Source.EndpointCounters(ctx)
	if err != nil {
		return nil, core.NewError(core.CodeUnavailable, "edge counters could not be read").WithCause(err)
	}
	admitted, overflow := l.admit(snapshot)
	windows := l.sampler.next(admitted, l.cfg.Clock.Now())
	slices.SortFunc(windows, func(a, b Window) int { return cmp.Compare(a.Endpoint, b.Endpoint) })

	out := make([]Outcome, 0, len(windows)+len(overflow))
	var first error
	for _, w := range windows {
		o, err := l.decide(ctx, w)
		if err != nil && first == nil {
			first = err
		}
		out = append(out, o)
	}
	for _, name := range overflow {
		clean := l.cfg.Redactor.Redact(name)
		out = append(out, Outcome{Endpoint: clean, Fingerprint: Fingerprint(clean),
			Verdict: pkgdecision.VerdictIgnore, Reason: "too_many_endpoints"})
	}
	return out, first
}

// admit keeps the endpoints already tracked and admits new ones while there
// is room. The rest are reported, never tracked.
func (l *Layer) admit(snapshot []EndpointCounter) (admitted []EndpointCounter, overflow []string) {
	slices.SortFunc(snapshot, func(a, b EndpointCounter) int { return cmp.Compare(a.Endpoint, b.Endpoint) })
	for _, c := range snapshot {
		if _, ok := l.states[c.Endpoint]; !ok {
			if len(l.states) >= l.cfg.Rules.MaxEndpoints {
				overflow = append(overflow, c.Endpoint)
				continue
			}
			l.states[c.Endpoint] = &endpointState{}
		}
		admitted = append(admitted, c)
	}
	return admitted, overflow
}

// decide runs the flow for one window and hands a new proposal to the sink.
func (l *Layer) decide(ctx context.Context, w Window) (Outcome, error) {
	name := l.cfg.Redactor.Redact(w.Endpoint)
	fp := Fingerprint(name)
	status, err := l.cfg.Store.Status(ctx, fp)
	if err != nil {
		// Without knowing whether it was proposed, propose nothing.
		return Outcome{Endpoint: name, Fingerprint: fp, Verdict: pkgdecision.VerdictCollectMoreEvidence, Reason: "store_unavailable"},
			core.NewError(core.CodeUnavailable, "proposal store could not be read").WithCause(err)
	}
	a := l.cfg.Rules.assess(w, name, l.states[w.Endpoint], status == StatusDismissed)
	o := Outcome{Endpoint: name, Fingerprint: fp, Verdict: a.verdict, Reason: a.reason, Answers: a.answers.list(), Evidence: a.evidence}
	if a.verdict != pkgdecision.VerdictPropose {
		return o, nil
	}
	// Scope and policy: the proposal must be complete, pinned and a bug fix.
	p := l.cfg.Rules.proposal(name, a, l.cfg.Policy, l.cfg.Features[w.Endpoint])
	if err := p.Validate(); err != nil || p.Kind.RequiresOwnerApproval() {
		o.Verdict, o.Reason = pkgdecision.VerdictCollectMoreEvidence, "proposal_out_of_scope"
		return o, nil
	}
	o.Proposal = &p
	if status == StatusOpen {
		return o, nil
	}
	if err := l.cfg.Sink.Propose(ctx, p); err != nil {
		return o, core.NewError(core.CodeUnavailable, "proposal could not be delivered").WithCause(err)
	}
	if err := l.cfg.Store.Record(ctx, p); err != nil {
		return o, core.NewError(core.CodeUnavailable, "proposal could not be recorded").WithCause(err)
	}
	o.NewProposal = true
	return o, nil
}

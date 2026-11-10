package decision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// ProposalStatus is where a proposal stands with people.
type ProposalStatus string

const (
	// StatusNone means no proposal exists for the fingerprint.
	StatusNone ProposalStatus = ""
	// StatusOpen means a proposal exists and nobody dismissed it.
	StatusOpen ProposalStatus = "open"
	// StatusDismissed means a person decided the condition is not worth
	// work; it is not proposed again.
	StatusDismissed ProposalStatus = "dismissed"
)

// ProposalStore remembers proposals by fingerprint, so the same condition
// seen again does not open a second issue.
type ProposalStore interface {
	Status(ctx context.Context, fingerprint string) (ProposalStatus, error)
	// Record stores a new proposal as open.
	Record(ctx context.Context, p pkgdecision.Proposal) error
}

// ProposalSink receives each new proposal once, for example to open an
// issue. It should be idempotent by fingerprint: a proposal whose sink call
// succeeded but whose record failed is offered again on the next run.
type ProposalSink interface {
	Propose(ctx context.Context, p pkgdecision.Proposal) error
}

// MemoryProposals is an in-process ProposalStore. It is safe for concurrent use.
type MemoryProposals struct {
	mu   sync.Mutex
	byFP map[string]storedProposal
}

type storedProposal struct {
	proposal pkgdecision.Proposal
	status   ProposalStatus
}

var _ ProposalStore = (*MemoryProposals)(nil)

// NewMemoryProposals returns an empty store.
func NewMemoryProposals() *MemoryProposals {
	return &MemoryProposals{byFP: map[string]storedProposal{}}
}

// Status returns the status of the proposal with fingerprint.
func (m *MemoryProposals) Status(_ context.Context, fingerprint string) (ProposalStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byFP[fingerprint].status, nil
}

// Record stores p as open. A fingerprint is recorded once.
func (m *MemoryProposals) Record(_ context.Context, p pkgdecision.Proposal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byFP[p.Fingerprint]; ok {
		return core.NewError(core.CodeAlreadyExists, "a proposal with this fingerprint exists")
	}
	m.byFP[p.Fingerprint] = storedProposal{proposal: p, status: StatusOpen}
	return nil
}

// Dismiss marks the proposal with fingerprint as not worth work.
func (m *MemoryProposals) Dismiss(_ context.Context, fingerprint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sp, ok := m.byFP[fingerprint]
	if !ok {
		return core.NewError(core.CodeNotFound, "no proposal with this fingerprint")
	}
	sp.status = StatusDismissed
	m.byFP[fingerprint] = sp
	return nil
}

// Fingerprint identifies the 5xx condition of one endpoint. It depends on
// the endpoint only, never on the values seen, so a condition that persists
// keeps one fingerprint.
func Fingerprint(endpoint string) string {
	sum := sha256.Sum256([]byte(Metric + "\x00" + endpoint))
	return "http-5xx-" + hex.EncodeToString(sum[:16])
}

// proposal drafts the task for a recurring regression. The endpoint name is
// data: it is quoted wherever it appears and decides nothing.
func (r Rules) proposal(name string, a assessment, policy core.PolicyRefs, feature string) pkgdecision.Proposal {
	ev := a.evidence
	limit := max(r.ErrorRateThreshold, ev.Baseline+r.MinDelta)
	p := pkgdecision.Proposal{
		Fingerprint: Fingerprint(name),
		Goal:        fmt.Sprintf("Bring the 5xx rate of endpoint %q from %.2f%% back to its baseline of %.2f%%.", name, 100*ev.Value, 100*ev.Baseline),
		DraftSoW: fmt.Sprintf("Find and fix the cause of the server errors returned by endpoint %q. "+
			"The scope is the code that serves this endpoint and its direct dependencies. "+
			"Policy, permissions, release thresholds and budget are out of scope.", name),
		DoD: fmt.Sprintf("Over %d consecutive windows of at least %d requests each, the 5xx rate of endpoint %q is at most %.2f%%, and a regression test reproduces the failure.",
			r.ConsecutiveWindows, r.MinSamples, name, 100*limit),
		Verdict:  pkgdecision.VerdictPropose,
		Kind:     pkgdecision.KindBug,
		Priority: priorityFor(ev.Value),
		Evidence: ev,
		Policy:   policy,
	}
	if feature != "" {
		p.AffectedFeatures = []string{feature}
	}
	return p
}

func priorityFor(rate float64) pkgdecision.Priority {
	switch {
	case rate >= 0.5:
		return pkgdecision.PriorityUrgent
	case rate >= 0.2:
		return pkgdecision.PriorityHigh
	}
	return pkgdecision.PriorityMedium
}

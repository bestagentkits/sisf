package wiring

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/edge"
	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/state"
)

// EdgeCounters reads the edge's cumulative counters for the decision layer:
// one endpoint per revision the edge served, with the requests that got a
// response and those that failed with a server error since the edge started.
// It holds a counts reader only, so the control zone cannot change the
// division of traffic through it.
type EdgeCounters struct{ Edge edge.CountsReader }

var _ decision.CounterSource = EdgeCounters{}

// EndpointCounters returns the totals, sorted by endpoint. A count the edge
// reports as negative is untrusted data and read as zero.
func (c EdgeCounters) EndpointCounters(ctx context.Context) ([]decision.EndpointCounter, error) {
	totals, err := c.Edge.Counts(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]decision.EndpointCounter, 0, len(totals))
	for revision, counts := range totals {
		out = append(out, decision.EndpointCounter{
			Endpoint: revision, Requests: nonNegative(counts.Samples), ServerErrors: nonNegative(counts.ServerErrors),
		})
	}
	slices.SortFunc(out, func(a, b decision.EndpointCounter) int { return strings.Compare(a.Endpoint, b.Endpoint) })
	return out, nil
}

func nonNegative(n int64) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// The decision layer's proposals are remembered in the operational store,
// so a restart of the control process does not open a second issue for a
// condition it already proposed.
const (
	proposalsNamespace = "control.decision"
	proposalsPrefix    = "proposal/"
)

// storedProposals is a decision.ProposalStore over the operational store.
type storedProposals struct{ store state.Store }

var _ decision.ProposalStore = storedProposals{}

func (s storedProposals) Status(ctx context.Context, fingerprint string) (decision.ProposalStatus, error) {
	status := decision.StatusNone
	err := s.store.View(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.GetKV(ctx, proposalsNamespace, proposalsPrefix+fingerprint)
		switch {
		case core.HasCode(err, core.CodeNotFound):
			return nil
		case err != nil:
			return err
		}
		status = decision.StatusOpen
		return nil
	})
	return status, err
}

// Record stores p once; recording a fingerprint again changes nothing.
func (s storedProposals) Record(ctx context.Context, p pkgdecision.Proposal) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return core.NewError(core.CodeInternal, "the proposal cannot be encoded").WithCause(err)
	}
	return s.store.Update(ctx, func(ctx context.Context, tx state.Tx) error {
		_, err := tx.GetKV(ctx, proposalsNamespace, proposalsPrefix+p.Fingerprint)
		switch {
		case err == nil:
			return nil
		case !core.HasCode(err, core.CodeNotFound):
			return err
		}
		_, err = tx.PutKV(ctx, state.KVEntry{Namespace: proposalsNamespace, Key: proposalsPrefix + p.Fingerprint, Value: raw})
		return err
	})
}

// IssueCreator opens an issue; github.RESTClient is one.
type IssueCreator interface {
	CreateIssue(ctx context.Context, repo github.RepoRef, n github.NewIssue) (github.Issue, error)
}

// issueProposals hands each new proposal to the Owner as an issue of the
// factory's repository. The layer quotes and redacts endpoint names before
// a proposal reaches it; the body is built from typed fields only.
type issueProposals struct {
	issues IssueCreator
	repo   github.RepoRef
}

var _ decision.ProposalSink = issueProposals{}

func (s issueProposals) Propose(ctx context.Context, p pkgdecision.Proposal) error {
	_, err := s.issues.CreateIssue(ctx, s.repo, github.NewIssue{Title: proposalTitle(p), Body: proposalBody(p)})
	return err
}

// proposalTitleMax bounds an issue title built from a goal.
const proposalTitleMax = 120

func proposalTitle(p pkgdecision.Proposal) string {
	title := fmt.Sprintf("[%s] %s", p.Priority, strings.TrimSpace(p.Goal))
	if r := []rune(title); len(r) > proposalTitleMax {
		title = string(r[:proposalTitleMax-1]) + "…"
	}
	return title
}

func proposalBody(p pkgdecision.Proposal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The decision layer proposes this %s. It recommends only: nothing runs until the Owner accepts it.\n\n", p.Kind)
	fmt.Fprintf(&b, "## Goal\n\n%s\n\n## Scope of work\n\n%s\n\n## Definition of done\n\n%s\n\n", p.Goal, p.DraftSoW, p.DoD)
	if len(p.Options) > 0 {
		b.WriteString("## Options\n\n")
		for _, o := range p.Options {
			fmt.Fprintf(&b, "- %s\n", o)
		}
		b.WriteString("\n")
	}
	e := p.Evidence
	fmt.Fprintf(&b, "## Evidence\n\n- %s: %.4f against a baseline of %.4f (%s)\n- %d samples from %s to %s, seen %d times, confidence %.2f\n\n",
		e.Metric, e.Value, e.Baseline, e.Trend, e.SampleCount, e.From.UTC().Format("2006-01-02 15:04 MST"),
		e.To.UTC().Format("2006-01-02 15:04 MST"), e.Recurrence, e.Confidence)
	fmt.Fprintf(&b, "Decided under policy %s and decision rules %s.\n\n<!-- sisf-decision-fingerprint: %s -->\n",
		p.Policy.PolicySHA, p.Policy.DecisionSHA, p.Fingerprint)
	return b.String()
}

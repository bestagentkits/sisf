package wiring_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bestagentkits/sisf/cmd/factoryd/wiring"
	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/decision/systemone/systemonetest"
	"github.com/bestagentkits/sisf/internal/policy"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// edgeCounters are cumulative counters, as the edge reports them.
type edgeCounters struct {
	mu       sync.Mutex
	requests uint64
	errors   uint64
}

func (c *edgeCounters) add(requests, errs uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests += requests
	c.errors += errs
}

func (c *edgeCounters) EndpointCounters(context.Context) ([]decision.EndpointCounter, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []decision.EndpointCounter{{Endpoint: "sha256:release", Requests: c.requests, ServerErrors: c.errors}}, nil
}

// decisionWindow adds one window of traffic and runs the control pass that
// closes it.
func decisionWindow(t *testing.T, f *harness.Factory, counters *edgeCounters, requests, errs uint64) {
	t.Helper()
	counters.add(requests, errs)
	f.Clock.Advance(wiring.DecisionInterval)
	f.Pass()
}

// regress gives the endpoint a healthy baseline and then two bad windows,
// the recurring regression the rules propose.
func regress(t *testing.T, f *harness.Factory, counters *edgeCounters) {
	t.Helper()
	f.Pass() // the first read only sets the starting point
	for range decision.DefaultRules().MinBaselineWindows {
		decisionWindow(t, f, counters, 1000, 5)
	}
	for range decision.DefaultRules().ConsecutiveWindows {
		decisionWindow(t, f, counters, 1000, 100)
	}
}

// openedIssues returns the issues the factory opened on its repository.
func openedIssues(f *harness.Factory) int {
	n := 0
	for _, r := range f.GitHub.Requests() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/repos/"+f.Repo+"/issues") && r.Status == http.StatusCreated {
			n++
		}
	}
	return n
}

// Without a System One section the rules decide alone: a recurring
// regression of the edge's 5xx rate becomes one issue for the Owner, and
// the same condition seen again opens no second one.
func TestTheRulesAloneProposeARecurringRegressionOnce(t *testing.T) {
	t.Parallel()
	counters := &edgeCounters{}
	f := harness.New(t, harness.Options{Decision: &wiring.DecisionConfig{Counters: counters}})
	before := openedIssues(f)
	regress(t, f, counters)
	if got := openedIssues(f) - before; got != 1 {
		t.Fatalf("the regression opened %d issues, want 1", got)
	}
	decisionWindow(t, f, counters, 1000, 100)
	decisionWindow(t, f, counters, 1000, 100)
	if got := openedIssues(f) - before; got != 1 {
		t.Fatalf("the same condition opened %d issues, want 1", got)
	}
}

// Before its interval is over the layer does not read the counters again.
func TestTheDecisionLayerWaitsForItsInterval(t *testing.T) {
	t.Parallel()
	counters := &edgeCounters{}
	f := harness.New(t, harness.Options{Decision: &wiring.DecisionConfig{Counters: counters}})
	before := openedIssues(f)
	f.Pass()
	for range decision.DefaultRules().MinBaselineWindows + decision.DefaultRules().ConsecutiveWindows {
		counters.add(1000, 100)
		f.Pass()
	}
	if got := openedIssues(f) - before; got != 0 {
		t.Fatalf("passes inside one interval opened %d issues", got)
	}
}

// When the approved manifest declares System One, the layer consults it
// about each proposal, and its judgment may raise the priority.
func TestTheApprovedSystemOneSectionIsConsulted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := systemonetest.New("1", "")
	t.Cleanup(fake.Close)
	route := core.ModelRoute{Provider: "jev", Model: "jev-1"}
	fake.Answer(func(req pkgdecision.Request) pkgdecision.Judgment {
		j := pkgdecision.Judgment{Verdict: pkgdecision.VerdictPropose, Kind: pkgdecision.KindBug,
			Priority: pkgdecision.PriorityUrgent, Model: route, SchemaVersion: req.SchemaVersion}
		for _, q := range req.Questions {
			value := map[string]string{decision.SystemOneVerdict: string(j.Verdict), decision.SystemOneKind: string(j.Kind),
				decision.SystemOnePriority: string(j.Priority)}[q.ID]
			if value == "" {
				value = q.Options[0]
			}
			j.Answers = append(j.Answers, pkgdecision.Answer{QuestionID: q.ID, Value: value})
		}
		return j
	})

	counters := &edgeCounters{}
	f := harness.New(t, harness.Options{Decision: &wiring.DecisionConfig{Counters: counters}})
	next := commitPolicyWith(t, f, func(files map[string]string) {
		files[policy.PathFactory] = strings.TrimRight(files[policy.PathFactory], "\n") + "\ndecision:\n  system_one:\n" +
			"    backend: jev\n    endpoint: " + fake.URL + "\n    version: \"1\"\n    timeout: 5s\n" +
			"    model:\n      provider: jev\n      model: jev-1\n"
	})
	request, err := f.Control.RequestPolicyChange(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Control.Approvals.Submit(ctx, owner(f), submission(request, approvals.DecisionApprove)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Control.ApproveBaseline(ctx, next, request.ID); err != nil {
		t.Fatal(err)
	}

	before := openedIssues(f)
	regress(t, f, counters)
	if got := openedIssues(f) - before; got != 1 {
		t.Fatalf("the regression opened %d issues, want 1", got)
	}
	if calls := fake.Calls(); len(calls) != 1 {
		t.Fatalf("System One was asked %d times, want 1", len(calls))
	}
	issue, err := f.GitHub.Issue(f.Repo, latestIssue(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issue.Title, "[urgent] ") || !strings.Contains(issue.Body, "sisf-decision-fingerprint") {
		t.Fatalf("the proposal issue is %q:\n%s", issue.Title, issue.Body)
	}
}

// latestIssue is the number of the newest issue of the factory's repository.
func latestIssue(t *testing.T, f *harness.Factory) int {
	t.Helper()
	last := 0
	for n := 1; n < 1000; n++ {
		if _, err := f.GitHub.Issue(f.Repo, n); err != nil {
			break
		}
		last = n
	}
	if last == 0 {
		t.Fatal("the repository has no issue")
	}
	return last
}

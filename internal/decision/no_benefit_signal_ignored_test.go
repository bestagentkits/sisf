package decision_test

import (
	"testing"

	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

func TestEndpointWithoutTrafficIsIgnoredForever(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.counters.add("GET /idle", 0, 0)
	for range 100 {
		o := f.outcomeFor(t, "GET /idle")
		if o.Verdict != pkgdecision.VerdictIgnore || o.Proposal != nil {
			t.Fatalf("an idle endpoint must be ignored, got %+v", o)
		}
	}
	if f.sink.count() != 0 {
		t.Fatalf("no issue may come from no activity, got %d", f.sink.count())
	}
}

func TestTooFewSamplesCollectMoreEvidenceAndCreateNoIssue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.baseline(t, "GET /rare")
	for range 50 {
		// Half the requests fail, but there are far too few of them to know.
		o := f.window(t, "GET /rare", 10, 5)
		if o.Verdict != pkgdecision.VerdictCollectMoreEvidence || o.Proposal != nil {
			t.Fatalf("thin data must yield collect_more_evidence, got %+v", o)
		}
	}
	if f.sink.count() != 0 {
		t.Fatalf("thin data created %d issues", f.sink.count())
	}
}

func TestHealthyEndpointIsIgnoredAndCreatesNoIssue(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.baseline(t, "GET /health")
	for range 50 {
		o := f.window(t, "GET /health", 1000, 4)
		if o.Verdict.CreatesWork() || o.Verdict == pkgdecision.VerdictInvestigate {
			t.Fatalf("a healthy endpoint must not create work, got %+v", o)
		}
	}
	if f.sink.count() != 0 {
		t.Fatalf("a healthy endpoint created %d issues", f.sink.count())
	}
}

func TestErrorRateBelowTheThresholdIsObservedNotProposed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.baseline(t, "GET /slow")
	// 1.5% is worse than the 0.5% baseline but under the 2% threshold.
	o := f.window(t, "GET /slow", 1000, 15)
	if o.Verdict != pkgdecision.VerdictObserve || o.Proposal != nil {
		t.Fatalf("a rise under the threshold must be observed, got %+v", o)
	}
}

func TestChronicErrorRateThatIsNotWorseIsObservedNotProposed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for range 20 {
		// Always 5%: above the threshold, but never worse than its own baseline.
		o := f.window(t, "GET /legacy", 1000, 50)
		if o.Verdict.CreatesWork() {
			t.Fatalf("a stable rate is no regression, got %+v", o)
		}
	}
	if f.sink.count() != 0 {
		t.Fatalf("a stable rate created %d issues", f.sink.count())
	}
}

func TestHighRateWithoutABaselineCollectsMoreEvidence(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	o := f.window(t, "GET /new", 1000, 300)
	if o.Verdict != pkgdecision.VerdictCollectMoreEvidence || o.Proposal != nil {
		t.Fatalf("without a baseline nothing is proposed, got %+v", o)
	}
}

func TestCounterResetIsADataQualityFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.baseline(t, "GET /x")
	f.counters.set("GET /x", 10, 10)
	o := f.outcomeFor(t, "GET /x")
	if o.Verdict != pkgdecision.VerdictCollectMoreEvidence || o.Proposal != nil {
		t.Fatalf("a counter that went backwards cannot be trusted, got %+v", o)
	}
}

func TestRegressionIsProposedOnceAndThenStopsWhenDismissed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.baseline(t, "POST /checkout")

	first := f.window(t, "POST /checkout", 1000, 100)
	if first.Verdict != pkgdecision.VerdictInvestigate || first.Proposal != nil {
		t.Fatalf("one bad window is investigated, not proposed: %+v", first)
	}
	second := f.window(t, "POST /checkout", 1000, 100)
	if second.Verdict != pkgdecision.VerdictPropose || second.Proposal == nil || !second.NewProposal {
		t.Fatalf("a recurring regression is proposed: %+v", second)
	}
	p := *second.Proposal
	if err := p.Validate(); err != nil {
		t.Fatalf("the proposal must be valid: %v", err)
	}
	if p.Kind != pkgdecision.KindBug || p.Evidence.SampleCount != 1000 || p.Evidence.Recurrence != 2 ||
		p.Evidence.Trend != pkgdecision.TrendRising || len(p.AffectedFeatures) != 1 || p.Policy != policyRefs() {
		t.Fatalf("proposal content: %+v", p)
	}

	for range 30 {
		o := f.window(t, "POST /checkout", 1000, 100)
		if o.Verdict != pkgdecision.VerdictPropose || o.NewProposal || o.Fingerprint != p.Fingerprint {
			t.Fatalf("the same condition must deduplicate to the same proposal: %+v", o)
		}
	}
	if f.sink.count() != 1 {
		t.Fatalf("one condition must open one issue, got %d", f.sink.count())
	}

	if err := f.store.Dismiss(t.Context(), p.Fingerprint); err != nil {
		t.Fatal(err)
	}
	o := f.window(t, "POST /checkout", 1000, 100)
	if o.Verdict != pkgdecision.VerdictStop || o.Proposal != nil {
		t.Fatalf("a dismissed condition stops instead of reopening: %+v", o)
	}
	if f.sink.count() != 1 {
		t.Fatalf("a dismissed condition must not reopen, got %d issues", f.sink.count())
	}
}

func TestRecoveryEndsTheStreak(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.baseline(t, "GET /flaky")
	for range 10 {
		if o := f.window(t, "GET /flaky", 1000, 100); o.Verdict != pkgdecision.VerdictInvestigate {
			t.Fatalf("a single bad window is investigated, got %+v", o)
		}
		if o := f.window(t, "GET /flaky", 1000, 5); o.Verdict.CreatesWork() {
			t.Fatalf("a recovered window creates no work, got %+v", o)
		}
	}
	if f.sink.count() != 0 {
		t.Fatalf("alternating spikes never recur long enough, got %d issues", f.sink.count())
	}
}

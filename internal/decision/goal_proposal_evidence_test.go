package decision_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/decision"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

func TestThinEvidenceChoosesDoingNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	thin := map[string]func() decision.Outcome{
		"too few samples": func() decision.Outcome { return f.window(t, "GET /few", 50, 50) },
		"no baseline":     func() decision.Outcome { return f.window(t, "GET /new", 1000, 900) },
		"counter reset": func() decision.Outcome {
			f.baseline(t, "GET /reset")
			f.counters.set("GET /reset", 900, 800)
			return f.outcomeFor(t, "GET /reset")
		},
		"single bad window": func() decision.Outcome {
			f.baseline(t, "GET /once")
			return f.window(t, "GET /once", 1000, 300)
		},
	}
	for name, run := range thin {
		o := run()
		if o.Chosen() != decision.OptionDoNothing || o.Proposal != nil || o.Verdict.CreatesWork() {
			t.Errorf("%s: want do-nothing, got %+v", name, o)
		}
	}
	if f.sink.count() != 0 {
		t.Fatalf("thin evidence opened %d issues", f.sink.count())
	}
}

func TestAProposalCarriesOptionsScopeDoneAndTracedEvidence(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.regress(t, "POST /checkout")
	o := f.window(t, "POST /checkout", 1000, 100)
	if o.Proposal == nil || o.Chosen() != decision.OptionFix {
		t.Fatalf("a recurring regression is proposed: %+v", o)
	}
	p := *o.Proposal
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(p.Options) != 3 {
		t.Fatalf("options = %q", p.Options)
	}
	for i, id := range []string{decision.OptionFix, decision.OptionRollback, decision.OptionDoNothing} {
		if !strings.HasPrefix(p.Options[i], id+": ") {
			t.Errorf("option %d = %q, want prefix %q", i, p.Options[i], id)
		}
	}
	if !strings.Contains(p.DoD, "2 consecutive windows") || !strings.Contains(p.DoD, "100 requests") ||
		!strings.Contains(p.DoD, "2.00%") || !strings.Contains(p.DraftSoW, "out of scope") {
		t.Fatalf("the definition of done is measurable and the scope bounded: dod %q sow %q", p.DoD, p.DraftSoW)
	}
	ev := p.Evidence
	if !ev.Sufficient(decision.DefaultRules().MinSamples) || ev.Recurrence != 2 || ev.Trend != pkgdecision.TrendRising ||
		ev.Baseline <= 0 || ev.Confidence != 1 || p.AffectedFeatures[0] != "checkout" {
		t.Fatalf("evidence = %+v", ev)
	}
	var windows, baselines int
	for _, ref := range ev.SourceRefs {
		switch {
		case strings.HasPrefix(ref, `edge:counters:"POST /checkout"@`):
			windows++
		case strings.HasPrefix(ref, `edge:baseline:"POST /checkout"@`) && strings.HasSuffix(ref, "#3"):
			baselines++
		}
	}
	if windows != 2 || baselines != 1 || ev.SourceRefs[0] == ev.SourceRefs[1] {
		t.Fatalf("evidence must trace both bad windows and the baseline: %q", ev.SourceRefs)
	}
}

func TestInstructionsInTelemetryCannotChangeTheChoice(t *testing.T) {
	t.Parallel()
	const plain = "POST /pay"
	f := newFixture(t)
	for _, name := range []string{plain, injection} {
		f.baseline(t, name)
	}
	compared := 0
	for range 3 {
		f.counters.add(plain, 1000, 100)
		f.counters.add(injection, 1000, 100)
		outs := map[string]decision.Outcome{}
		for _, o := range f.run(t) {
			outs[o.Endpoint] = o
		}
		a, b := outs[plain], outs[injection]
		if a.Verdict != b.Verdict || a.Reason != b.Reason || a.Chosen() != b.Chosen() || (a.Proposal == nil) != (b.Proposal == nil) {
			t.Fatalf("the same evidence decides the same, whatever the endpoint says: %+v vs %+v", a, b)
		}
		if a.Proposal == nil {
			continue
		}
		compared++
		pa, pb := *a.Proposal, *b.Proposal
		if pa.Kind != pb.Kind || pa.Priority != pb.Priority || len(pa.Options) != len(pb.Options) || pa.Policy != pb.Policy ||
			strings.ReplaceAll(pb.DoD, injection, plain) != pa.DoD {
			t.Fatalf("proposals differ beyond the quoted name: %+v vs %+v", pa, pb)
		}
	}
	if compared != 2 {
		t.Fatalf("compared %d proposals, want 2", compared)
	}
}

func TestSystemOneSeesTelemetryOnlyAsQuotedData(t *testing.T) {
	t.Parallel()
	f, fake := newSystemOneFixture(t)
	fake.Answer(func(req pkgdecision.Request) pkgdecision.Judgment {
		return judgmentFor(req, pkgdecision.VerdictPropose, pkgdecision.KindBug, pkgdecision.PriorityUrgent)
	})
	f.regress(t, injection)
	o := f.window(t, injection, 1000, 100)
	if o.Proposal == nil || o.Proposal.Kind != pkgdecision.KindBug || o.Proposal.Priority != pkgdecision.PriorityMedium {
		t.Fatalf("outcome %+v", o)
	}
	req := fake.Calls()[0].Request.Request
	if !reflect.DeepEqual(req.Questions, typedQuestions(t).Questions) || !strings.Contains(req.Goal, `"`+injection+`"`) {
		t.Fatalf("the questions come from DECISIONS.md and the name is quoted data: %+v", req)
	}
}

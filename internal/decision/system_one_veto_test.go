package decision_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/bestagentkits/sisf/internal/decision"
	"github.com/bestagentkits/sisf/internal/decision/systemone/systemonetest"
	"github.com/bestagentkits/sisf/internal/redact"
	"github.com/bestagentkits/sisf/pkg/core"
	pkgdecision "github.com/bestagentkits/sisf/pkg/decision"
)

// alwaysDoIt is a System One that says every condition is safe to implement now.
func alwaysDoIt(req pkgdecision.Request) pkgdecision.Judgment {
	return judgmentFor(req, pkgdecision.VerdictImplement, pkgdecision.KindBug, pkgdecision.PriorityUrgent)
}

func TestSystemOneCannotTurnADeterministicVetoIntoAProposal(t *testing.T) {
	t.Parallel()
	f, fake := newSystemOneFixture(t)
	fake.Answer(alwaysDoIt)

	if o := f.window(t, "GET /thin", 10, 9); o.Verdict != pkgdecision.VerdictCollectMoreEvidence || o.Proposal != nil {
		t.Fatalf("thin data stays collect_more_evidence: %+v", o)
	}
	f.baseline(t, "GET /quiet")
	if o := f.window(t, "GET /quiet", 1000, 5); o.Verdict != pkgdecision.VerdictIgnore || o.Proposal != nil {
		t.Fatalf("a healthy endpoint stays ignored: %+v", o)
	}
	if len(fake.Calls()) != 0 {
		t.Fatalf("System One was asked about a vetoed condition %d times", len(fake.Calls()))
	}

	f.regress(t, "GET /a")
	o := f.window(t, "GET /a", 1000, 100)
	if o.Proposal == nil || o.Verdict != pkgdecision.VerdictPropose || o.Proposal.Verdict != pkgdecision.VerdictPropose {
		t.Fatalf("implement never goes beyond a proposal: %+v", o)
	}
	if err := f.store.Dismiss(context.Background(), o.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if o := f.window(t, "GET /a", 1000, 100); o.Verdict != pkgdecision.VerdictStop || o.Proposal != nil {
		t.Fatalf("a dismissed condition stays stopped: %+v", o)
	}
	if f.sink.count() != 1 || len(fake.Calls()) != 1 {
		t.Fatalf("issues %d, System One calls %d; want 1 and 1", f.sink.count(), len(fake.Calls()))
	}
}

func TestSystemOneCautionDowngradesAProposal(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		verdict pkgdecision.Verdict
		kind    pkgdecision.Kind
		want    pkgdecision.Verdict
		reason  string
	}{
		"observe":     {pkgdecision.VerdictObserve, "", pkgdecision.VerdictObserve, decision.ReasonSystemOneCaution},
		"stop":        {pkgdecision.VerdictStop, "", pkgdecision.VerdictStop, decision.ReasonSystemOneCaution},
		"other kind":  {pkgdecision.VerdictPropose, pkgdecision.KindNewFeature, pkgdecision.VerdictInvestigate, decision.ReasonSystemOneKindDoubt},
		"more doubts": {pkgdecision.VerdictCollectMoreEvidence, "", pkgdecision.VerdictCollectMoreEvidence, decision.ReasonSystemOneCaution},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, fake := newSystemOneFixture(t)
			fake.Answer(func(req pkgdecision.Request) pkgdecision.Judgment {
				j := judgmentFor(req, c.verdict, c.kind, pkgdecision.PriorityLow)
				j.Uncertainties = []string{"baseline_short"}
				return j
			})
			f.regress(t, "GET /a")
			o := f.window(t, "GET /a", 1000, 100)
			if o.Verdict != c.want || o.Reason != c.reason || o.Proposal != nil || o.Chosen() != decision.OptionDoNothing ||
				f.sink.count() != 0 || len(o.Uncertainties) != 1 {
				t.Fatalf("outcome %+v, issues %d", o, f.sink.count())
			}
		})
	}
}

// The rules propose medium priority for a regression; System One may only
// keep or lower it, never raise it.
func TestSystemOneLowersPriorityButNeverRaisesIt(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ asked, want pkgdecision.Priority }{
		{pkgdecision.PriorityLow, pkgdecision.PriorityLow},
		{pkgdecision.PriorityMedium, pkgdecision.PriorityMedium},
		{pkgdecision.PriorityHigh, pkgdecision.PriorityMedium},
		{pkgdecision.PriorityUrgent, pkgdecision.PriorityMedium},
	} {
		f, fake := newSystemOneFixture(t)
		fake.Answer(func(req pkgdecision.Request) pkgdecision.Judgment {
			return judgmentFor(req, pkgdecision.VerdictPropose, pkgdecision.KindBug, c.asked)
		})
		f.regress(t, "GET /a")
		o := f.window(t, "GET /a", 1000, 100)
		if o.Proposal == nil || o.Proposal.Priority != c.want || f.sink.issues[0].Priority != c.want ||
			o.Proposal.Policy.ModelRoute != jevRoute || o.Proposal.Policy.DecisionSHA != policyRefs().DecisionSHA {
			t.Fatalf("asked %s: outcome %+v", c.asked, o)
		}
		req := fake.Calls()[0].Request.Request
		if req.SchemaVersion != 1 || len(req.Questions) != 3 || req.Policy != policyRefs() || req.Evidence.SampleCount != 1000 {
			t.Fatalf("System One was asked %+v", req)
		}
	}
}

func TestAFailedOrInconsistentJudgmentProposesNothing(t *testing.T) {
	t.Parallel()
	failures := map[string]func(*systemonetest.Server){
		"server error": func(s *systemonetest.Server) {
			s.Reply(func(systemonetest.Call) systemonetest.Reply {
				return systemonetest.Reply{Status: http.StatusBadGateway}
			})
		},
		"schema violation": func(s *systemonetest.Server) {
			s.Answer(func(req pkgdecision.Request) pkgdecision.Judgment {
				j := alwaysDoIt(req)
				j.Verdict = "safe, do it"
				return j
			})
		},
		"answers contradict fields": func(s *systemonetest.Server) {
			s.Answer(func(req pkgdecision.Request) pkgdecision.Judgment {
				j := judgmentFor(req, pkgdecision.VerdictObserve, pkgdecision.KindBug, pkgdecision.PriorityLow)
				j.Verdict, j.Kind = pkgdecision.VerdictPropose, pkgdecision.KindBug
				return j
			})
		},
	}
	for name, fail := range failures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, fake := newSystemOneFixture(t)
			fail(fake)
			f.regress(t, "GET /a")
			f.counters.add("GET /a", 1000, 100)
			o, err := f.runAllowingErrors(t, "GET /a")
			if !core.HasCode(err, core.CodeUnavailable) || o.Verdict != pkgdecision.VerdictCollectMoreEvidence ||
				o.Reason != decision.ReasonSystemOneFailed || o.Proposal != nil || f.sink.count() != 0 {
				t.Fatalf("must fail closed: %+v, %v", o, err)
			}
			fake.Answer(alwaysDoIt)
			if o := f.window(t, "GET /a", 1000, 100); !o.NewProposal || f.sink.count() != 1 {
				t.Fatalf("the proposal is offered again once System One answers: %+v", o)
			}
		})
	}
}

func TestSystemOneNeedsQuestionsReadAtThePinnedDecisionCommit(t *testing.T) {
	t.Parallel()
	tq := typedQuestions(t)
	stale := tq
	stale.Revision = core.CommitSHA("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	for name, q := range map[string]decision.TypedQuestions{"none": {}, "other commit": stale} {
		_, err := decision.New(decision.Config{Source: newCounters(), Sink: &issueSink{}, Redactor: redact.New(redact.Config{}),
			Policy: policyRefs(), Rules: decision.DefaultRules(), SystemOne: stubSystemOne{}, Questions: q})
		if !core.HasCode(err, core.CodePolicyHold) {
			t.Errorf("%s: want %s, got %v", name, core.CodePolicyHold, err)
		}
	}
}

type stubSystemOne struct{}

func (stubSystemOne) Judge(context.Context, pkgdecision.Request) (pkgdecision.Judgment, error) {
	return pkgdecision.Judgment{}, errBoom
}

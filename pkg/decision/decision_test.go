package decision_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/decision"
)

const sha = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")

var (
	from = time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	to   = from.Add(24 * time.Hour)
)

func TestVerdictsAreTheSevenOutcomesAndOnlyTwoCreateWork(t *testing.T) {
	want := []decision.Verdict{decision.VerdictIgnore, decision.VerdictObserve, decision.VerdictInvestigate,
		decision.VerdictPropose, decision.VerdictImplement, decision.VerdictStop, decision.VerdictCollectMoreEvidence}
	if got := decision.Verdicts(); !slices.Equal(got, want) {
		t.Fatalf("Verdicts() = %v", got)
	}
	creates := map[decision.Verdict]bool{decision.VerdictPropose: true, decision.VerdictImplement: true}
	for _, v := range want {
		if !v.Valid() || v.CreatesWork() != creates[v] {
			t.Errorf("verdict %q: valid=%v createsWork=%v", v, v.Valid(), v.CreatesWork())
		}
	}
	if string(decision.VerdictCollectMoreEvidence) != "collect_more_evidence" {
		t.Errorf("wire name changed: %q", decision.VerdictCollectMoreEvidence)
	}
	if decision.Verdict("approve").Valid() || decision.Verdict("").CreatesWork() {
		t.Error("an unknown verdict is invalid and creates nothing")
	}
}

func TestKindsThatWidenTheProductNeedTheOwner(t *testing.T) {
	needsOwner := map[decision.Kind]bool{decision.KindNewFeature: true, decision.KindPolicyChange: true}
	kinds := decision.Kinds()
	if len(kinds) != 4 {
		t.Fatalf("Kinds() = %v", kinds)
	}
	for _, k := range kinds {
		if !k.Valid() || k.RequiresOwnerApproval() != needsOwner[k] {
			t.Errorf("kind %q: valid=%v requiresOwner=%v", k, k.Valid(), k.RequiresOwnerApproval())
		}
	}
	if !decision.Kind("refactor").RequiresOwnerApproval() || decision.Kind("refactor").Valid() {
		t.Error("an unknown kind is invalid and treated as needing the Owner")
	}
}

func TestPrioritiesAreOrdered(t *testing.T) {
	levels := decision.Priorities()
	if len(levels) != 4 {
		t.Fatalf("Priorities() = %v", levels)
	}
	for i, p := range levels {
		if !p.Valid() || p.Rank() != i+1 {
			t.Errorf("priority %q: valid=%v rank=%d", p, p.Valid(), p.Rank())
		}
	}
	if decision.Priority("asap").Valid() || decision.Priority("asap").Rank() != 0 {
		t.Error("an unknown priority is invalid and ranks below every level")
	}
}

func validSignal() decision.Signal {
	return decision.Signal{Fingerprint: "edge.5xx_rate:GET /orders", Source: "edge", Metric: "http.5xx_rate",
		Feature: "orders", Value: 0.08, Baseline: 0.01, SampleCount: 1200, From: from, To: to,
		SourceRefs: []string{"telemetry:edge/2026-11-09"}}
}

func validEvidence() decision.Evidence {
	return decision.Evidence{Metric: "http.5xx_rate", Value: 0.08, Baseline: 0.01, Trend: decision.TrendRising,
		SampleCount: 1200, From: from, To: to, Correlations: []string{"acme/app#12"},
		UserImpact: "8% of order requests fail", Recurrence: 3, Confidence: 0.9,
		SourceRefs: []string{"telemetry:edge/2026-11-09"}}
}

func TestSignalAndEvidenceValidation(t *testing.T) {
	if err := validSignal().Validate(); err != nil {
		t.Fatalf("valid signal rejected: %v", err)
	}
	if err := validEvidence().Validate(); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
	signals := map[string]func(*decision.Signal){
		"no fingerprint":   func(s *decision.Signal) { s.Fingerprint = "" },
		"no metric":        func(s *decision.Signal) { s.Metric = "" },
		"no source":        func(s *decision.Signal) { s.Source = "" },
		"negative samples": func(s *decision.Signal) { s.SampleCount = -1 },
		"empty time range": func(s *decision.Signal) { s.To = s.From },
		"no source refs":   func(s *decision.Signal) { s.SourceRefs = nil },
	}
	for name, mutate := range signals {
		s := validSignal()
		mutate(&s)
		if core.CodeOf(s.Validate()) != core.CodeInvalidArgument {
			t.Errorf("signal %s: want %s, got %v", name, core.CodeInvalidArgument, s.Validate())
		}
	}
	evidence := map[string]func(*decision.Evidence){
		"no metric":            func(e *decision.Evidence) { e.Metric = "" },
		"unknown trend":        func(e *decision.Evidence) { e.Trend = "wobbly" },
		"negative samples":     func(e *decision.Evidence) { e.SampleCount = -5 },
		"reversed time range":  func(e *decision.Evidence) { e.From, e.To = e.To, e.From },
		"confidence above one": func(e *decision.Evidence) { e.Confidence = 1.2 },
		"negative confidence":  func(e *decision.Evidence) { e.Confidence = -0.1 },
		"negative recurrence":  func(e *decision.Evidence) { e.Recurrence = -1 },
		"no source refs":       func(e *decision.Evidence) { e.SourceRefs = nil },
	}
	for name, mutate := range evidence {
		e := validEvidence()
		mutate(&e)
		if core.CodeOf(e.Validate()) != core.CodeInvalidArgument {
			t.Errorf("evidence %s: want %s, got %v", name, core.CodeInvalidArgument, e.Validate())
		}
	}
}

func TestEvidenceIsSufficientOnlyWithEnoughSamples(t *testing.T) {
	e := validEvidence()
	if !e.Sufficient(1200) || e.Sufficient(1201) {
		t.Fatal("evidence is sufficient exactly when it reaches the sample minimum")
	}
	if e.Sufficient(0) {
		t.Fatal("a minimum that was never configured must not make evidence sufficient")
	}
	e.SourceRefs = nil
	if e.Sufficient(1) {
		t.Fatal("evidence that cannot be traced to a source is not sufficient")
	}
}

func validRequest() decision.Request {
	return decision.Request{
		Evidence: validEvidence(), Goal: "Bring the order error rate back to baseline.",
		Questions: []decision.Question{
			{ID: "user_harm", Text: "Does this harm users now?", Options: []string{"yes", "no", "unknown"}, Weight: 3},
			{ID: "in_catalog", Text: "Is the affected feature in the catalogue?", Options: []string{"yes", "no"}, Weight: 2},
		},
		Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			ModelRoute: core.ModelRoute{Provider: "rules", Model: "deterministic"}},
		SchemaVersion: 1,
	}
}

func validJudgment() decision.Judgment {
	return decision.Judgment{Verdict: decision.VerdictPropose, Kind: decision.KindBug, Priority: decision.PriorityHigh,
		Answers:       []decision.Answer{{QuestionID: "user_harm", Value: "yes"}, {QuestionID: "in_catalog", Value: "yes"}},
		Model:         core.ModelRoute{Provider: "rules", Model: "deterministic"},
		SchemaVersion: 1}
}

func TestJudgmentThatBreaksTheSchemaIsRejected(t *testing.T) {
	req := validRequest()
	if err := validJudgment().Validate(req); err != nil {
		t.Fatalf("valid judgment rejected: %v", err)
	}
	quiet := validJudgment()
	quiet.Verdict, quiet.Kind = decision.VerdictIgnore, ""
	if err := quiet.Validate(req); err != nil {
		t.Fatalf("a verdict that creates no work needs no kind: %v", err)
	}
	cases := map[string]func(*decision.Judgment){
		"free-text verdict":        func(j *decision.Judgment) { j.Verdict = "looks fine" },
		"work without a kind":      func(j *decision.Judgment) { j.Kind = "" },
		"unknown kind":             func(j *decision.Judgment) { j.Kind = "chore" },
		"unknown priority":         func(j *decision.Judgment) { j.Priority = "asap" },
		"another schema version":   func(j *decision.Judgment) { j.SchemaVersion = 2 },
		"question left unanswered": func(j *decision.Judgment) { j.Answers = j.Answers[:1] },
		"answer outside options":   func(j *decision.Judgment) { j.Answers[0].Value = "probably" },
		"answer to unknown question": func(j *decision.Judgment) {
			j.Answers = append(j.Answers, decision.Answer{QuestionID: "grant_scope", Value: "yes"})
		},
		"question answered twice": func(j *decision.Judgment) {
			j.Answers = append(j.Answers, decision.Answer{QuestionID: "user_harm", Value: "no"})
		},
		"model not recorded": func(j *decision.Judgment) { j.Model = core.ModelRoute{} },
	}
	for name, mutate := range cases {
		j := validJudgment()
		mutate(&j)
		if core.CodeOf(j.Validate(req)) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, j.Validate(req))
		}
	}
}

func TestProposalValidation(t *testing.T) {
	good := decision.Proposal{Fingerprint: "edge.5xx_rate:GET /orders", Goal: "Bring the order error rate back to baseline.",
		Options: []string{"fix the handler", "roll back"}, DraftSoW: "Fix the nil map in the order handler.",
		DoD: "5xx rate of GET /orders below 1% over 24h.", Verdict: decision.VerdictPropose, Kind: decision.KindBug,
		Priority: decision.PriorityHigh, Evidence: validEvidence(), Policy: validRequest().Policy,
		AffectedFeatures: []string{"orders"}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	cases := map[string]func(*decision.Proposal){
		"no fingerprint":           func(p *decision.Proposal) { p.Fingerprint = "" },
		"no goal":                  func(p *decision.Proposal) { p.Goal = " " },
		"no scope of work":         func(p *decision.Proposal) { p.DraftSoW = "" },
		"no definition of done":    func(p *decision.Proposal) { p.DoD = "" },
		"verdict creates no work":  func(p *decision.Proposal) { p.Verdict = decision.VerdictCollectMoreEvidence },
		"unknown kind":             func(p *decision.Proposal) { p.Kind = "" },
		"unknown priority":         func(p *decision.Proposal) { p.Priority = "" },
		"evidence without sources": func(p *decision.Proposal) { p.Evidence.SourceRefs = nil },
		"policy not pinned":        func(p *decision.Proposal) { p.Policy.DecisionSHA = "" },
	}
	for name, mutate := range cases {
		p := good
		mutate(&p)
		if core.CodeOf(p.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, p.Validate())
		}
	}
}

// rulesStub proves the contract can be met by deterministic rules alone.
type rulesStub struct{ minSamples int64 }

func (r rulesStub) Judge(_ context.Context, req decision.Request) (decision.Judgment, error) {
	if !req.Evidence.Sufficient(r.minSamples) {
		return decision.Judgment{Verdict: decision.VerdictCollectMoreEvidence, Priority: decision.PriorityLow,
			Model: req.Policy.ModelRoute, SchemaVersion: req.SchemaVersion}, nil
	}
	return decision.Judgment{}, core.NewError(core.CodeTimeout, "model did not answer")
}

func TestSystemOneContractIsImplementable(t *testing.T) {
	var model decision.SystemOne = rulesStub{minSamples: 5000}
	req := validRequest()
	req.Questions = nil
	j, err := model.Judge(context.Background(), req)
	if err != nil || j.Verdict.CreatesWork() || j.Validate(req) != nil {
		t.Fatalf("too few samples must yield a valid verdict that creates no work, got %+v, %v", j, err)
	}
	model = rulesStub{minSamples: 10}
	if j, err := model.Judge(context.Background(), req); err == nil || j.Verdict.CreatesWork() {
		t.Fatalf("a failed judgment must not create work, got %+v, %v", j, err)
	}
}

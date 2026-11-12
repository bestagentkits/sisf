package core

import (
	"testing"
	"time"
)

func validReceipt() QAReceipt {
	issued := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	return QAReceipt{
		FactoryID:           "acme-factory",
		IssueID:             IssueRef{Owner: "acme", Repo: "app", Number: 7},
		RunID:               "run-0001",
		CandidateSHA:        testSHA,
		CandidateDigest:     testDigest,
		SuiteDigest:         testDigest,
		BaselineSHA:         testSHA,
		PolicySHA:           testSHA,
		QAAgentIdentity:     "agent:qa-1",
		CodingAgentIdentity: "agent:coder-1",
		CodingModelRoute:    ModelRoute{Provider: "anthropic", Model: "mid", Effort: "low"},
		QAModelRoute:        ModelRoute{Provider: "openai", Model: "mid", Effort: "low"},
		Checks: []QACheck{
			{Name: "tests_contract", Kind: CheckHardGate, Passed: true},
			{Name: "correctness_regression", Kind: CheckRubric, Passed: true, Points: 28, MaxPoints: 30},
		},
		Score:            90,
		IssuedAt:         issued,
		ExpiresAt:        issued.Add(time.Hour),
		EvaluatorAttempt: 1,
		Signature:        Signature{KeyID: "qa-2026-11", Algorithm: "ed25519", Value: []byte{1}},
	}
}

func TestQAReceiptValidation(t *testing.T) {
	if err := validReceipt().Validate(); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	cases := map[string]func(*QAReceipt){
		"names no factory":         func(r *QAReceipt) { r.FactoryID = "" },
		"malformed factory":        func(r *QAReceipt) { r.FactoryID = "Acme Factory" },
		"names no issue":           func(r *QAReceipt) { r.IssueID = IssueRef{} },
		"issue without a number":   func(r *QAReceipt) { r.IssueID.Number = 0 },
		"names no run":             func(r *QAReceipt) { r.RunID = "" },
		"malformed run id":         func(r *QAReceipt) { r.RunID = "Run 1" },
		"unbuilt candidate":        func(r *QAReceipt) { r.CandidateDigest = "" },
		"candidate not a commit":   func(r *QAReceipt) { r.CandidateSHA = "feature-branch" },
		"suite digest missing":     func(r *QAReceipt) { r.SuiteDigest = "" },
		"baseline not pinned":      func(r *QAReceipt) { r.BaselineSHA = "main" },
		"policy not pinned":        func(r *QAReceipt) { r.PolicySHA = "" },
		"anonymous evaluator":      func(r *QAReceipt) { r.QAAgentIdentity = "" },
		"anonymous coder":          func(r *QAReceipt) { r.CodingAgentIdentity = "" },
		"evaluator scored itself":  func(r *QAReceipt) { r.QAAgentIdentity = r.CodingAgentIdentity },
		"blank evaluator":          func(r *QAReceipt) { r.QAAgentIdentity = "  " },
		"blank coder":              func(r *QAReceipt) { r.CodingAgentIdentity = "\t" },
		"coding route missing":     func(r *QAReceipt) { r.CodingModelRoute = ModelRoute{} },
		"qa route missing":         func(r *QAReceipt) { r.QAModelRoute = ModelRoute{} },
		"same route not disclosed": func(r *QAReceipt) { r.QAModelRoute = r.CodingModelRoute },
		"same route overclaimed":   func(r *QAReceipt) { r.SameModelRoute = true },
		"no checks":                func(r *QAReceipt) { r.Checks = nil },
		"unnamed check":            func(r *QAReceipt) { r.Checks[0].Name = "" },
		"unknown check kind":       func(r *QAReceipt) { r.Checks[0].Kind = "vibes" },
		"points above maximum":     func(r *QAReceipt) { r.Checks[1].Points = 31 },
		"score above range":        func(r *QAReceipt) { r.Score = 101 },
		"score below range":        func(r *QAReceipt) { r.Score = -1 },
		"no issue time":            func(r *QAReceipt) { r.IssuedAt = time.Time{} },
		"expires before issue":     func(r *QAReceipt) { r.ExpiresAt = r.IssuedAt },
		"attempt below one":        func(r *QAReceipt) { r.EvaluatorAttempt = 0 },
		"unsigned":                 func(r *QAReceipt) { r.Signature = Signature{} },
	}
	for name, mutate := range cases {
		r := validReceipt()
		mutate(&r)
		if CodeOf(r.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, CodeInvalidArgument, r.Validate())
		}
	}
}

func TestAnEvaluatorCannotPassAsAnotherPartyByRespellingItsIdentity(t *testing.T) {
	t.Parallel()
	for name, evaluator := range map[string]string{
		"trailing space":  "agent:coder-1 ",
		"leading space":   " agent:coder-1",
		"trailing tab":    "agent:coder-1\t",
		"trailing line":   "agent:coder-1\n",
		"upper case":      "AGENT:CODER-1",
		"mixed case":      "Agent:Coder-1",
		"case and spaces": "  Agent:CODER-1 ",
		// Characters that render as nothing do not make another party.
		"zero-width space":          "agent:\u200bcoder-1",
		"zero-width joiner":         "agent:coder\u200d-1",
		"byte order mark":           "\ufeffagent:coder-1",
		"soft hyphen":               "agent:co\u00adder-1",
		"word joiner":               "agent:coder-1\u2060",
		"variation selector":        "agent:coder-1\ufe0f",
		"combining grapheme joiner": "agent:\u034fcoder-1",
		"hangul filler":             "agent:coder-1\u3164",
		"invisible and case":        " AGENT:\u200bCoder-1\u200c",
	} {
		r := validReceipt()
		r.QAAgentIdentity = evaluator
		if CodeOf(r.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: evaluator %q scored coder %q: want %s, got %v",
				name, evaluator, r.CodingAgentIdentity, CodeInvalidArgument, r.Validate())
		}
		if !SameIdentity(evaluator, r.CodingAgentIdentity) || !SameIdentity(r.CodingAgentIdentity, evaluator) {
			t.Errorf("%s: %q and %q name the same party", name, evaluator, r.CodingAgentIdentity)
		}
	}
	if SameIdentity("agent:qa-1", "agent:coder-1") || SameIdentity("agent:qa-1", "agent:qa-10") {
		t.Error("two different identities were taken for one party")
	}
}

func TestASharedModelRouteCannotBeHiddenByRespellingIt(t *testing.T) {
	t.Parallel()
	for name, route := range map[string]ModelRoute{
		"provider in upper case": {Provider: "ANTHROPIC", Model: "mid"},
		"model in upper case":    {Provider: "anthropic", Model: "MID"},
		"both in mixed case":     {Provider: "Anthropic", Model: "Mid", Effort: "high"},
		"padded with spaces":     {Provider: " anthropic", Model: "mid "},
	} {
		r := validReceipt()
		r.QAModelRoute = route
		if !r.CodingModelRoute.SameModel(route) || !route.SameModel(r.CodingModelRoute) {
			t.Errorf("%s: %s and %s are the same model", name, route, r.CodingModelRoute)
		}
		if CodeOf(r.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: an undisclosed shared route: want %s, got %v", name, CodeInvalidArgument, r.Validate())
		}
		r.SameModelRoute = true
		if err := r.Validate(); err != nil {
			t.Errorf("%s: a disclosed shared route is a valid receipt: %v", name, err)
		}
	}
	for name, route := range map[string]ModelRoute{
		"blank provider": {Provider: " ", Model: "mid"},
		"blank model":    {Provider: "anthropic", Model: "\t"},
	} {
		if CodeOf(route.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, CodeInvalidArgument, route.Validate())
		}
	}
}

func TestReceiptDisclosesASharedModelRoute(t *testing.T) {
	r := validReceipt()
	r.QAModelRoute = r.CodingModelRoute
	r.QAModelRoute.Effort = "high"
	r.SameModelRoute = true
	if err := r.Validate(); err != nil {
		t.Fatalf("a disclosed shared route is a valid receipt: %v", err)
	}
}

func TestAHighScoreNeverHidesAFailedHardGate(t *testing.T) {
	r := validReceipt()
	r.Score = 100
	r.Checks = append(r.Checks, QACheck{Name: "secrets", Kind: CheckHardGate, Passed: false})
	if r.HardGatesPassed() {
		t.Fatal("a failed hard gate must fail the receipt whatever the score")
	}
}

func TestAReceiptWithoutHardGateEvidenceDoesNotPass(t *testing.T) {
	r := validReceipt()
	r.Checks = []QACheck{{Name: "correctness_regression", Kind: CheckRubric, Passed: true, Points: 30, MaxPoints: 30}}
	if r.HardGatesPassed() {
		t.Fatal("no hard-gate result means no proof the gates ran")
	}
	if !validReceipt().HardGatesPassed() {
		t.Fatal("every recorded hard gate passed")
	}
}

func TestReceiptExpiresAtItsDeadline(t *testing.T) {
	r := validReceipt()
	if r.Expired(r.ExpiresAt.Add(-time.Nanosecond)) {
		t.Error("the receipt is still valid just before its deadline")
	}
	if !r.Expired(r.ExpiresAt) || !r.Expired(r.ExpiresAt.Add(time.Minute)) {
		t.Error("the receipt is expired at and after its deadline")
	}
}

func TestSignatureMustNameItsKeyAndAlgorithm(t *testing.T) {
	good := Signature{KeyID: "k1", Algorithm: "ed25519", Value: []byte{1, 2}}
	if good.IsZero() || good.Validate() != nil {
		t.Fatal("a complete signature is valid")
	}
	for name, s := range map[string]Signature{
		"zero":         {},
		"no key":       {Algorithm: "ed25519", Value: []byte{1}},
		"no algorithm": {KeyID: "k1", Value: []byte{1}},
		"no value":     {KeyID: "k1", Algorithm: "ed25519"},
	} {
		if CodeOf(s.Validate()) != CodeInvalidArgument {
			t.Errorf("%s: want %s", name, CodeInvalidArgument)
		}
	}
	if !(Signature{}).IsZero() {
		t.Error("the empty signature is zero")
	}
}

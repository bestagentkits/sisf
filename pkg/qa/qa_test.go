package qa_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

const (
	sha    = core.CommitSHA("0123456789abcdef0123456789abcdef01234567")
	digest = core.Digest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
)

func TestStartingRubricWeighsFiveCategoriesToOneHundred(t *testing.T) {
	rubric := qa.DefaultRubric()
	want := map[qa.Category]int{
		qa.CategoryCorrectness:   30,
		qa.CategorySecurity:      25,
		qa.CategoryContract:      20,
		qa.CategoryExperience:    15,
		qa.CategoryObservability: 10,
	}
	if len(rubric.Weights) != len(want) || rubric.PassScore != 85 {
		t.Fatalf("DefaultRubric() = %+v", rubric)
	}
	for category, weight := range want {
		if rubric.Weights[category] != weight {
			t.Errorf("weight of %s = %d, want %d", category, rubric.Weights[category], weight)
		}
	}
	if err := rubric.Validate(); err != nil {
		t.Fatalf("the starting rubric must be valid: %v", err)
	}
	if len(qa.Categories()) != 5 {
		t.Fatalf("Categories() = %v", qa.Categories())
	}
	rubric.Weights[qa.CategoryCorrectness] = 0
	if qa.DefaultRubric().Weights[qa.CategoryCorrectness] != 30 {
		t.Fatal("a caller changed the starting rubric for everyone")
	}
}

func TestRubricValidation(t *testing.T) {
	cases := map[string]func(*qa.Rubric){
		"weights below 100":    func(r *qa.Rubric) { r.Weights[qa.CategorySecurity] = 5 },
		"missing category":     func(r *qa.Rubric) { delete(r.Weights, qa.CategoryObservability) },
		"unknown category":     func(r *qa.Rubric) { r.Weights["vibes"] = 0 },
		"negative weight":      func(r *qa.Rubric) { r.Weights[qa.CategoryContract] = -20; r.Weights[qa.CategoryCorrectness] = 70 },
		"pass score of zero":   func(r *qa.Rubric) { r.PassScore = 0 },
		"pass score above 100": func(r *qa.Rubric) { r.PassScore = 101 },
	}
	for name, mutate := range cases {
		rubric := qa.DefaultRubric()
		mutate(&rubric)
		if core.CodeOf(rubric.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, rubric.Validate())
		}
	}
}

func TestThereAreNineHardGates(t *testing.T) {
	want := []qa.GateName{qa.GateTestsContract, qa.GatePermissions, qa.GateMigrations, qa.GateSecrets,
		qa.GateObservedBuild, qa.GateIndependentEvaluator, qa.GateResourceBudget, qa.GateRollbackReadiness,
		qa.GateSupplyChain}
	if got := qa.Gates(); !slices.Equal(got, want) {
		t.Fatalf("Gates() = %v, want %v", got, want)
	}
}

func allGatesPassed() []core.QACheck {
	var checks []core.QACheck
	for _, g := range qa.Gates() {
		checks = append(checks, qa.GateResult{Gate: g, Passed: true}.Check())
	}
	return checks
}

func receipt(score int, checks []core.QACheck) core.QAReceipt {
	return core.QAReceipt{Score: score, Checks: checks}
}

func TestCandidatePassesOnlyWithTheScoreAndEveryHardGate(t *testing.T) {
	rubric := qa.DefaultRubric()
	if !rubric.Passes(receipt(85, allGatesPassed())) {
		t.Fatal("the pass score with every gate passed is a pass")
	}
	if rubric.Passes(receipt(84, allGatesPassed())) {
		t.Fatal("a score below the pass score fails")
	}
	failed := allGatesPassed()
	failed[3].Passed = false
	if rubric.Passes(receipt(100, failed)) {
		t.Fatal("a perfect score must not override a failed hard gate")
	}
	missing := allGatesPassed()[:8]
	if rubric.Passes(receipt(100, missing)) {
		t.Fatal("a gate that was never run counts as not passed")
	}
	if got := qa.MissingGates(missing); !slices.Equal(got, []qa.GateName{qa.GateSupplyChain}) {
		t.Fatalf("MissingGates = %v", got)
	}
	if len(qa.MissingGates(allGatesPassed())) != 0 {
		t.Fatal("no gate is missing when all were recorded")
	}
	relabelled := allGatesPassed()
	relabelled[0].Kind = core.CheckRubric
	if rubric.Passes(receipt(100, relabelled)) {
		t.Fatal("a rubric entry named like a gate is not a hard-gate result")
	}
}

func TestGateResultBecomesAHardGateCheck(t *testing.T) {
	check := qa.GateResult{Gate: qa.GateSecrets, Passed: false, Detail: "token found in diff", EvidenceRef: "logs/secrets.txt"}.Check()
	want := core.QACheck{Name: "secrets", Kind: core.CheckHardGate, Passed: false, Detail: "token found in diff", EvidenceRef: "logs/secrets.txt"}
	if check != want {
		t.Fatalf("Check() = %+v", check)
	}
}

func validCandidate() qa.Candidate {
	return qa.Candidate{
		IssueID: core.IssueRef{Owner: "acme", Repo: "app", Number: 7}, RunID: "run-0001",
		CommitSHA: sha, Digest: digest, BaselineSHA: sha, SuiteDigest: digest,
		Policy: core.PolicyRefs{PolicySHA: sha, ReviewSHA: sha, DecisionSHA: sha, FeatureCatalogSHA: sha,
			ModelRoute: core.ModelRoute{Provider: "openai", Model: "mid"}},
		CodingAgentIdentity: "agent:coder-1",
		CodingModelRoute:    core.ModelRoute{Provider: "anthropic", Model: "mid"},
		Attempt:             1,
	}
}

func TestCandidateValidation(t *testing.T) {
	if err := validCandidate().Validate(); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
	cases := map[string]func(*qa.Candidate){
		"not built yet":       func(c *qa.Candidate) { c.Digest = "" },
		"not a commit":        func(c *qa.Candidate) { c.CommitSHA = "feature-branch" },
		"baseline not pinned": func(c *qa.Candidate) { c.BaselineSHA = "" },
		"suite not pinned":    func(c *qa.Candidate) { c.SuiteDigest = "" },
		"policy not pinned":   func(c *qa.Candidate) { c.Policy.ReviewSHA = "" },
		"anonymous coder":     func(c *qa.Candidate) { c.CodingAgentIdentity = "" },
		"coder route unknown": func(c *qa.Candidate) { c.CodingModelRoute = core.ModelRoute{} },
		"no run":              func(c *qa.Candidate) { c.RunID = "" },
		"attempt below one":   func(c *qa.Candidate) { c.Attempt = 0 },
	}
	for name, mutate := range cases {
		c := validCandidate()
		mutate(&c)
		if core.CodeOf(c.Validate()) != core.CodeInvalidArgument {
			t.Errorf("%s: want %s, got %v", name, core.CodeInvalidArgument, c.Validate())
		}
	}
}

// gateStub and evaluatorStub prove the contracts can be met with the exported types alone.
type gateStub struct{}

func (gateStub) Name() qa.GateName { return qa.GateSupplyChain }
func (gateStub) Check(context.Context, qa.Candidate) (qa.GateResult, error) {
	return qa.GateResult{Gate: qa.GateSupplyChain, Passed: false, Detail: "new dependency without approval"}, nil
}

type evaluatorStub struct{ gates []qa.HardGate }

func (e evaluatorStub) Evaluate(ctx context.Context, c qa.Candidate) (core.QAReceipt, error) {
	if err := c.Validate(); err != nil {
		return core.QAReceipt{}, err
	}
	r := core.QAReceipt{CandidateSHA: c.CommitSHA, CandidateDigest: c.Digest, Score: 99, IssuedAt: time.Now()}
	for _, g := range e.gates {
		result, err := g.Check(ctx, c)
		if err != nil {
			return core.QAReceipt{}, err
		}
		r.Checks = append(r.Checks, result.Check())
	}
	return r, nil
}

func TestEvaluatorAndGateContractsAreImplementable(t *testing.T) {
	var evaluator qa.Evaluator = evaluatorStub{gates: []qa.HardGate{gateStub{}}}
	unbuilt := validCandidate()
	unbuilt.Digest = ""
	if _, err := evaluator.Evaluate(context.Background(), unbuilt); !core.HasCode(err, core.CodeInvalidArgument) {
		t.Fatalf("code that has not finished building cannot be scored, got %v", err)
	}
	r, err := evaluator.Evaluate(context.Background(), validCandidate())
	if err != nil {
		t.Fatal(err)
	}
	if qa.DefaultRubric().Passes(r) {
		t.Fatal("a failed supply-chain gate blocks the candidate whatever its score")
	}
}

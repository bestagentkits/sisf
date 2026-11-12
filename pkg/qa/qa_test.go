package qa_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/pkg/core"
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
	// The rubric is the one the release step judges a receipt against.
	for i, category := range core.RubricCategories() {
		if qa.Categories()[i] != qa.Category(category.Name) || rubric.Weights[qa.Category(category.Name)] != category.MaxPoints {
			t.Errorf("category %s at %d is not the one the release step scores", category.Name, category.MaxPoints)
		}
	}
	rubric.Weights[qa.CategoryCorrectness] = 0
	if qa.DefaultRubric().Weights[qa.CategoryCorrectness] != 30 {
		t.Fatal("a caller changed the starting rubric for everyone")
	}
}

func TestRubricValidation(t *testing.T) {
	cases := map[string]func(*qa.Rubric){
		"weights below 100": func(r *qa.Rubric) { r.Weights[qa.CategorySecurity] = 5 },
		"weight moved between categories": func(r *qa.Rubric) {
			r.Weights[qa.CategoryCorrectness], r.Weights[qa.CategorySecurity] = 45, 10
		},
		"one category weighs everything": func(r *qa.Rubric) {
			r.Weights = map[qa.Category]int{qa.CategoryCorrectness: 100, qa.CategorySecurity: 0,
				qa.CategoryContract: 0, qa.CategoryExperience: 0, qa.CategoryObservability: 0}
		},
		"no weights":           func(r *qa.Rubric) { r.Weights = nil },
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

// receipt records the given gate results and a score earned in full as rubric
// points: every category of the rubric is scored once, filled in order up to
// its weight.
func receipt(score int, checks []core.QACheck) core.QAReceipt {
	scored, left := slices.Clone(checks), score
	for _, category := range core.RubricCategories() {
		points := min(left, category.MaxPoints)
		left -= points
		scored = append(scored, core.QACheck{Name: category.Name, Kind: core.CheckRubric, Passed: true,
			Points: points, MaxPoints: category.MaxPoints})
	}
	return core.QAReceipt{Score: score, Checks: scored}
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

func TestAScoreEarnedOutsideTheRubricDoesNotPass(t *testing.T) {
	rubric := qa.DefaultRubric()
	onlyRubric := func(rubricChecks ...core.QACheck) core.QAReceipt {
		r := core.QAReceipt{Checks: append(allGatesPassed(), rubricChecks...)}
		for _, c := range rubricChecks {
			r.Score += c.Points
		}
		return r
	}
	correctness := core.QACheck{Name: string(qa.CategoryCorrectness), Kind: core.CheckRubric, Passed: true, Points: 30, MaxPoints: 30}
	cases := map[string]core.QAReceipt{
		"one category scored 90 out of 100": onlyRubric(
			core.QACheck{Name: string(qa.CategoryCorrectness), Kind: core.CheckRubric, Passed: true, Points: 90, MaxPoints: 100}),
		"one category three times and another at zero": onlyRubric(correctness, correctness, correctness,
			core.QACheck{Name: string(qa.CategorySecurity), Kind: core.CheckRubric, MaxPoints: 25}),
		"two categories that swallow the weight of the other three": onlyRubric(correctness,
			core.QACheck{Name: string(qa.CategorySecurity), Kind: core.CheckRubric, Passed: true, Points: 60, MaxPoints: 70}),
	}
	for name, r := range cases {
		if r.Score < rubric.PassScore {
			t.Fatalf("%s: the fixture scores %d, below the pass score", name, r.Score)
		}
		if rubric.Passes(r) {
			t.Errorf("%s: a score of %d earned outside the rubric passed", name, r.Score)
		}
	}
	// A rubric with weights of its own cannot pass what the release step
	// would refuse, even on a receipt that passes the starting rubric.
	reweighed := qa.DefaultRubric()
	reweighed.Weights[qa.CategoryCorrectness], reweighed.Weights[qa.CategorySecurity] = 45, 10
	if reweighed.Passes(receipt(100, allGatesPassed())) {
		t.Fatal("a rubric with other weights than the review criteria passed a receipt")
	}
}

func TestTheHardGatesAreTheSetTheReleaseStepRequires(t *testing.T) {
	var names []string
	for _, gate := range qa.Gates() {
		names = append(names, string(gate))
	}
	if !slices.Equal(names, core.RequiredHardGates()) {
		t.Fatalf("Gates() = %v, but the release step requires %v", names, core.RequiredHardGates())
	}
}

func TestTheRubricAppliesTheSharedPassRule(t *testing.T) {
	rubric := qa.DefaultRubric()
	inflated := receipt(84, allGatesPassed())
	inflated.Score = 99
	if rubric.Passes(inflated) {
		t.Fatal("a score above the rubric points recorded must not pass")
	}
	unscored := core.QAReceipt{Score: 90, Checks: allGatesPassed()}
	if rubric.Passes(unscored) {
		t.Fatal("a score backed by no rubric points must not pass")
	}
	unbounded := qa.Rubric{Weights: rubric.Weights, PassScore: 0}
	if unbounded.Passes(receipt(100, allGatesPassed())) {
		t.Fatal("a rubric without a pass score must not pass anything")
	}
	// The rubric and the pass rule give one verdict on every receipt.
	receipts := map[string]core.QAReceipt{
		"passing":         receipt(85, allGatesPassed()),
		"one point short": receipt(84, allGatesPassed()),
		"gate missing":    receipt(100, allGatesPassed()[:8]),
		"inflated score":  inflated,
		"empty":           {},
	}
	for name, r := range receipts {
		if got, want := rubric.Passes(r), r.CheckPass(rubric.PassScore) == nil; got != want {
			t.Errorf("%s: the rubric says %v and the pass rule says %v", name, got, want)
		}
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

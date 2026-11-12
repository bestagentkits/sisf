//go:build unix

package agentjudge_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/policy"
	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/qa/agentjudge"
	"github.com/bestagentkits/sisf/internal/signing"
	"github.com/bestagentkits/sisf/internal/signing/delegation"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
	"github.com/bestagentkits/sisf/pkg/state/sqlstore/sqlstoretest"
)

func TestAJudgeNeedsAnAgentMaterialsAndAnAbsoluteWorkspaceRoot(t *testing.T) {
	t.Parallel()
	materials := agentjudge.MaterialsFunc(func(context.Context, qa.Candidate) (agentjudge.Materials, error) {
		return hostileMaterials(), nil
	})
	cases := map[string]agentjudge.Config{
		"no agent":          {Materials: materials, WorkspaceRoot: t.TempDir()},
		"relative root":     {Agent: stubRunner{}, Materials: materials, WorkspaceRoot: "qa"},
		"no materials":      {Agent: stubRunner{}, WorkspaceRoot: t.TempDir()},
		"negative timeout":  {Agent: stubRunner{}, Materials: materials, WorkspaceRoot: t.TempDir(), Timeout: -time.Second},
		"padded model":      {Agent: stubRunner{}, Materials: materials, WorkspaceRoot: t.TempDir(), Model: " gpt-judge"},
		"relative evidence": {Agent: stubRunner{}, Materials: materials, WorkspaceRoot: t.TempDir(), EvidenceDir: "evidence"},
	}
	for name, cfg := range cases {
		if _, err := agentjudge.New(cfg); !core.HasCode(err, core.CodeInvalidArgument) {
			t.Errorf("%s: %v, want INVALID_ARGUMENT", name, err)
		}
	}
}

// stubRunner satisfies the configuration check; it is never run.
type stubRunner struct{ agentjudge.Runner }

func TestTheJudgeRefusesToShareTheCodersIdentityOrModel(t *testing.T) {
	t.Parallel()
	steps := codexRun(codexMessage("m1", block(wireJudgment(30))))

	shared := request()
	shared.Candidate.CodingModelRoute = core.ModelRoute{Provider: "openai", Model: "gpt-coder"}
	s := newJudge(t, steps, func(c *agentjudge.Config) { c.Model = "GPT-CODER" })
	_, err := s.judge.Score(context.Background(), shared)
	wantCode(t, err, core.CodePermissionDenied)

	self := request()
	self.Identity = " " + self.Candidate.CodingAgentIdentity
	_, err = s.judge.Score(context.Background(), self)
	wantCode(t, err, core.CodePermissionDenied)
	if s.materials != 0 {
		t.Fatal("a refused judgment still gathered the candidate's materials")
	}
	if _, err := os.Stat(filepath.Join(s.record, "args.json")); !os.IsNotExist(err) {
		t.Fatal("the QA agent ran for a refused judgment")
	}

	// A model of its own on the evaluator's provider is fine.
	own := newJudge(t, steps, func(c *agentjudge.Config) { c.Model = "gpt-judge" })
	if _, err := own.judge.Score(context.Background(), shared); err != nil {
		t.Fatalf("a distinct QA model was refused: %v", err)
	}
}

// Through the evaluator: a judgment reaches a signed receipt; with one
// model route the agent runs only when the Owner acknowledged it, and the
// receipt says so.
func TestTheEvaluatorSignsTheAgentsScoresAndKeepsTheRouteRule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	steps := codexRun(codexMessage("m1", block(wireJudgment(26))))

	s := newJudge(t, steps, nil)
	result, err := newEvaluator(t, s.judge, false).Run(ctx, candidate())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || result.Receipt.Score != 96 || result.Receipt.SameModelRoute || result.Receipt.QAModelRoute != qaRoute {
		t.Fatalf("result %+v", result)
	}

	failing := newJudge(t, codexRun(codexMessage("m1", block(wireJudgment(0)))), nil)
	result, err = newEvaluator(t, failing.judge, false).Run(ctx, candidate())
	if err != nil || result.Passed || result.Action != evaluator.ActionReturnToDevelopment || result.Receipt.Score != 70 {
		t.Fatalf("a judgment below the pass score: %+v, %v", result, err)
	}

	broken := newJudge(t, codexRun(codexMessage("m1", block(wireJudgment(31)))), nil)
	result, err = newEvaluator(t, broken.judge, false).Run(ctx, candidate())
	wantRefused(t, err)
	if !result.Receipt.Signature.IsZero() || result.Passed {
		t.Fatalf("a refused judgment produced a receipt: %+v", result)
	}

	single := candidate()
	single.Policy.ModelRoute = core.ModelRoute{Provider: "openai", Model: "mid"}
	single.CodingModelRoute = core.ModelRoute{Provider: "openai", Model: "MID"}
	held := newJudge(t, steps, nil)
	_, err = newEvaluator(t, held.judge, false).Run(ctx, single)
	wantCode(t, err, core.CodePolicyHold)
	if held.materials != 0 {
		t.Fatal("the QA agent was prepared on the coder's route without the Owner's acknowledgement")
	}
	allowed := newJudge(t, steps, nil)
	result, err = newEvaluator(t, allowed.judge, true).Run(ctx, single)
	if err != nil || !result.Receipt.SameModelRoute || result.Action != evaluator.ActionOwnerApproval {
		t.Fatalf("an acknowledged single route: %+v, %v", result, err)
	}
}

// newEvaluator wires the evaluator around judge with passing gates.
func newEvaluator(t *testing.T, judge qa.Judge, sameRouteAcknowledged bool) *evaluator.Evaluator {
	t.Helper()
	start := time.Now().UTC().Truncate(time.Second)
	clock := sqlstoretest.NewClock(start)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := signing.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := delegation.NewChainVerifier(delegation.VerifierConfig{FactoryID: "acme-hello", Root: pub, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	key, err := delegation.Issue(context.Background(), root, delegation.IssueRequest{FactoryID: "acme-hello",
		Purpose: delegation.PurposeQAReceipt, Holder: delegation.HolderSupervisor, NotBefore: start, Lifetime: 24 * time.Hour}, nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := delegation.Load(context.Background(), verifier, delegation.LoadRequest{
		Holder: delegation.HolderSupervisor, Delegation: key.Delegation, PrivateKey: key.PrivateKey})
	if err != nil {
		t.Fatal(err)
	}
	review, err := os.ReadFile(filepath.Join("..", "..", "..", policy.PathReview))
	if err != nil {
		t.Fatal(err)
	}
	var gates []qa.HardGate
	for _, name := range qa.Gates() {
		if name != qa.GateIndependentEvaluator {
			gates = append(gates, passingGate(name))
		}
	}
	ev, err := evaluator.New(evaluator.Config{
		FactoryID: "acme-hello", Identity: qaIdentity, Workspace: qaWorkspace, Gates: gates, Judge: judge,
		Baselines: fixedBaseline{qa.Baseline{CommitSHA: baselineSHA, PolicySHA: policySHA, SuiteDigest: suiteDigest, WorkspaceID: qaWorkspace, ReadOnly: true}},
		Builds:    fixedBuild{}, Review: reviewFile(review), SameRouteAcknowledged: sameRouteAcknowledged,
		Signer: signer, Verifier: verifier, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

type passingGate qa.GateName

func (g passingGate) Name() qa.GateName { return qa.GateName(g) }
func (g passingGate) Check(context.Context, qa.Candidate) (qa.GateResult, error) {
	return qa.GateResult{Gate: qa.GateName(g), Passed: true, Detail: "fixture"}, nil
}

type fixedBaseline struct{ b qa.Baseline }

func (f fixedBaseline) Baseline(context.Context, qa.Candidate) (qa.Baseline, error) { return f.b, nil }

type fixedBuild struct{}

func (fixedBuild) FinishedBuild(context.Context, core.CommitSHA) (core.Digest, error) {
	return buildDigest, nil
}

type reviewFile []byte

func (r reviewFile) ReadFile(_ context.Context, _ core.CommitSHA, path string) ([]byte, error) {
	if path != policy.PathReview {
		return nil, core.NewError(core.CodeNotFound, "no such file")
	}
	return r, nil
}

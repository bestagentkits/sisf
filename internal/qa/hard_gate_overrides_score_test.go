package qa_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bestagentkits/sisf/adapters/agent/fake"
	evaluator "github.com/bestagentkits/sisf/internal/qa"
	"github.com/bestagentkits/sisf/internal/supplychain"
	"github.com/bestagentkits/sisf/pkg/agent"
	"github.com/bestagentkits/sisf/pkg/approvals"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/qa"
)

// replaceGate wires g in place of the configured gate of the same name.
func replaceGate(e *env, g qa.HardGate) {
	for i, existing := range e.cfg.Gates {
		if existing.Name() == g.Name() {
			e.cfg.Gates[i] = g
		}
	}
}

// wantFailedDespitePerfectScore runs a candidate the judge gives full marks
// and requires a signed receipt that records the score and fails anyway.
func wantFailedDespitePerfectScore(t *testing.T, e *env, failing qa.GateName) evaluator.Result {
	t.Helper()
	result, err := e.build(t).Run(context.Background(), candidate())
	if err != nil {
		t.Fatalf("a failed gate still yields a receipt as evidence: %v", err)
	}
	switch {
	case result.Receipt.Score != 100:
		t.Fatalf("the judge's score is recorded as given, got %d", result.Receipt.Score)
	case result.Passed || result.Action != evaluator.ActionReturnToDevelopment:
		t.Fatalf("a perfect score passed past a failed %s gate: %+v", failing, result)
	case core.CodeOf(result.Refusal) != core.CodeGuardFailed:
		t.Fatalf("refusal = %v, want %s", result.Refusal, core.CodeGuardFailed)
	case qa.DefaultRubric().Passes(result.Receipt) || result.Receipt.HardGatesPassed():
		t.Fatal("the receipt passes the shared rule despite the failed gate")
	}
	if err := evaluator.VerifyReceipt(context.Background(), e.verifier, result.Receipt, factory, candidate(), e.clock.Now()); err != nil {
		t.Fatalf("the failing receipt must still be signed and bound: %v", err)
	}
	return result
}

func TestAFailedHardGateOverridesAPerfectScore(t *testing.T) {
	t.Parallel()
	for _, name := range qa.Gates() {
		if name == qa.GateIndependentEvaluator {
			continue
		}
		t.Run(string(name), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			replaceGate(e, gate{name: name, passed: false})
			result := wantFailedDespitePerfectScore(t, e, name)
			if got := detailsOf(result.Refusal)["gate"]; got != string(name) {
				t.Fatalf("the refusal names gate %q, want %q", got, name)
			}
		})
	}
}

func TestAGateThatCannotRunOrMisreportsFailsTheCandidate(t *testing.T) {
	t.Parallel()
	cases := map[string]gate{
		"gate error":          {name: qa.GateMigrations, err: core.NewError(core.CodeUnavailable, "database down")},
		"result of another":   {name: qa.GateSecrets, passed: true, report: qa.GatePermissions},
		"result without name": {name: qa.GateRollbackReadiness, passed: true, report: "nothing"},
	}
	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			replaceGate(e, g)
			wantFailedDespitePerfectScore(t, e, g.name)
		})
	}
}

// changedTree is a repository whose candidate adds a dependency.
type changedTree struct{}

func (changedTree) ChangedPaths(context.Context, core.CommitSHA, core.CommitSHA) ([]string, error) {
	return []string{"go.mod"}, nil
}

func (changedTree) ReadFile(_ context.Context, commit core.CommitSHA, path string) ([]byte, error) {
	if commit == candidateSHA && path == "go.mod" {
		return []byte("module example.com/app\n\ngo 1.25\n\nrequire example.com/new v1.0.0\n"), nil
	}
	return nil, core.NewError(core.CodeNotFound, "no such file")
}

type noScans struct{}

func (noScans) Scan(context.Context, qa.Candidate) (supplychain.ScanResult, error) {
	return supplychain.ScanResult{}, core.NewError(core.CodeNotFound, "no scan")
}

type noApprovals struct{}

func (noApprovals) Approval(context.Context, qa.Candidate, core.Digest) (approvals.Approval, error) {
	return approvals.Approval{}, core.NewError(core.CodeNotFound, "no approval")
}

func TestTheSupplyChainGateOverridesAPerfectScore(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	supply, err := supplychain.NewGate(supplychain.GateConfig{FactoryID: factory, Tree: changedTree{}, Scans: noScans{},
		Approvals: noApprovals{}, ApprovalVerifier: e.verifier, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	replaceGate(e, supply)
	result := wantFailedDespitePerfectScore(t, e, qa.GateSupplyChain)
	for _, check := range result.Receipt.Checks {
		if check.Name == string(qa.GateSupplyChain) && (check.Passed || check.Kind != core.CheckHardGate) {
			t.Fatalf("the supply-chain check is recorded as %+v", check)
		}
	}
}

func TestEveryGateIsRecordedAndPassesOnlyTogether(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if err := e.judge.Script(agent.RoleQA, issue, fake.Score(scores(30))); err != nil {
		t.Fatal(err)
	}
	result, err := e.build(t).Run(context.Background(), candidate())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || result.Action != evaluator.ActionRelease || result.Refusal != nil {
		t.Fatalf("every gate passed at full marks, got %+v", result)
	}
	if missing := qa.MissingGates(result.Receipt.Checks); len(missing) != 0 {
		t.Fatalf("gates missing from the receipt: %v", missing)
	}
}

// detailsOf returns the details of a typed error.
func detailsOf(err error) map[string]string {
	var typed *core.Error
	if errors.As(err, &typed) {
		return typed.Details
	}
	return nil
}

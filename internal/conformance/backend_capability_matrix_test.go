package conformance

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bestagentkits/sisf/internal/conformance/conformancetest"
	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/agent"
	kit "github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/execution"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// gradeNew runs the self-grading kit of each interface against a backend the
// factory has never seen, after bend changed it.
func gradeNew(t *testing.T, kind kit.Kind, bend func(declared core.CapabilitySet, fail map[string]error)) kit.Row {
	t.Helper()
	ctx := context.Background()
	var row kit.Row
	var err error
	switch kind {
	case kit.KindExecution:
		backend := conformancetest.NewExecution("new-sandbox")
		bend(backend.Declared, backend.Fail)
		row, err = kit.GradeExecution(ctx, backend, kit.ExecutionProbe{
			Spec: conformancetest.SandboxSpec(), Command: execution.Command{Argv: []string{"true"}}, EndToEndProven: true})
	case kit.KindDeploy:
		backend := conformancetest.NewDeploy("new-platform")
		bend(backend.Declared, backend.Fail)
		baseline, candidate := conformancetest.Artifacts()
		row, err = kit.GradeDeploy(ctx, backend, kit.DeployProbe{Baseline: baseline, Candidate: candidate, Window: time.Minute, EndToEndProven: true})
	case kit.KindAgent:
		backend := conformancetest.NewAgent("new-agent")
		bend(backend.Declared, backend.Fail)
		row, err = kit.GradeAgent(ctx, backend, kit.AgentProbe{Task: conformancetest.Task(), Workspace: agent.WorkspaceRef{ID: "probe"}, EndToEndProven: true})
	}
	if err != nil {
		t.Fatalf("the %s backend could not be graded: %v", kind, err)
	}
	return row
}

var backendKinds = []kit.Kind{kit.KindAgent, kit.KindExecution, kit.KindDeploy}

func TestANewBackendIsGradedOnLifecycleIsolationRollbackAndObservability(t *testing.T) {
	want := []core.Capability{core.CapLifecycle, core.CapIsolation, core.CapRollback, core.CapObservability}
	if !slices.Equal(core.ConformanceAreas(), want) {
		t.Fatalf("the conformance areas are %v, want %v", core.ConformanceAreas(), want)
	}
	for _, kind := range backendKinds {
		row := gradeNew(t, kind, func(core.CapabilitySet, map[string]error) {})
		if len(row.Areas) != len(want) {
			t.Errorf("%s: the matrix row covers %d areas, want %d", kind, len(row.Areas), len(want))
		}
		for _, area := range want {
			if row.Areas[area] != core.SupportSupported {
				t.Errorf("%s: %s is graded %q for a backend that proves it", kind, area, row.Areas[area])
			}
		}
		if row.Verdict != kit.VerdictSupported || len(row.Missing) != 0 {
			t.Errorf("%s: a backend proving all four areas is supported, got %q missing %v", kind, row.Verdict, row.Missing)
		}
	}
}

// What a backend must declare, or must be seen to do, for each area. Taking
// away either one leaves the area missing.
var areaProof = map[kit.Kind]map[core.Capability]struct {
	declared core.Capability
	method   string
}{
	kit.KindAgent: {
		core.CapLifecycle:     {core.CapLifecycle, "Start"},
		core.CapIsolation:     {core.CapIsolation, ""},
		core.CapRollback:      {core.CapAgentCancel, "Cancel"},
		core.CapObservability: {core.CapObservability, "Events"},
	},
	kit.KindExecution: {
		core.CapLifecycle:     {core.CapLifecycle, "CreateSandbox"},
		core.CapIsolation:     {core.CapExecEgressEnforcement, ""},
		core.CapRollback:      {core.CapRollback, "Destroy"},
		core.CapObservability: {core.CapObservability, "Inspect"},
	},
	kit.KindDeploy: {
		core.CapLifecycle:     {core.CapLifecycle, "Stage"},
		core.CapIsolation:     {core.CapIsolation, ""},
		core.CapRollback:      {core.CapDeployRollback, "Rollback"},
		core.CapObservability: {core.CapDeployObservedRevision, "Measure"},
	},
}

func TestANewBackendMissingAnyAreaIsMarkedUnsupported(t *testing.T) {
	broken := errors.New("not implemented by this backend")
	for _, kind := range backendKinds {
		for _, area := range core.ConformanceAreas() {
			proof := areaProof[kind][area]

			undeclared := gradeNew(t, kind, func(declared core.CapabilitySet, _ map[string]error) { delete(declared, proof.declared) })
			if undeclared.Verdict != kit.VerdictUnsupported || !slices.Contains(undeclared.Missing, area) {
				t.Errorf("%s not declaring %s: verdict %q, missing %v; want unsupported", kind, area, undeclared.Verdict, undeclared.Missing)
			}

			// Isolation cannot be shown by a probe that behaves well; it is
			// judged from the declaration alone.
			if proof.method == "" {
				continue
			}
			failing := gradeNew(t, kind, func(_ core.CapabilitySet, fail map[string]error) { fail[proof.method] = broken })
			if failing.Verdict != kit.VerdictUnsupported || !slices.Contains(failing.Missing, area) {
				t.Errorf("%s declaring %s but failing %s: verdict %q, missing %v; want unsupported", kind, area, proof.method, failing.Verdict, failing.Missing)
			}
		}
	}
}

func TestAnUnprovenClaimNeverMakesABackendSupported(t *testing.T) {
	for _, kind := range backendKinds {
		row := gradeNew(t, kind, func(declared core.CapabilitySet, _ map[string]error) {
			for capability := range declared {
				declared[capability] = core.SupportUnverified
			}
		})
		if row.Verdict != kit.VerdictExperimental {
			t.Errorf("%s: claims without proof must grade experimental, got %q", kind, row.Verdict)
		}
	}
}

func TestAManifestSelectsOnlyAModeWithASupportedProfile(t *testing.T) {
	for _, mode := range []manifest.Mode{"kubernetes", "tose", "cloudflare", "vercel", "render", "railway", ""} {
		m := docker(t)
		m.Mode = mode
		wantOnly(t, Check(m), RuleModeUnsupported, "mode")
	}
}

func TestAManifestSelectsOnlyBackendsTheMatrixHolds(t *testing.T) {
	m := docker(t)
	m.Adapters.Deploy.Name = "nomad"
	wantOnly(t, Check(m), RuleBackendUndeclared, "adapters.deploy.name")

	m = docker(t)
	m.Adapters.Execution.Name = "firecracker"
	wantOnly(t, Check(m), RuleBackendUndeclared, "adapters.execution.name")

	// A backend is known by interface and name together.
	m = docker(t)
	m.Adapters.Execution.Name = "compose"
	wantOnly(t, Check(m), RuleBackendUndeclared, "adapters.execution.name")
}

func TestAManifestSelectsOnlyBackendsThatRunInItsMode(t *testing.T) {
	m := docker(t)
	m.Adapters.Execution.Name = "local" // local processes inside a compose project
	wantOnly(t, Check(m), RuleBackendMode, "adapters.execution.name")

	m = standalone(t)
	m.Adapters.Deploy.Name = "compose"
	wantOnly(t, Check(m), RuleBackendMode, "adapters.deploy.name")

	// Containers for the runners are available to a standalone install too.
	m = standalone(t)
	m.Adapters.Execution.Name = "docker"
	if report := Check(m); !report.Conformant() {
		t.Fatalf("docker sandboxes run in standalone mode, got %+v", report.Findings)
	}
}

func TestAManifestAsksABackendOnlyForWhatItDeclares(t *testing.T) {
	// The process deployer has one switch and no traffic split.
	m := standalone(t)
	m.Release.Strategy = manifest.StrategyCanary
	wantOnly(t, Check(m), RuleBackendCapability, "release.strategy")

	// A new backend that declares no rollback and no metrics may be selected,
	// but the factory may not publish through it by itself.
	blind := kit.Declaration{Kind: kit.KindDeploy, Name: "blind", Modes: []manifest.Mode{manifest.ModeDocker},
		Capabilities: core.CapabilitySet{core.CapDeployCanary: core.SupportSupported, core.CapDeployObservedRevision: core.SupportSupported}}
	backends := append(kit.Builtin(), blind)
	m = docker(t)
	m.Adapters.Deploy.Name = "blind"
	report := checkWith(m, backends)
	wantOnly(t, report, RuleBackendCapability, "release.units")
	if len(report.Findings) != 2 {
		t.Fatalf("missing rollback and missing metrics are two findings, got %+v", report.Findings)
	}

	m.Release.Units = []manifest.UnitName{}
	if report := checkWith(m, backends); !report.Conformant() {
		t.Fatalf("with nothing self-published the backend needs no rollback, got %+v", report.Findings)
	}

	// The same backend without any way to switch versions cannot release at all.
	m.Release.Strategy = manifest.StrategyBlueGreen
	wantOnly(t, checkWith(m, backends), RuleBackendCapability, "release.strategy")
}

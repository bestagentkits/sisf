package conformance_test

import (
	"slices"
	"testing"

	"github.com/bestagentkits/sisf/internal/conformance/conformancetest"
	"github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// proven returns, per interface, a capability set in which all four areas
// are verified.
func proven() map[conformance.Kind]core.CapabilitySet {
	return map[conformance.Kind]core.CapabilitySet{
		conformance.KindAgent:     conformancetest.NewAgent("a").Declared,
		conformance.KindExecution: conformancetest.NewExecution("e").Declared,
		conformance.KindDeploy:    conformancetest.NewDeploy("d").Declared,
	}
}

func TestABackendLackingAnyAreaIsUnsupported(t *testing.T) {
	// The capability each area of each interface stands or falls with.
	evidence := map[conformance.Kind]map[core.Capability]core.Capability{
		conformance.KindAgent: {
			core.CapLifecycle: core.CapLifecycle, core.CapIsolation: core.CapIsolation,
			core.CapRollback: core.CapAgentCancel, core.CapObservability: core.CapObservability,
		},
		conformance.KindExecution: {
			core.CapLifecycle: core.CapLifecycle, core.CapIsolation: core.CapExecIsolation,
			core.CapRollback: core.CapRollback, core.CapObservability: core.CapObservability,
		},
		conformance.KindDeploy: {
			core.CapLifecycle: core.CapLifecycle, core.CapIsolation: core.CapIsolation,
			core.CapRollback: core.CapDeployRollback, core.CapObservability: core.CapDeployMetrics,
		},
	}
	for kind, declared := range proven() {
		whole := conformance.Grade(conformance.Declaration{Kind: kind, Name: "whole", Capabilities: declared})
		if whole.Verdict != conformance.VerdictExperimental || len(whole.Missing) != 0 {
			t.Fatalf("%s: a backend declaring all four areas lacks none and awaits a kit run, got %+v", kind, whole)
		}
		for _, area := range core.ConformanceAreas() {
			for _, how := range []string{"never declared", "declared absent"} {
				lacking := proven()[kind]
				if delete(lacking, evidence[kind][area]); how == "declared absent" {
					lacking[evidence[kind][area]] = core.SupportNotSupported
				}
				row := conformance.Grade(conformance.Declaration{Kind: kind, Name: "lacking", Capabilities: lacking})
				if row.Verdict != conformance.VerdictUnsupported || !slices.Equal(row.Missing, []core.Capability{area}) {
					t.Errorf("%s with %s %s: verdict %q, missing %v; want unsupported, missing [%s]", kind, area, how, row.Verdict, row.Missing, area)
				}
			}
		}
	}
}

func TestAClaimIsNotSupportUntilItIsProven(t *testing.T) {
	for kind := range proven() {
		claimed := proven()[kind]
		for capability := range claimed {
			claimed[capability] = core.SupportUnverified
		}
		row := conformance.Grade(conformance.Declaration{Kind: kind, Name: "claimed", Capabilities: claimed})
		if row.Verdict != conformance.VerdictExperimental {
			t.Errorf("%s: unverified claims must grade experimental, got %q", kind, row.Verdict)
		}
		// Areas declared verified still need a kit run before "supported".
		row = conformance.Grade(conformance.Declaration{Kind: kind, Name: "no-run", Capabilities: proven()[kind]})
		if row.Verdict != conformance.VerdictExperimental {
			t.Errorf("%s: without a kit run the grade must stay experimental, got %q", kind, row.Verdict)
		}
		// A level the factory does not know is silence, and silence is not support.
		odd := proven()[kind]
		odd[core.CapLifecycle] = "probably"
		row = conformance.Grade(conformance.Declaration{Kind: kind, Name: "odd", Capabilities: odd})
		if row.Verdict != conformance.VerdictUnsupported {
			t.Errorf("%s: an unknown support level must not count, got %q", kind, row.Verdict)
		}
	}
	silent := conformance.Grade(conformance.Declaration{Kind: conformance.KindExecution, Name: "silent"})
	if silent.Verdict != conformance.VerdictUnsupported || len(silent.Missing) != 4 {
		t.Errorf("a backend that declares nothing lacks all four areas, got %+v", silent)
	}
}

func TestIsolationOfASandboxNeedsBothProvenIsolationAndEnforcedEgress(t *testing.T) {
	for _, capability := range []core.Capability{core.CapExecIsolation, core.CapExecEgressEnforcement} {
		declared := proven()[conformance.KindExecution]
		declared[capability] = core.SupportUnverified
		// Declaring the area by name does not replace the two capabilities.
		declared[core.CapIsolation] = core.SupportSupported
		row := conformance.Grade(conformance.Declaration{Kind: conformance.KindExecution, Name: "hosted", Capabilities: declared})
		if row.Areas[core.CapIsolation] != core.SupportUnverified {
			t.Errorf("isolation with %s unverified = %q, want unverified", capability, row.Areas[core.CapIsolation])
		}
	}
}

func TestADeploymentBackendNeedsOneWayToSwitchVersions(t *testing.T) {
	declared := proven()[conformance.KindDeploy]
	delete(declared, core.CapDeployBlueGreen)
	row := conformance.Grade(conformance.Declaration{Kind: conformance.KindDeploy, Name: "no-switch", Capabilities: declared})
	if !slices.Equal(row.Missing, []core.Capability{core.CapLifecycle}) {
		t.Fatalf("without blue-green or canary the lifecycle is missing, got %v", row.Missing)
	}
	declared[core.CapDeployCanary] = core.SupportSupported
	row = conformance.Grade(conformance.Declaration{Kind: conformance.KindDeploy, Name: "canary-only", Capabilities: declared})
	if len(row.Missing) != 0 {
		t.Fatalf("canary alone is a way to switch versions, got missing %v", row.Missing)
	}
}

func TestMatrixGradesEveryBackendOnTheSameFourAreas(t *testing.T) {
	matrix := conformance.NewMatrix(conformance.Builtin()...)
	if len(matrix) != len(conformance.Builtin()) {
		t.Fatalf("the matrix has %d rows for %d declarations", len(matrix), len(conformance.Builtin()))
	}
	for i, row := range matrix {
		if len(row.Areas) != 4 {
			t.Errorf("%s/%s is graded on %d areas, want 4", row.Kind, row.Name, len(row.Areas))
		}
		for _, area := range core.ConformanceAreas() {
			if !row.Areas[area].Valid() {
				t.Errorf("%s/%s has no grade for %s", row.Kind, row.Name, area)
			}
		}
		if i > 0 && (matrix[i-1].Kind > row.Kind || matrix[i-1].Kind == row.Kind && matrix[i-1].Name >= row.Name) {
			t.Errorf("rows must be ordered by kind and name; %s/%s follows %s/%s", row.Kind, row.Name, matrix[i-1].Kind, matrix[i-1].Name)
		}
	}
	if _, ok := matrix.Row(conformance.KindDeploy, "kubernetes"); ok {
		t.Error("a backend that was never declared has no row")
	}
}

func TestBuiltinBackendsClaimNoMoreThanTheirDesignAllows(t *testing.T) {
	matrix := conformance.NewMatrix(conformance.Builtin()...)
	want := map[string]conformance.Verdict{
		// Local processes share the machine with the factory: no isolation.
		"execution/local":  conformance.VerdictUnsupported,
		"execution/docker": conformance.VerdictExperimental,
		"deploy/local":     conformance.VerdictExperimental,
		"deploy/compose":   conformance.VerdictExperimental,
		// The built-in agent runtimes of the brief, each only a claim so far.
		"agent/claude": conformance.VerdictExperimental,
		"agent/codex":  conformance.VerdictExperimental,
		"agent/pi":     conformance.VerdictExperimental,
	}
	for _, row := range matrix {
		key := string(row.Kind) + "/" + row.Name
		if row.Verdict != want[key] {
			t.Errorf("%s is graded %q, want %q", key, row.Verdict, want[key])
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Errorf("backends missing from the built-in matrix: %v", want)
	}
	local, _ := matrix.Row(conformance.KindExecution, "local")
	if !slices.Equal(local.Missing, []core.Capability{core.CapIsolation}) {
		t.Errorf("local execution lacks isolation and nothing else, got %v", local.Missing)
	}

	declarations := conformance.Builtin()
	processes, ok := conformance.Find(declarations, conformance.KindDeploy, "local")
	if !ok || processes.Capabilities.Level(core.CapDeployCanary) != core.SupportNotSupported {
		t.Error("the process deployer has no traffic split and must say so")
	}
	if slices.Contains(processes.Modes, manifest.ModeDocker) {
		t.Error("the process deployer does not run in docker mode")
	}
	if _, ok := conformance.Find(declarations, conformance.KindExecution, "compose"); ok {
		t.Error("a backend is found by kind and name together")
	}
	for _, name := range []string{"claude", "codex", "pi"} {
		runtime, ok := conformance.Find(declarations, conformance.KindAgent, name)
		if !ok || runtime.Capabilities.Level(core.CapAgentHardBudget) != core.SupportNotSupported {
			t.Errorf("agent runtime %q must be declared and must not claim a hard budget no provider enforces", name)
		}
	}
	for _, d := range declarations {
		if !d.Kind.Valid() || len(d.Modes) == 0 {
			t.Errorf("%s/%s must state its interface and its modes", d.Kind, d.Name)
		}
	}
}

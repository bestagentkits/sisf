package conformance

import (
	"strings"
	"testing"

	kit "github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Local processes share the machine with the factory. They cannot confine a
// run or hold it to the egress allowlist, so they are accepted only when the
// Owner states that nothing but trusted code runs there.
func TestABackendThatCannotSandboxNeedsTheOwnersTrustedCodeAcknowledgement(t *testing.T) {
	t.Parallel()
	m := standalone(t)
	if report := Check(m); !report.Conformant() || !report.TrustedCodeOnly {
		t.Fatalf("with the acknowledgement the manifest is conformant at reduced assurance, got %+v", report)
	}

	m.TrustedCodeOnly = false
	report := Check(m)
	wantOnly(t, report, RuleBackendCapability, "adapters.execution.name")
	wantOnly(t, report, RuleBackendCapability, "egress.mode")
	if len(report.Findings) != 2 || report.TrustedCodeOnly {
		t.Fatalf("missing isolation and missing egress enforcement are two findings, got %+v", report)
	}
	for _, finding := range report.Findings {
		for _, want := range []string{"reduced assurance", "trusted", `"local"`} {
			if !strings.Contains(finding.Message, want) {
				t.Errorf("the finding must explain what the acknowledgement gives up (%q), got %q", want, finding.Message)
			}
		}
		if !strings.Contains(finding.Fix, "trusted_code_only: true") {
			t.Errorf("the fix must name the acknowledgement, got %q", finding.Fix)
		}
	}

	// With blocking switched off by the Owner, only isolation is still asked for.
	m.Egress.Mode = core.EgressUnrestricted
	report = Check(m)
	wantOnly(t, report, RuleBackendCapability, "adapters.execution.name")
	if len(report.Findings) != 1 {
		t.Fatalf("without an allowlist only isolation is missing, got %+v", report.Findings)
	}

	// The acknowledgement is false unless the manifest states it.
	if unstated := docker(t); unstated.TrustedCodeOnly {
		t.Fatal("trusted_code_only must default to false")
	}

	// A backend that sandboxes needs no acknowledgement, and stating one
	// anyway does not lower what the report says about it.
	m = standalone(t)
	m.Adapters.Execution.Name, m.TrustedCodeOnly = "docker", false
	if report := Check(m); !report.Conformant() || report.TrustedCodeOnly {
		t.Fatalf("docker sandboxes need no acknowledgement, got %+v", report)
	}
	m = docker(t)
	m.TrustedCodeOnly = true
	if report := Check(m); !report.Conformant() || report.TrustedCodeOnly {
		t.Fatalf("an acknowledgement nothing relies on changes nothing, got %+v", report)
	}

	// Each capability is judged on its own: isolation without enforced egress.
	open := kit.Declaration{Kind: kit.KindExecution, Name: "open-sandbox", Modes: []manifest.Mode{manifest.ModeDocker},
		Capabilities: core.CapabilitySet{core.CapExecIsolation: core.SupportUnverified}}
	m = docker(t)
	m.Adapters.Execution.Name = "open-sandbox"
	report = checkWith(m, append(kit.Builtin(), open))
	wantOnly(t, report, RuleBackendCapability, "egress.mode")
	if len(report.Findings) != 1 {
		t.Fatalf("only egress enforcement is missing, got %+v", report.Findings)
	}
}

package conformance

import (
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/pkg/core"
)

// Widening the allowlist and turning blocking off are the Owner's to
// choose. They pass, and the risk is shown beside the verdict.
func TestAWideEgressPolicyIsAcceptedWithARiskBesideTheVerdict(t *testing.T) {
	t.Parallel()
	risky := map[string]func(p *core.EgressPolicy){
		"egress.mode":     func(p *core.EgressPolicy) { p.Mode = core.EgressUnrestricted },
		"egress.allow[0]": func(p *core.EgressPolicy) { p.Allow[0] = "*.com" },
		"egress.allow[1]": func(p *core.EgressPolicy) { p.Allow[1] = "*.co.uk" },
		"egress.allow[2]": func(p *core.EgressPolicy) { p.Allow = append(p.Allow, "*.io") },
	}
	for path, widen := range risky {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			m := docker(t)
			widen(&m.Egress)
			report := Check(m)
			if !report.Conformant() {
				t.Fatalf("a wide egress policy is the Owner's choice and passes, got %+v", report.Findings)
			}
			if len(report.Risks) != 1 || report.Risks[0].Path != path || report.Risks[0].Message == "" {
				t.Fatalf("want one risk at %s, got %+v", path, report.Risks)
			}
			if !slices.ContainsFunc(report.Notes(), func(note string) bool { return strings.HasPrefix(note, "risk: "+path+": ") }) {
				t.Errorf("the risk must be shown beside the verdict, got %q", report.Notes())
			}
		})
	}

	// With blocking off the list is kept but not in force, so only the mode is a risk.
	m := docker(t)
	m.Egress.Mode, m.Egress.Allow = core.EgressUnrestricted, []string{"*.com", "*.net"}
	if report := Check(m); len(report.Risks) != 1 || report.Risks[0].Path != "egress.mode" {
		t.Fatalf("want the one risk of blocking turned off, got %+v", report.Risks)
	}
}

func TestAnAllowlistOfNamedOrganizationsCarriesNoRisk(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"../../.sisf/factory.yaml", "testdata/valid-docker.yaml", "testdata/valid-standalone.yaml"} {
		if report := Check(load(t, path)); len(report.Risks) != 0 {
			t.Errorf("%s: the default allowlist is not a risk, got %+v", path, report.Risks)
		}
	}
	narrow := []string{"*.githubusercontent.com", "*.s3.amazonaws.com", "*.gh.io", "*.example.co.uk", "github.com", "com"}
	for _, host := range narrow {
		if wideWildcard(host) {
			t.Errorf("%q permits the hosts of one name, not of a public suffix", host)
		}
	}
	for _, host := range []string{"*.com", "*.io", "*.co.uk", "*.com.au", "*.org.uk"} {
		if !wideWildcard(host) {
			t.Errorf("%q permits every host under a public suffix", host)
		}
	}
}

func TestTheVerdictIsShownWithEveryReductionOfAssuranceInAFixedOrder(t *testing.T) {
	t.Parallel()
	plain := Check(docker(t)).Notes()
	if len(plain) != 1 || plain[0] != "self-update: enabled, the supervisor boundary holds" {
		t.Fatalf("a manifest with nothing to note says only whether it may self-update, got %q", plain)
	}

	m := standalone(t)
	m.ModelRoutes.SameModelRoute = true
	m.ModelRoutes.Routes[core.StepQA] = m.ModelRoutes.Routes[core.StepCoding]
	m.Egress.Mode = core.EgressUnrestricted
	m.Supervisor, m.Release.Units = nil, nil
	notes := Check(m).Notes()
	prefixes := []string{"self-update: disabled", "assurance: reduced, trusted code only", "assurance: reduced, same model route", "risk: egress.mode"}
	if len(notes) != len(prefixes) {
		t.Fatalf("want %d notes, got %q", len(prefixes), notes)
	}
	for i, prefix := range prefixes {
		if !strings.HasPrefix(notes[i], prefix) {
			t.Errorf("note %d = %q, want it to start with %q", i, notes[i], prefix)
		}
	}
}

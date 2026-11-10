package conformance

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/core"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// asTyped unwraps err into the typed error every action returns.
func asTyped(err error, target **core.Error) bool { return errors.As(err, target) }

func TestAConformantManifestHasNoError(t *testing.T) {
	report := Check(docker(t))
	if !report.Conformant() || report.Err() != nil || report.Has(RuleZoneMissing) {
		t.Fatalf("a conformant manifest has no findings and no error, got %+v", report)
	}
}

func TestARejectedManifestYieldsATypedActionableError(t *testing.T) {
	m := docker(t)
	m.Components.Agent = nil
	m.Supervisor.StartupSelfCheck = false
	m.Supervisor.DigestChange = manifest.DigestChangePipeline
	report := Check(m)

	var typed *core.Error
	if !asTyped(report.Err(), &typed) {
		t.Fatalf("want a typed error, got %v", report.Err())
	}
	if typed.Code != core.CodeInvalidArgument || !strings.Contains(typed.Message, "3 findings") {
		t.Errorf("the error must carry the code and the count, got %v", typed)
	}
	if typed.Details["findings"] != "3" || typed.Details["rules"] != "supervisor.digest_change_by_pipeline,supervisor.self_check_missing,zone.missing" {
		t.Errorf("the error must name the failed rules in a stable order, got %v", typed.Details)
	}
	if len(typed.NextActions) != 1 || typed.NextActions[0].Action != "conformance.check" || typed.NextActions[0].Description == "" {
		t.Errorf("the error must say what to do next, got %+v", typed.NextActions)
	}

	m = docker(t)
	m.Components.Agent = nil
	if err := Check(m).Err(); !strings.Contains(err.Error(), "1 finding") || strings.Contains(err.Error(), "findings") {
		t.Errorf("one finding is counted in the singular, got %v", err)
	}
}

func TestEveryFindingIsReportedInOneRun(t *testing.T) {
	m := docker(t)
	m.Components.Control = nil
	delete(m.Units, manifest.UnitEdge)
	m.Supervisor.Digest = "latest"
	m.Components.Agent.StoreRole = m.Components.Application.StoreRole
	m.FactoryID = m.FactoryID + "!"
	m.Mode = "vercel"
	report := Check(m)
	for _, rule := range []Rule{RuleZoneMissing, RuleUnitMissing, RuleSupervisorNotPinned, RuleSharedStoreRole, RuleIdentityInvalid, RuleModeUnsupported} {
		if !report.Has(rule) {
			t.Errorf("rule %s must be reported together with the others, got %+v", rule, report.Findings)
		}
	}
}

func TestASchemaViolationIsReportedWhereNoRuleSpoke(t *testing.T) {
	m := docker(t)
	editUnit(m, manifest.UnitApp, func(u *manifest.Unit) { u.Port = 70000 })
	wantOnly(t, Check(m), RuleSchema, "units.app.port")

	// A missing zone breaks the schema too; it is reported once, by the rule.
	m = docker(t)
	m.Components.Agent = nil
	if report := Check(m); len(report.Findings) != 1 || report.Has(RuleSchema) {
		t.Fatalf("want the rule's finding alone, got %+v", report.Findings)
	}
}

func TestATaskAlwaysHasBounds(t *testing.T) {
	cases := map[string]func(l *manifest.Limits){
		"limits.run_timeout":     func(l *manifest.Limits) { l.RunTimeout = 0 },
		"limits.max_attempts":    func(l *manifest.Limits) { l.MaxAttempts = 0 },
		"limits.concurrency":     func(l *manifest.Limits) { l.Concurrency = -1 },
		"limits.clarify_timeout": func(l *manifest.Limits) { l.ClarifyTimeout = 0 },
	}
	for path, unbound := range cases {
		m := docker(t)
		// Unlimited money never lifts these limits.
		m.Budget.Mode = core.BudgetUnlimited
		unbound(&m.Limits)
		wantOnly(t, Check(m), RuleLimitsUnbounded, path)
	}
}

func TestCheckingNothingIsAFinding(t *testing.T) {
	report := Check(nil)
	if report.Conformant() || !core.HasCode(report.Err(), core.CodeInvalidArgument) {
		t.Fatalf("no manifest is not a conformant manifest, got %+v", report)
	}
}

func TestCheckFileSeparatesAnUnreadableFileFromANonConformantOne(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "factory.yaml")
	report, err := CheckFile(missing)
	if !core.HasCode(err, core.CodeNotFound) || report.Source != missing {
		t.Fatalf("a missing file is NOT_FOUND, got %v", err)
	}
	report, err = CheckFile("testdata/reject-missing-zone-agent.yaml")
	if err != nil || report.Conformant() {
		t.Fatalf("a readable, non-conformant file is a report with findings, got %+v, %v", report, err)
	}
}

func TestRuleNamesAreUniqueAndStable(t *testing.T) {
	rules := Rules()
	seen := map[Rule]bool{}
	for _, rule := range rules {
		if seen[rule] || !strings.Contains(string(rule), ".") || strings.ToLower(string(rule)) != string(rule) {
			t.Errorf("rule name %q must be unique, lowercase and of the form area.name", rule)
		}
		seen[rule] = true
	}
	// The names the outcome of this checker is described by must not change.
	for _, rule := range []Rule{"zone.missing", "supervisor.digest_not_pinned", "supervisor.trust_volume_exposed",
		"supervisor.digest_change_by_pipeline", "supervisor.self_check_missing", "backend.capability_mismatch"} {
		if !slices.Contains(rules, rule) {
			t.Errorf("rule %s is gone", rule)
		}
	}
}

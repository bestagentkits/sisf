package conformance

import (
	"fmt"
	"strings"

	kit "github.com/bestagentkits/sisf/pkg/conformance"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// Check runs every conformance rule against m, judging the selected backends
// by the built-in capability matrix.
func Check(m *manifest.Manifest) Report {
	return checkWith(m, kit.Builtin())
}

// CheckFile parses the manifest at path and checks it. The error is non-nil
// only when the file cannot be read or parsed; a manifest that was read and
// found wanting is a Report with findings.
func CheckFile(path string) (Report, error) {
	m, err := manifest.ParseFile(path)
	if err != nil {
		return Report{Source: path}, err
	}
	report := Check(m)
	report.Source = path
	return report, nil
}

// checker collects the findings of one run.
type checker struct {
	m        *manifest.Manifest
	backends []kit.Declaration
	findings []Finding
}

// add records a finding. The message is formatted with args.
func (c *checker) add(rule Rule, path, fix, message string, args ...any) {
	c.findings = append(c.findings, Finding{Rule: rule, Path: path, Message: fmt.Sprintf(message, args...), Fix: fix})
}

// checkWith runs every rule against m, judging backends by the given
// declarations.
func checkWith(m *manifest.Manifest, backends []kit.Declaration) Report {
	if m == nil {
		return Report{Findings: []Finding{{Rule: RuleSchema, Message: "no manifest was given"}}}
	}
	c := &checker{m: m, backends: backends}
	c.checkIdentity()
	c.checkZones()
	c.checkUnits()
	c.checkNetworks()
	c.checkSupervisor()
	c.checkIsolation()
	c.checkBackends()
	c.checkLimits()

	report := Report{Findings: c.findings}
	report.SelfUpdate = m.Supervisor != nil && !hasSupervisorFinding(report.Findings)
	// A schema violation at a place a rule already reported adds nothing: the
	// rule's finding says what is wrong there and how to fix it.
	reported := map[string]bool{}
	for _, finding := range report.Findings {
		reported[finding.Path] = true
	}
	for _, violation := range manifest.Validate(m) {
		if !reported[violation.Path] {
			report.Findings = append(report.Findings, Finding{
				Rule: RuleSchema, Path: violation.Path, Message: violation.Message,
				Fix: "make the value match schemas/factory.schema.json",
			})
		}
	}
	return report
}

// hasSupervisorFinding reports whether any supervisor-boundary rule failed.
func hasSupervisorFinding(findings []Finding) bool {
	for _, finding := range findings {
		if strings.HasPrefix(string(finding.Rule), "supervisor.") {
			return true
		}
	}
	return false
}

package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/core"
)

// The files in testdata are whole manifests. valid-docker.yaml and
// valid-standalone.yaml are conformant; every reject-*.yaml is one of them
// with a single defect, and its second line names what must reject it:
//
//	# expect: <rule>       the checker reports findings of this rule only
//	# expect: unreadable   the file is refused before any rule runs

// expectation reads the "# expect:" line of a fixture.
func expectation(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rule, ok := strings.CutPrefix(line, "# expect: "); ok {
			return strings.TrimSpace(rule)
		}
	}
	t.Fatalf("%s does not say what it expects", path)
	return ""
}

func rejectFixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("testdata/reject-*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no reject fixtures found: %v", err)
	}
	return paths
}

func TestValidExamplesAreConformant(t *testing.T) {
	examples := []string{"../../.sisf/factory.yaml", "testdata/valid-docker.yaml", "testdata/valid-standalone.yaml"}
	modes := map[string]bool{}
	for _, path := range examples {
		report, err := CheckFile(path)
		if err != nil {
			t.Fatalf("%s must be readable: %v", path, err)
		}
		if !report.Conformant() || report.Err() != nil || !report.SelfUpdate || report.Source != path {
			t.Errorf("%s must be conformant with self-update enabled, got %+v", path, report)
		}
		modes[string(load(t, path).Mode)] = true
	}
	if !modes["docker"] || !modes["standalone"] {
		t.Fatalf("both supported modes need a valid example, got %v", modes)
	}
}

func TestEveryRejectFixtureIsRejectedByTheRuleItNames(t *testing.T) {
	for _, path := range rejectFixtures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			want := expectation(t, path)
			report, err := CheckFile(path)
			if want == "unreadable" {
				if !core.HasCode(err, core.CodeInvalidArgument) {
					t.Fatalf("want the file refused as INVALID_ARGUMENT, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("the fixture must parse so that the rule can judge it: %v", err)
			}
			if report.Conformant() {
				t.Fatalf("the manifest was accepted; want %s", want)
			}
			for _, finding := range report.Findings {
				if string(finding.Rule) != want {
					t.Errorf("a fixture holds one defect, yet it also fails [%s] at %s: %s", finding.Rule, finding.Path, finding.Message)
				}
				if finding.Path == "" || finding.Message == "" || finding.Fix == "" {
					t.Errorf("finding %+v must say where, what and how to fix", finding)
				}
			}
			var typed *core.Error
			if failure := report.Err(); !asTyped(failure, &typed) || typed.Code != core.CodeInvalidArgument ||
				typed.Details["rules"] != want || len(typed.NextActions) == 0 || typed.NextActions[0].Params["file"] != path {
				t.Errorf("the error must be typed and actionable, got %+v", typed)
			}
		})
	}
}

func TestEveryRuleHasARejectFixture(t *testing.T) {
	covered := map[string]bool{}
	for _, path := range rejectFixtures(t) {
		covered[expectation(t, path)] = true
	}
	for _, rule := range Rules() {
		if !covered[string(rule)] {
			t.Errorf("rule %s has no reject fixture in testdata", rule)
		}
	}
	if !covered["unreadable"] {
		t.Error("no fixture shows a manifest that is refused before the rules run")
	}
}

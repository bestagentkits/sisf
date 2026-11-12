package conformance

import (
	"strings"
	"testing"
)

// Each refusal of a declared unit input says which pattern, what it can
// match and why that cannot be a self-published input, in words the Owner
// can act on.
func TestARefusedUnitInputPatternNamesAPathItMatches(t *testing.T) {
	for fixture, want := range map[string][]string{
		"testdata/reject-release-unit-inputs-protected-foundation-directory.yaml": {`".sisf/*"`, ".sisf/", "protected"},
		"testdata/reject-release-unit-inputs-protected-go-mod.yaml":               {`"go.mod"`, "go.mod", "protected"},
		"testdata/reject-release-unit-inputs-protected-supervisor.yaml":           {`"cmd/supervisor/*"`, "cmd/supervisor/", "protected"},
		"testdata/reject-release-unit-inputs-protected-workflows.yaml":            {`".github/workflows/*"`, ".github/", "protected"},
		"testdata/reject-release-unit-inputs-go-code.yaml":                        {`"tools/runner/*"`, ".go", "application alone"},
		"testdata/reject-release-unit-inputs-dashboard-templates.yaml":            {`"ui/templates/*.html"`, "ui/", "protected"},
		"testdata/reject-release-unit-inputs-dashboard-static.yaml":               {`"ui/static/*.js"`, "ui/", "protected"},
		"testdata/reject-release-unit-inputs-overlap.yaml":                        {"web/", "consumes as well"},
	} {
		report, err := CheckFile(fixture)
		if err != nil {
			t.Fatalf("%s: %v", fixture, err)
		}
		if len(report.Findings) == 0 {
			t.Fatalf("%s was accepted", fixture)
		}
		for _, f := range report.Findings {
			if !strings.HasPrefix(f.Path, "release.unit_inputs.") {
				t.Errorf("%s: the finding is at %q, want the pattern's place", fixture, f.Path)
			}
			for _, w := range want {
				if !strings.Contains(f.Message, w) {
					t.Errorf("%s: %q does not say %q", fixture, f.Message, w)
				}
			}
		}
	}
}

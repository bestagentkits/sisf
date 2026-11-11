package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// greetingAndConformanceDiff changes the application's greeting and adds a
// file to a package the installer links, which the application does not.
const greetingAndConformanceDiff = greetingDiff + `--- /dev/null
+++ b/internal/conformance/zz.go
@@ -0,0 +1 @@
+package conformance
`

// dashboardTemplateDiff adds a dashboard template, a static input of the
// control unit.
const dashboardTemplateDiff = `--- /dev/null
+++ b/ui/templates/dash-checked.html
@@ -0,0 +1 @@
+<p>Last checked by the factory.</p>
`

// dashboardGoDiff adds Go code to the dashboard's package, which the
// installer and the simulation link as well as the control unit.
const dashboardGoDiff = `--- /dev/null
+++ b/ui/zz.go
@@ -0,0 +1 @@
+package ui
`

// wantNotMerged fails the test when anything reached main.
func wantNotMerged(t *testing.T, f *harness.Factory) {
	t.Helper()
	if merges := f.GitData.Merges(); merges != 0 {
		t.Fatalf("%d merges reached main, want none", merges)
	}
}

// A candidate of the application that also changes a path the application
// does not consume is refused for the Owner's approval: observing the
// application cannot vouch for it. Nothing is deployed or merged.
func TestAnApplicationCandidateThatChangesAPathTheApplicationDoesNotConsumeIsRefused(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingAndConformanceDiff)
	f.Cycle(6)

	rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
	if !strings.Contains(rec.Reason, "internal/conformance/zz.go") || !strings.Contains(rec.Reason, "Owner's approval") {
		t.Fatalf("the refusal does not name the path that needs the Owner: %+v", rec)
	}
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("the candidate was deployed: %+v", staged)
	}
	wantNotMerged(t, f)
}

// A candidate that changes only the application's inputs is released,
// verified and merged by the factory itself.
func TestAnApplicationCandidateOfItsOwnInputsIsReleasedAndMerged(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff)
	f.Cycle(4)

	wantSurfaces(t, f, number, core.StateClosed)
	if merges := f.GitData.Merges(); merges != 1 {
		t.Fatalf("%d merges reached main, want one", merges)
	}
}

// A new dashboard template is an input of the control unit: with control in
// release.units it is released, observed and verified as the control unit.
func TestADashboardTemplateIsReleasedAsTheControlUnit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := harness.New(t, harness.Options{ReleaseUnits: []manifest.UnitName{manifest.UnitApp, manifest.UnitControl}})
	installed := f.Trust.Installed["control"]
	number := f.OpenIssue("maintainer", "MEMBER", "Say who checked the status", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, dashboardTemplateDiff)
	f.Cycle(4)

	wantSurfaces(t, f, number, core.StateClosed)
	good, found, err := f.Trust.LKG.Get(ctx, "control")
	if err != nil || !found || good.Digest == installed || core.Digest(good.Digest) != f.Record(number).CandidateDigest {
		t.Fatalf("the control unit built from the template is not the last known good: %+v, %v", good, err)
	}
	for _, rev := range f.Deploy.Staged() {
		if rev.Unit != "control" {
			t.Fatalf("a unit other than control was deployed: %+v", rev)
		}
	}
}

// Without control in release.units the same template is refused for a
// person, and nothing is deployed or merged.
func TestADashboardTemplateIsRefusedWhenControlIsNotAReleaseUnit(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Say who checked the status", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, dashboardTemplateDiff)
	f.Cycle(6)

	wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("the control unit was deployed: %+v", staged)
	}
	wantNotMerged(t, f)
}

// Go code of the dashboard is linked by the installer and the simulation
// too, so one observed release of the control unit cannot vouch for it: it
// is refused for the Owner even with control in release.units.
func TestGoCodeOfTheDashboardIsRefusedEvenWhenControlIsAReleaseUnit(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{ReleaseUnits: []manifest.UnitName{manifest.UnitApp, manifest.UnitControl}})
	number := f.OpenIssue("maintainer", "MEMBER", "Say who checked the status", dashboardBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, dashboardGoDiff)
	f.Cycle(6)

	rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
	if !strings.Contains(rec.Reason, "ui/zz.go") {
		t.Fatalf("the refusal does not name the dashboard's Go file: %+v", rec)
	}
	if staged := f.Deploy.Staged(); len(staged) != 0 {
		t.Fatalf("the candidate was deployed: %+v", staged)
	}
	wantNotMerged(t, f)
}

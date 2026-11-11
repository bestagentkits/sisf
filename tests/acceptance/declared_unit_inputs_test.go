package acceptance

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bestagentkits/sisf/cmd/supervisor/stages"
	"github.com/bestagentkits/sisf/internal/testkit/harness"
	"github.com/bestagentkits/sisf/pkg/core"
	"github.com/bestagentkits/sisf/pkg/github"
	"github.com/bestagentkits/sisf/pkg/manifest"
)

// guideDiff adds an operator guide, which no unit consumes by default.
const guideDiff = `--- /dev/null
+++ b/docs/guides/greeting.md
@@ -0,0 +1 @@
+The application greets the world.
`

// guideInputs declares the application's inputs as its own directory and
// the operator guides.
var guideInputs = map[manifest.UnitName][]string{manifest.UnitApp: {"examples/hello-app/*", "docs/guides/*.md"}}

// With the application's inputs declared in the installed manifest, a
// candidate of those inputs is released, verified and merged by the
// factory itself; without the declaration the same candidate is refused for
// the Owner.
func TestACandidateOfDeclaredInputsIsReleasedAndMerged(t *testing.T) {
	t.Parallel()
	for _, declared := range []bool{true, false} {
		opts := harness.Options{}
		if declared {
			opts.UnitInputs = guideInputs
		}
		f := harness.New(t, opts)
		number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
		f.ScriptPatch(number, 0, greetingDiff+guideDiff)
		f.Cycle(6)
		if !declared {
			rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
			if !strings.Contains(rec.Reason, "docs/guides/greeting.md") {
				t.Fatalf("the refusal does not name the guide: %+v", rec)
			}
			wantNotMerged(t, f)
			continue
		}
		wantSurfaces(t, f, number, core.StateClosed)
		if merges := f.GitData.Merges(); merges != 1 {
			t.Fatalf("%d merges reached main, want one", merges)
		}
	}
}

// manifestWideningDiff is a patch of the repository's .sisf/factory.yaml
// that declares the operator guides an input of the application.
func manifestWideningDiff(t *testing.T, f *harness.Factory) string {
	t.Helper()
	_, files, ok := f.GitData.Branch(f.Repo, "main")
	if !ok {
		t.Fatal("main has no files")
	}
	lines := strings.Split(files[".sisf/factory.yaml"], "\n")
	at := slices.Index(lines, "  units: [app]")
	if at < 0 {
		t.Fatal("the repository's manifest does not list release.units as [app]")
	}
	return fmt.Sprintf("--- a/.sisf/factory.yaml\n+++ b/.sisf/factory.yaml\n@@ -%[1]d,1 +%[1]d,3 @@\n-  units: [app]\n+  units: [app]\n"+
		"+  unit_inputs:\n+    app: [\"examples/hello-app/*\", \"docs/guides/*.md\"]\n", at+1)
}

// A candidate cannot widen its own inputs: one that edits the repository's
// .sisf/factory.yaml to declare more inputs is refused before anything is
// pushed, and the supervisor keeps judging by the manifest it was installed
// with, so the next attempt, changing the same guide, is refused for the
// Owner.
func TestACandidateCannotWidenItsOwnInputsThroughTheRepositorysManifest(t *testing.T) {
	t.Parallel()
	f := harness.New(t, harness.Options{})
	number := f.OpenIssue("maintainer", "MEMBER", "Greet the world", bugBody, github.LabelReady, github.LabelKindBug)
	f.ScriptPatch(number, 0, greetingDiff+guideDiff+manifestWideningDiff(t, f))
	f.ScriptPatch(number, 0, greetingDiff+guideDiff)

	record := exportedPatch(t, f, number)
	res := f.Stages.Build.Pass(context.Background())
	if len(res.Errors) > 0 || !slices.Equal(res.Unbuildable, []core.IssueRef{f.Issue(number)}) {
		t.Fatalf("the build pass: %+v", res)
	}
	doc := candidateOf(t, f, record)
	if doc.BuildRefusal != string(stages.RefusalProtectedPath) || !strings.Contains(doc.BuildFailure, ".sisf/factory.yaml") {
		t.Fatalf("the refusal was recorded as %q: %q", doc.BuildRefusal, doc.BuildFailure)
	}

	f.Cycle(8)
	rec := wantRefusedAndBlocked(t, f, number, core.CodeApprovalRequired)
	if !strings.Contains(rec.Reason, "docs/guides/greeting.md") {
		t.Fatalf("the next attempt was not refused for the guide: %+v", rec)
	}
	if len(f.Manifest.Release.UnitInputs) != 0 {
		t.Fatalf("the installed manifest's inputs changed: %v", f.Manifest.Release.UnitInputs)
	}
	wantNotMerged(t, f)
}
